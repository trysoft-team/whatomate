package calling

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/trysoft/whatomate/internal/models"
)

// aiCallExit is how a realtime AI Call session ended.
type aiCallExit struct {
	Type   string // transfer | hangup | error
	TeamID string
	Reason string
}

// mapAICallExit turns a session exit into an AI Call node outcome. Transfers
// record the target team and reason for the following transfer node.
func mapAICallExit(ev aiCallExit, defaultTeam string, ctx *IVRContext) string {
	switch strings.ToLower(strings.TrimSpace(ev.Type)) {
	case "transfer":
		team := strings.TrimSpace(ev.TeamID)
		if team == "" {
			team = defaultTeam
		}
		if ctx != nil {
			if ctx.Variables == nil {
				ctx.Variables = map[string]string{}
			}
			if team != "" {
				ctx.Variables["prefer_team_id"] = team
			}
			if ev.Reason != "" {
				ctx.Variables["transfer_reason"] = ev.Reason
			}
		}
		return "transfer"
	case "error":
		return "error"
	default:
		return "completed"
	}
}

var geminiVoices = []string{
	"Achernar", "Achird", "Algenib", "Algieba", "Alnilam", "Aoede", "Autonoe",
	"Callirrhoe", "Charon", "Despina", "Enceladus", "Erinome", "Fenrir",
	"Gacrux", "Iapetus", "Kore", "Laomedeia", "Leda", "Orus", "Puck",
	"Pulcherrima", "Rasalgethi", "Sadachbia", "Sadaltager", "Schedar",
	"Sulafat", "Umbriel", "Vindemiatrix", "Zephyr", "Zubenelgenubi",
}

// geminiVoice maps node/org voice settings to a Gemini prebuilt voice in its
// canonical casing. Unknown (e.g. ElevenLabs) IDs fall back to Puck.
func geminiVoice(voice string) string {
	v := strings.TrimSpace(voice)
	for _, name := range geminiVoices {
		if strings.EqualFold(name, v) {
			return name
		}
	}
	return "Puck"
}

// resolveAICallPlayer returns the AudioPlayer on the track the caller hears.
func (m *Manager) resolveAICallPlayer(session *CallSession, player *AudioPlayer) (*AudioPlayer, error) {
	session.mu.Lock()
	outTrack := session.AudioTrack
	if session.Direction == models.CallDirectionOutgoing && session.WAAudioTrack != nil {
		outTrack = session.WAAudioTrack
	}
	if player == nil {
		player = session.IVRPlayer
	}
	session.mu.Unlock()
	if outTrack == nil {
		return nil, fmt.Errorf("no outbound audio track")
	}
	if player == nil {
		player = NewAudioPlayer(outTrack)
		session.mu.Lock()
		session.IVRPlayer = player
		session.mu.Unlock()
	}
	return player, nil
}

// waitCallerRemoteTrack polls until OnTrack stores the caller's Opus track.
// WhatsApp often delivers it 1–2s after the IVR starts sending audio.
func (m *Manager) waitCallerRemoteTrack(ctx context.Context, session *CallSession, timeout time.Duration) (*webrtc.TrackRemote, error) {
	deadline := time.Now().Add(timeout)
	for {
		session.mu.Lock()
		callerTrack := session.CallerRemoteTrack
		if session.Direction == models.CallDirectionOutgoing {
			callerTrack = session.WARemoteTrack
		}
		session.mu.Unlock()
		if callerTrack != nil {
			return callerTrack, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no caller remote track")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// restartCallerAudioConsumer recreates BridgeStarted and resumes DTMF/audio tee
// so a subsequent transfer can take exclusive track ownership.
func (m *Manager) restartCallerAudioConsumer(session *CallSession) {
	session.mu.Lock()
	session.BridgeStarted = make(chan struct{})
	var track *webrtc.TrackRemote
	if session.Direction == models.CallDirectionOutgoing {
		track = session.WARemoteTrack
	} else {
		track = session.CallerRemoteTrack
	}
	session.mu.Unlock()
	if track != nil {
		go m.consumeAudioWithDTMF(session, track)
	}
}

func boolFromConfig(cfg map[string]any, key string, def bool) bool {
	if cfg == nil {
		return def
	}
	v, ok := cfg[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		if s == "true" || s == "1" || s == "yes" {
			return true
		}
		if s == "false" || s == "0" || s == "no" {
			return false
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return def
}

// WriteOpusPayload sends one 20 ms Opus frame as an RTP packet on the player's track.
func (p *AudioPlayer) WriteOpusPayload(opus []byte) error {
	if p == nil || p.track == nil || len(opus) == 0 {
		return nil
	}
	const samplesPerFrame = 960
	rtpPkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: p.sequenceNumber,
			Timestamp:      p.timestamp,
			SSRC:           1,
		},
		Payload: opus,
	}
	if err := p.track.WriteRTP(rtpPkt); err != nil {
		return err
	}
	p.sequenceNumber++
	p.timestamp += samplesPerFrame
	return nil
}
