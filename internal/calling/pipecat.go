package calling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/models"
)

// PipecatControlEvent is signaled from the sidecar (via HTTP) or the WS reader.
type PipecatControlEvent struct {
	Type   string // transfer | hangup | error | completed
	TeamID string
	Reason string
	Error  string
}

// pipecatBootstrap is POSTed to the sidecar before the audio WebSocket opens.
type pipecatBootstrap struct {
	SessionID      string           `json:"session_id"`
	CallID         string           `json:"call_id"`
	OrganizationID string           `json:"organization_id"`
	AccountName    string           `json:"account_name"`
	CallerPhone    string           `json:"caller_phone"`
	CallerName     string           `json:"caller_name"`
	SystemPrompt   string           `json:"system_prompt"`
	WelcomeMessage string           `json:"welcome_message"`
	TransferTeamID string           `json:"transfer_team_id"`
	Locale         string           `json:"locale"`
	Provider       string           `json:"llm_provider"`
	Model          string           `json:"llm_model"`
	APIKey         string           `json:"llm_api_key"`
	MaxTokens      int              `json:"llm_max_tokens"`
	VoiceProvider  string           `json:"voice_provider"`
	VoiceID        string           `json:"voice_id"`
	OpenAIKey      string           `json:"openai_key"` // optional Whisper / OpenAI TTS fallback
	Tools          []map[string]any `json:"tools"`
	CallbackURL    string           `json:"callback_url"`
	CallbackToken  string           `json:"callback_token"`
	MaxSessionSecs int              `json:"max_session_secs"`
	// Recording / transcription (AI Call node options)
	RecordingEnabled     bool `json:"recording_enabled"`
	RecordUser           bool `json:"record_user"`
	RecordAssistant      bool `json:"record_assistant"`
	RecordStereo         bool `json:"record_stereo"`
	TranscriptionEnabled bool `json:"transcription_enabled"`
}

// SetPipecat attaches sidecar config used by realtime ai_call.
func (m *Manager) SetPipecat(cfg *config.PipecatConfig) {
	if m == nil {
		return
	}
	m.pipecat = cfg
}

// PipecatEnabled reports whether realtime ai_call should use the sidecar.
func (m *Manager) PipecatEnabled() bool {
	return m != nil && m.pipecat != nil && m.pipecat.Enabled &&
		strings.TrimSpace(m.pipecat.BaseURL) != "" &&
		strings.TrimSpace(m.pipecat.AuthToken) != ""
}

// SignalPipecatEvent wakes a waiting executeAICallPipecat for the given WhatsApp call id.
func (m *Manager) SignalPipecatEvent(callID string, ev PipecatControlEvent) bool {
	if m == nil || callID == "" {
		return false
	}
	m.mu.RLock()
	session := m.sessions[callID]
	m.mu.RUnlock()
	if session == nil {
		return false
	}
	session.mu.Lock()
	ch := session.PipecatEvent
	session.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- ev:
		return true
	default:
		// Already has a pending event; overwrite by non-blocking drain then send.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- ev:
			return true
		default:
			return false
		}
	}
}

// RunPipecatTool executes an HTTP / set_variable tool on behalf of the sidecar.
func (m *Manager) RunPipecatTool(callID, toolName string, argsJSON string) (string, error) {
	m.mu.RLock()
	session := m.sessions[callID]
	m.mu.RUnlock()
	if session == nil {
		return "", fmt.Errorf("call not found")
	}
	session.mu.Lock()
	tools := session.PipecatTools
	ctx := session.IVRCtx
	team := session.PipecatTransferTeam
	session.mu.Unlock()
	if ctx == nil {
		ctx = &IVRContext{Variables: map[string]string{}}
	}
	var def *aiCallToolDef
	for i := range tools {
		if tools[i].Name == toolName {
			def = &tools[i]
			break
		}
	}
	if def == nil {
		return "", fmt.Errorf("unknown tool %q", toolName)
	}
	result, transfer, hangup := m.runAICallTool(def, argsJSON, session, ctx, team)
	if transfer {
		m.SignalPipecatEvent(callID, PipecatControlEvent{Type: "transfer", TeamID: team})
	}
	if hangup {
		m.SignalPipecatEvent(callID, PipecatControlEvent{Type: "hangup"})
	}
	return result, nil
}

