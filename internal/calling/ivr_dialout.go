package calling

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
)

// executeDialOut places an outbound WhatsApp call to another number and bridges
// audio with the current caller until hangup, max duration, or no answer.
// Outcomes: completed, no_answer, error.
func (m *Manager) executeDialOut(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer, waAccount *whatsapp.Account) string {
	phone, _ := node.Config["phone"].(string)
	phone = strings.TrimSpace(interpolateTemplate(phone, ctx.Variables))
	if phone == "" {
		m.log.Error("IVR dial_out missing phone", "call_id", session.ID)
		return "error"
	}
	if waAccount == nil || m.whatsapp == nil {
		return "error"
	}
	ringSecs := getConfigInt(node.Config, "ring_seconds", 45)
	if ringSecs < 5 {
		ringSecs = 5
	}
	if ringSecs > 120 {
		ringSecs = 120
	}
	talkSecs := getConfigInt(node.Config, "max_talk_seconds", 300)
	if talkSecs < 10 {
		talkSecs = 10
	}

	prompt := m.resolvePromptAudio(node, ctx)
	if prompt != "" && m.config.AudioDir != "" && player != nil {
		_, _ = player.PlayFile(filepath.Join(m.config.AudioDir, prompt))
	}

	outPC, err := m.createPeerConnection()
	if err != nil {
		m.log.Error("IVR dial_out PC create failed", "error", err, "call_id", session.ID)
		return "error"
	}

	outLocal, err := createOpusTrack(outPC, "ivr-dialout")
	if err != nil {
		_ = outPC.Close()
		m.log.Error("IVR dial_out track failed", "error", err, "call_id", session.ID)
		return "error"
	}

	remoteCh := make(chan *webrtc.TrackRemote, 1)
	outPC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Codec().MimeType == "audio/telephone-event" {
			return
		}
		select {
		case remoteCh <- track:
		default:
		}
	})

	offer, err := outPC.CreateOffer(nil)
	if err != nil {
		_ = outPC.Close()
		return "error"
	}
	if err := outPC.SetLocalDescription(offer); err != nil {
		_ = outPC.Close()
		return "error"
	}
	gatherDone := webrtc.GatheringCompletePromise(outPC)
	select {
	case <-gatherDone:
	case <-time.After(5 * time.Second):
	}
	local := outPC.LocalDescription()
	if local == nil {
		_ = outPC.Close()
		return "error"
	}

	callCtx, cancel := context.WithTimeout(context.Background(), time.Duration(ringSecs)*time.Second)
	defer cancel()

	outCallID, err := m.whatsapp.InitiateCall(callCtx, waAccount, whatsapp.Recipient{Phone: phone}, local.SDP)
	if err != nil {
		_ = outPC.Close()
		m.log.Error("IVR dial_out InitiateCall failed", "error", err, "call_id", session.ID, "phone", phone)
		return "no_answer"
	}
	m.log.Info("IVR dial_out initiated", "call_id", session.ID, "out_call_id", outCallID, "phone", phone)

	// Register a temporary session so the webhook can deliver the SDP answer.
	pending := &CallSession{
		ID:             outCallID,
		OrganizationID: session.OrganizationID,
		AccountName:    session.AccountName,
		CallerPhone:    phone,
		Status:         models.CallStatusInitiating,
		Direction:      models.CallDirectionOutgoing,
		StartedAt:      time.Now(),
		SDPAnswerReady: make(chan string, 1),
		BridgeStarted:  make(chan struct{}),
		CallLogID:      uuid.Nil,
	}
	m.mu.Lock()
	m.sessions[outCallID] = pending
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.sessions, outCallID)
		m.mu.Unlock()
		_ = outPC.Close()
	}()

	// Wait for SDP answer via webhook, then set remote description.
	select {
	case sdpAnswer := <-pending.SDPAnswerReady:
		if err := outPC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  sdpAnswer,
		}); err != nil {
			m.log.Error("IVR dial_out set remote SDP failed", "error", err, "out_call_id", outCallID)
			_ = m.whatsapp.TerminateCall(context.Background(), waAccount, outCallID)
			return "error"
		}
	case <-time.After(time.Duration(ringSecs) * time.Second):
		m.log.Warn("IVR dial_out SDP answer timeout", "out_call_id", outCallID)
		_ = m.whatsapp.TerminateCall(context.Background(), waAccount, outCallID)
		return "no_answer"
	}

	var remote *webrtc.TrackRemote
	select {
	case remote = <-remoteCh:
	case <-time.After(time.Duration(ringSecs) * time.Second):
		m.log.Warn("IVR dial_out no remote audio", "call_id", session.ID, "out_call_id", outCallID)
		_ = m.whatsapp.TerminateCall(context.Background(), waAccount, outCallID)
		return "no_answer"
	}

	session.mu.Lock()
	callerRemote := session.CallerRemoteTrack
	callerLocal := session.AudioTrack
	if session.Direction == models.CallDirectionOutgoing {
		callerLocal = session.WAAudioTrack
	}
	session.mu.Unlock()
	if callerRemote == nil || callerLocal == nil {
		_ = m.whatsapp.TerminateCall(context.Background(), waAccount, outCallID)
		return "error"
	}

	done := make(chan struct{})
	go forwardRTP(callerRemote, outLocal, done)
	go forwardRTP(remote, callerLocal, done)

	timer := time.NewTimer(time.Duration(talkSecs) * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			close(done)
			_ = m.whatsapp.TerminateCall(context.Background(), waAccount, outCallID)
			return "completed"
		case <-ticker.C:
			session.mu.Lock()
			status := session.Status
			session.mu.Unlock()
			if status != models.CallStatusAnswered {
				close(done)
				_ = m.whatsapp.TerminateCall(context.Background(), waAccount, outCallID)
				return "completed"
			}
		}
	}
}

func forwardRTP(src *webrtc.TrackRemote, dst *webrtc.TrackLocalStaticRTP, done <-chan struct{}) {
	buf := make([]byte, 1500)
	for {
		select {
		case <-done:
			return
		default:
		}
		n, _, err := src.Read(buf)
		if err != nil {
			return
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}