func (m *Manager) executeAICallPipecat(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	welcome, _ := node.Config["welcome_message"].(string)
	systemInstr, _ := node.Config["system_instructions"].(string)
	if systemInstr == "" {
		systemInstr, _ = node.Config["system_prompt"].(string)
	}
	transferTeamID, _ := node.Config["transfer_team_id"].(string)
	tools := parseAICallTools(node.Config["tools"])

	settings := m.loadOrgAISettings(session.OrganizationID)
	apiKey, provider, llmModel, maxTokens := m.resolveAILLMCreds(settings, "")
	if apiKey == "" {
		m.log.Warn("IVR ai_call (pipecat): AI not configured", "call_id", session.ID)
		return "error"
	}
	if systemInstr == "" {
		systemInstr = settings.AI.SystemPrompt
	}
	if systemInstr == "" {
		systemInstr = "You are a helpful phone IVR assistant. Keep replies under 2 short sentences. Use tools when needed."
	}
	systemInstr = interpolateTemplate(systemInstr, ctx.Variables)
	systemInstr = m.composeAICallSystemPrompt(session, ctx, systemInstr, "", node.Config)

	synthOpts := m.resolveSynthOpts(session.OrganizationID, node.Config)
	welcome = interpolateTemplate(welcome, ctx.Variables)

	eventCh := make(chan PipecatControlEvent, 2)
	session.mu.Lock()
	session.PipecatEvent = eventCh
	session.PipecatTools = tools
	session.PipecatTransferTeam = transferTeamID
	session.AICallNodeID = node.ID
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.PipecatEvent = nil
		session.PipecatTools = nil
		session.PipecatTransferTeam = ""
		session.AICallNodeID = ""
		session.mu.Unlock()
	}()

	toolMaps := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolMaps = append(toolMaps, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
			"handler":     t.Handler,
		})
	}

	maxSecs := m.pipecat.MaxSessionSecs
	if maxSecs <= 0 {
		maxSecs = 600
	}
	boot := pipecatBootstrap{
		SessionID:            uuid.New().String(),
		CallID:               session.ID,
		OrganizationID:       session.OrganizationID.String(),
		AccountName:          session.AccountName,
		CallerPhone:          session.CallerPhone,
		CallerName:           session.CallerName,
		SystemPrompt:         systemInstr,
		WelcomeMessage:       welcome,
		TransferTeamID:       transferTeamID,
		Locale:               session.Locale,
		Provider:             provider,
		Model:                llmModel,
		APIKey:               apiKey,
		MaxTokens:            maxTokens,
		VoiceProvider:        "gemini",
		VoiceID:              pipecatGeminiVoice(synthOpts.Voice),
		OpenAIKey:            m.resolveOpenAIKey(session.OrganizationID),
		Tools:                toolMaps,
		CallbackURL:          strings.TrimRight(m.pipecat.CallbackURL, "/"),
		CallbackToken:        m.pipecat.AuthToken,
		MaxSessionSecs:       maxSecs,
		RecordingEnabled:     boolFromConfig(node.Config, "recording_enabled", false),
		RecordUser:           boolFromConfig(node.Config, "record_user", true),
		RecordAssistant:      boolFromConfig(node.Config, "record_assistant", true),
		RecordStereo:         boolFromConfig(node.Config, "record_stereo", true),
		TranscriptionEnabled: boolFromConfig(node.Config, "transcription_enabled", false),
	}
	if boot.OpenAIKey == "" && m.aiConfig != nil {
		boot.OpenAIKey = m.aiConfig.OpenAIKey
	}

	if err := m.pipecatCreateSession(boot); err != nil {
		m.log.Error("IVR ai_call pipecat session create failed", "error", err, "call_id", session.ID)
		return "error"
	}

	bridgeCtx, cancel := context.WithTimeout(context.Background(), time.Duration(maxSecs)*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- m.runPipecatAudioBridge(bridgeCtx, session, player, boot.SessionID)
	}()

	var outcome string
	select {
	case ev := <-eventCh:
		cancel()
		outcome = mapPipecatEvent(ev, transferTeamID, ctx)
	case err := <-errCh:
		// Check before cancel(): cancel() sets bridgeCtx.Err() and would
		// otherwise swallow real bridge failures as "completed".
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			cancel()
			if isPipecatBenignClose(err) {
				// Caller hung up / WS closed cleanly after a live session.
				outcome = "completed"
				break
			}
			m.log.Error("IVR ai_call pipecat bridge failed", "error", err, "call_id", session.ID)
			m.restartCallerAudioConsumer(session)
			return "error"
		}
		cancel()
		// Bridge closed without control event — treat as completed.
		outcome = "completed"
	case <-bridgeCtx.Done():
		outcome = "completed"
	}

	// Restore DTMF consumer so a following transfer node can take the track.
	m.restartCallerAudioConsumer(session)
	return outcome
}

// pipecatGeminiVoice maps node/org voice settings to a GeminiTTSService
// prebuilt voice. Unknown / ElevenLabs IDs fall back to Puck.
func pipecatGeminiVoice(voice string) string {
	v := strings.TrimSpace(voice)
	switch strings.ToLower(v) {
	case "achernar", "achird", "algenib", "algieba", "alnilam", "aoede", "autonoe",
		"callirrhoe", "charon", "despina", "enceladus", "erinome", "fenrir",
		"gacrux", "iapetus", "kore", "laomedeia", "leda", "orus", "puck",
		"pulcherrima", "rasalgethi", "sadachbia", "sadaltager", "schedar",
		"sulafat", "umbriel", "vindemiatrix", "zephyr", "zubenelgenubi":
		// Preserve canonical casing used by Gemini TTS.
		for _, name := range []string{
			"Achernar", "Achird", "Algenib", "Algieba", "Alnilam", "Aoede", "Autonoe",
			"Callirrhoe", "Charon", "Despina", "Enceladus", "Erinome", "Fenrir",
			"Gacrux", "Iapetus", "Kore", "Laomedeia", "Leda", "Orus", "Puck",
			"Pulcherrima", "Rasalgethi", "Sadachbia", "Sadaltager", "Schedar",
			"Sulafat", "Umbriel", "Vindemiatrix", "Zephyr", "Zubenelgenubi",
		} {
			if strings.EqualFold(name, v) {
				return name
			}
		}
	}
	return "Puck"
}

func mapPipecatEvent(ev PipecatControlEvent, defaultTeam string, ctx *IVRContext) string {
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
	case "hangup", "completed":
		return "completed"
	case "error":
		return "error"
	default:
		return "completed"
	}
}

// isPipecatBenignClose reports WS/peer disconnects that should end ai_call as
// completed rather than error (caller hung up mid-session).
func isPipecatBenignClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "close 1000") ||
		strings.Contains(msg, "close 1001") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection")
}

func (m *Manager) pipecatCreateSession(boot pipecatBootstrap) error {
	body, err := json.Marshal(boot)
	if err != nil {
		return err
	}
	url := strings.TrimRight(m.pipecat.BaseURL, "/") + "/v1/sessions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.pipecat.AuthToken)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pipecat session %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
	}
	return nil
}

// warmPipecatWelcome starts Gemini TTS for the first ai_call welcome during
// ICE, so audio is cached before the WebSocket opens.
func (m *Manager) warmPipecatWelcome(session *CallSession) {
	if !m.PipecatEnabled() || session == nil || session.IVRFlow == nil || session.IVRFlow.Menu == nil {
		return
	}
	menuBytes, err := json.Marshal(session.IVRFlow.Menu)
	if err != nil {
		return
	}
	var graph IVRFlowGraph
	if err := json.Unmarshal(menuBytes, &graph); err != nil {
		return
	}
	if graph.Version != 2 || graph.EntryNode == "" {
		return
	}
	graph.BuildMaps()
	node := findFirstAICallNode(&graph)
	if node == nil {
		return
	}
	welcome, _ := node.Config["welcome_message"].(string)
	welcome = strings.TrimSpace(welcome)
	if welcome == "" {
		return
	}
	settings := m.loadOrgAISettings(session.OrganizationID)
	apiKey, _, _, _ := m.resolveAILLMCreds(settings, "")
	if apiKey == "" {
		return
	}
	synthOpts := m.resolveSynthOpts(session.OrganizationID, node.Config)
	payload := map[string]any{
		"welcome_message": welcome,
		"llm_api_key":     apiKey,
		"voice_id":        pipecatGeminiVoice(synthOpts.Voice),
		"voice_provider":  "gemini",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	url := strings.TrimRight(m.pipecat.BaseURL, "/") + "/v1/warm"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.pipecat.AuthToken)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		m.log.Warn("IVR ai_call pipecat warm failed", "error", err, "call_id", session.ID)
		return
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 300 {
		m.log.Warn("IVR ai_call pipecat warm rejected", "status", resp.StatusCode, "call_id", session.ID)
		return
	}
	m.log.Info("IVR ai_call pipecat warm started", "call_id", session.ID)
}

func findFirstAICallNode(graph *IVRFlowGraph) *IVRNode {
	if graph == nil {
		return nil
	}
	id := graph.EntryNode
	seen := map[string]bool{}
	for i := 0; i < 32 && id != "" && !seen[id]; i++ {
		seen[id] = true
		n := graph.Node(id)
		if n == nil {
			return nil
		}
		if n.Type == IVRNodeAICall {
			return n
		}
		// Only follow automatic / default edges; stop at interactive nodes.
		switch n.Type {
		case IVRNodeMenu, IVRNodeGather, IVRNodeSpeechInput, IVRNodeConfirm,
			IVRNodeTransfer, IVRNodeHangup, IVRNodeVoicemail:
			return nil
		}
		next := graph.ResolveEdge(id, "default")
		if next == "" {
			edges := graph.OutgoingEdges(id)
			if len(edges) == 0 {
				return nil
			}
			next = edges[0].To
		}
		id = next
	}
	return nil
}

// runPipecatAudioBridge takes exclusive ownership of the caller Opus track,
// streams Opus payloads to the sidecar, and plays Opus from the sidecar on the
// local WebRTC track. Exits when ctx is cancelled or the WS closes.
//
// WhatsApp often delivers OnTrack 1–2s after IVR starts (after we begin
// sending audio). We open the WS and start downlink immediately so the
// welcome TTS can prime the media path, then wait briefly for the remote
// track before uplink ReadRTP.
func (m *Manager) runPipecatAudioBridge(ctx context.Context, session *CallSession, player *AudioPlayer, sessionID string) error {
	_, player, err := m.resolvePipecatOutbound(session, player)
	if err != nil {
		return err
	}

	// Stop DTMF consumer so we can ReadRTP exclusively once the track arrives.
	safeClose(session.BridgeStarted)

	wsURL := strings.TrimRight(m.pipecat.WSURL, "/") + "/v1/sessions/" + sessionID + "/audio"
	m.log.Info("IVR ai_call pipecat dialing audio WS", "call_id", session.ID, "url", wsURL)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+m.pipecat.AuthToken)
	conn, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		m.restartCallerAudioConsumer(session)
		return fmt.Errorf("pipecat ws dial: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	m.log.Info("IVR ai_call pipecat audio WS connected", "call_id", session.ID)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// Uplink: wait for caller Opus → Pipecat
	wg.Add(1)
	go func() {
		defer wg.Done()
		callerTrack, werr := m.waitCallerRemoteTrack(ctx, session, 12*time.Second)
		if werr != nil {
			// Don't fail the whole session: welcome/downlink may still be useful.
			// Keep waiting until ctx ends so we don't tear down the WS.
			m.log.Warn("IVR ai_call pipecat uplink waiting for track failed",
				"error", werr, "call_id", session.ID)
			<-ctx.Done()
			return
		}
		m.log.Info("IVR ai_call pipecat uplink ready", "call_id", session.ID)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_ = callerTrack.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			pkt, _, rerr := callerTrack.ReadRTP()
			if rerr != nil {
				if ctx.Err() != nil {
					return
				}
				if strings.Contains(rerr.Error(), "timeout") || strings.Contains(rerr.Error(), "deadline") {
					continue
				}
				errCh <- rerr
				return
			}
			if pkt == nil || len(pkt.Payload) == 0 {
				continue
			}
			// Skip telephone-event
			if pkt.PayloadType != 111 && len(pkt.Payload) == 4 {
				continue
			}
			if werr := conn.WriteMessage(websocket.BinaryMessage, pkt.Payload); werr != nil {
				errCh <- werr
				return
			}
		}
	}()

	// Downlink: Pipecat Opus → WebRTC (starts immediately so welcome TTS plays).
	// Do NOT SetReadDeadline on this conn — fasthttp/websocket treats deadline
	// timeouts as permanent read failures and panics on the next ReadMessage.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			msgType, data, rerr := conn.ReadMessage()
			if rerr != nil {
				if ctx.Err() != nil {
					return
				}
				if websocket.IsCloseError(rerr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				errCh <- rerr
				return
			}
			if msgType == websocket.TextMessage {
				var ev struct {
					Type   string `json:"type"`
					Event  string `json:"event"`
					TeamID string `json:"team_id"`
					Reason string `json:"reason"`
					Error  string `json:"error"`
				}
				if json.Unmarshal(data, &ev) == nil {
					name := ev.Event
					if name == "" {
						name = ev.Type
					}
					if name != "" && name != "ready" && name != "ping" && name != "interrupt" {
						m.SignalPipecatEvent(session.ID, PipecatControlEvent{
							Type: name, TeamID: ev.TeamID, Reason: ev.Reason, Error: ev.Error,
						})
					}
				}
				continue
			}
			if msgType != websocket.BinaryMessage || len(data) == 0 {
				continue
			}
			if werr := player.WriteOpusPayload(data); werr != nil {
				m.log.Warn("pipecat playback write failed", "error", werr, "call_id", session.ID)
			}
		}
	}()

	select {
	case <-ctx.Done():
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"))
		wg.Wait()
		return ctx.Err()
	case err := <-errCh:
		_ = conn.Close()
		wg.Wait()
		return err
	}
}

// resolvePipecatOutbound returns the outbound WebRTC track and AudioPlayer.
func (m *Manager) resolvePipecatOutbound(session *CallSession, player *AudioPlayer) (*webrtc.TrackLocalStaticRTP, *AudioPlayer, error) {
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
		return nil, nil, fmt.Errorf("no outbound audio track")
	}
	if player == nil {
		player = NewAudioPlayer(outTrack)
		session.mu.Lock()
		session.IVRPlayer = player
		session.mu.Unlock()
	}
	return outTrack, player, nil
}

// waitCallerRemoteTrack polls until OnTrack stores the caller's Opus track.
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

// WriteOpusPayload sends one Opus frame as an RTP packet on the player's track.
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
