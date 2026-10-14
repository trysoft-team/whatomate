package calling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/realtime"
)

const (
	// While the assistant is talking, caller audio below this RMS is treated as
	// speaker-to-mic echo and not forwarded, or Gemini interrupts itself.
	realtimeBargeRMS = 1400
	// ~100 ms of loud caller speech flushes queued assistant audio at once,
	// before Gemini's own interrupt arrives.
	realtimeBargeFrames      = 5
	realtimePlayingHold      = 120 * time.Millisecond
	realtimeHangupMinWait    = 500 * time.Millisecond
	realtimeExitIdle         = 400 * time.Millisecond
	realtimeHangupMaxWait    = 6 * time.Second
	realtimeTransferAnnounce = 2500 * time.Millisecond
	realtimeTransferMaxWait  = 10 * time.Second
	realtimeSoftClip         = 28000
	realtimeOpusBitrate      = 32000
	realtimeFrame24k         = realtime.LiveOutputRate / 50 // 20 ms
	// A partial frame left this long at the end of a turn is padded and played.
	realtimePartialFrameWait = 60 * time.Millisecond
	realtimeMaxSession       = 10 * time.Minute
)

const (
	defaultTransferToolDesc = "Transfer the caller to a human agent."
	endCallRequiredNote     = "The line stays open until this tool is used."
	defaultEndCallToolDesc  = "REQUIRED to disconnect. Call this when the caller says goodbye " +
		"or the conversation is finished. Speak a brief farewell, then call " +
		"end_call (or call it and speak farewell). " + endCallRequiredNote
)

// geminiLiveEndpoint is a variable so tests can point it at a fake server.
var geminiLiveEndpoint = realtime.LiveEndpoint

// realtimeAICallAvailable reports whether the org's AI key can open Gemini Live.
func realtimeAICallAvailable(provider, apiKey string) bool {
	if apiKey == "" {
		return false
	}
	return provider == string(models.AIProviderGoogle) ||
		strings.HasPrefix(apiKey, "AIza") || strings.HasPrefix(apiKey, "AQ.")
}

// executeAICallRealtime runs the AI Call node as a Gemini Live voice session
// inside the server. handled is false when no Live model could be reached, so
// the caller can fall back to the turn-based loop.
func (m *Manager) executeAICallRealtime(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) (outcome string, handled bool) {
	apiKey, provider, _, _ := m.resolveAILLMCreds(m.loadOrgAISettings(session.OrganizationID), "")
	if !realtimeAICallAvailable(provider, apiKey) {
		return "", false
	}
	// Preparing the prompt consumes the "agents busy" flag; give it back if
	// the turn-based loop ends up running instead.
	busy := ctx.Variables[aiTransferBusyVar]
	defer func() {
		if !handled && busy != "" {
			ctx.Variables[aiTransferBusyVar] = busy
		}
	}()
	p, ok := m.prepareAICallPrompt(session, node, ctx)
	if !ok {
		return "", false
	}
	player, err := m.resolveAICallPlayer(session, player)
	if err != nil {
		m.log.Error("IVR ai_call realtime: no outbound audio", "error", err, "call_id", session.ID)
		return "error", true
	}

	session.mu.Lock()
	session.AICallNodeID = node.ID
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.AICallNodeID = ""
		session.mu.Unlock()
	}()

	// Stop the DTMF consumer so the session can read the caller track exclusively.
	safeClose(session.BridgeStarted)
	defer m.restartCallerAudioConsumer(session)

	cfg := realtime.LiveConfig{
		Voice:             geminiVoice(p.voice),
		SystemInstruction: realtimeSystemInstruction(p.system, p.welcome),
		Tools:             realtimeToolDeclarations(p.tools),
		Transcription:     boolFromConfig(node.Config, "transcription_enabled", false),
	}
	for _, model := range realtime.LiveModels {
		cfg.Model = model
		call := &realtimeCall{
			m: m, session: session, ivr: ctx, player: player, prompt: &p,
			transcribe: cfg.Transcription,
			record:     boolFromConfig(node.Config, "recording_enabled", false),
			recordUser: boolFromConfig(node.Config, "record_user", true),
			recordAsst: boolFromConfig(node.Config, "record_assistant", true),
			recordMix:  boolFromConfig(node.Config, "record_stereo", true),
		}
		ev, err := call.run(cfg, realtimeMaxSession)
		var setupErr *realtime.SetupError
		if errors.As(err, &setupErr) {
			m.log.Warn("Gemini Live setup failed, trying next model", "model", model, "error", err, "call_id", session.ID)
			continue
		}
		if err != nil {
			m.log.Warn("Gemini Live dropped before speaking, trying next model", "model", model, "error", err, "call_id", session.ID)
			continue
		}
		if ev == nil {
			return "completed", true
		}
		return mapAICallExit(*ev, p.transferTeam, ctx), true
	}
	m.log.Warn("Gemini Live unavailable, falling back to turn-based AI Call", "call_id", session.ID)
	return "", false
}

func realtimeSystemInstruction(system, welcome string) string {
	s := system + "\n\nPhone style: keep replies short and natural. " +
		"If the caller speaks while you are talking, stop immediately and listen. " +
		"When the caller is done or says goodbye: say a brief farewell AND call end_call. " +
		"The line does not disconnect until end_call is called."
	if strings.TrimSpace(welcome) != "" {
		s += "\n\nWhen the call starts, greet the caller immediately with this welcome " +
			"(paraphrase naturally, keep it brief):\n" + strings.TrimSpace(welcome)
	}
	return s
}

func realtimeKickoff(welcome string) string {
	kick := strings.TrimSpace(welcome)
	if kick == "" {
		kick = "Greet the caller briefly and ask how you can help."
	}
	return "The phone call is now connected. Speak first now: " + kick
}

// nodeToolDescription returns the operator's description for a built-in tool,
// which is their policy for when the model should use it.
func nodeToolDescription(tools []aiCallToolDef, name string) string {
	for _, t := range tools {
		if strings.TrimSpace(t.Name) == name {
			return strings.TrimSpace(t.Description)
		}
	}
	return ""
}

func realtimeToolDeclarations(tools []aiCallToolDef) []realtime.FunctionDeclaration {
	transferDesc := nodeToolDescription(tools, "transfer_to_agent")
	if transferDesc == "" {
		transferDesc = defaultTransferToolDesc
	}
	endDesc := defaultEndCallToolDesc
	if custom := nodeToolDescription(tools, "end_call"); custom != "" {
		endDesc = custom + " " + endCallRequiredNote
	}
	decls := []realtime.FunctionDeclaration{
		{Name: "transfer_to_agent", Description: transferDesc, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"reason": map[string]any{"type": "string"}},
		}},
		{Name: "end_call", Description: endDesc, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{},
		}},
	}
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" || name == "transfer_to_agent" || name == "end_call" {
			continue
		}
		desc := t.Description
		if desc == "" {
			desc = name
		}
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		decls = append(decls, realtime.FunctionDeclaration{Name: name, Description: desc, Parameters: params})
	}
	return decls
}

// transferToolResponse asks the model to announce the handover when it has not
// started speaking, since the transfer waits for queued speech to finish.
func transferToolResponse(alreadySpeaking bool) map[string]any {
	if alreadySpeaking {
		return map[string]any{"result": "transferring"}
	}
	return map[string]any{
		"result": "transferring",
		"instruction": "In one short sentence, tell the caller you are transferring them to an " +
			"agent now. Do not call any more tools.",
	}
}

// realtimeCall is one Gemini Live session bridged to the caller's WebRTC audio.
type realtimeCall struct {
	m       *Manager
	session *CallSession
	ivr     *IVRContext
	player  *AudioPlayer
	prompt  *aiCallPrompt
	live    *realtime.LiveSession

	transcribe                                bool
	record, recordUser, recordAsst, recordMix bool
	timeline                                  *realtime.Timeline

	mu            sync.Mutex
	pending       []int16 // 24 kHz assistant audio waiting to be played
	pendingAt     time.Time
	playingUntil  time.Time
	exitRequested bool
	tx            realtime.TranscriptAssembler
	transcript    []realtime.TranscriptLine
	firstAudio    time.Duration
	engaged       bool // the model spoke or called a tool, so a retry could repeat it
	interrupts    int
	recvErr       error

	done     chan struct{}
	stopOnce sync.Once
	result   *aiCallExit
}

// run returns the control event that ended the session (nil = completed).
// A *realtime.SetupError, or an error when Live closed before any audio,
// means another model may be tried.
func (c *realtimeCall) run(cfg realtime.LiveConfig, maxDur time.Duration) (*aiCallExit, error) {
	m, session := c.m, c.session
	start := time.Now()
	dialCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	live, err := realtime.DialLive(dialCtx, geminiLiveEndpoint, c.prompt.apiKey, cfg)
	cancel()
	if err != nil {
		return nil, err
	}
	c.live = live
	c.done = make(chan struct{})
	c.firstAudio = -1
	if c.record {
		c.timeline = realtime.NewTimeline()
	}
	m.log.Info("Gemini Live connected", "model", cfg.Model, "voice", cfg.Voice,
		"setup_ms", time.Since(start).Milliseconds(), "call_id", session.ID)

	if err := live.SendText(realtimeKickoff(c.prompt.welcome)); err != nil {
		_ = live.Close()
		return nil, err
	}

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); c.uplink() }()
	go func() { defer wg.Done(); c.receive(start) }()
	go func() { defer wg.Done(); c.playback() }()
	go func() { defer wg.Done(); c.watch(maxDur) }()

	<-c.done
	_ = live.Close()
	wg.Wait()

	c.mu.Lock()
	c.transcript = append(c.transcript, c.tx.FlushAll()...)
	lines, recvErr, firstAudio, engaged := c.transcript, c.recvErr, c.firstAudio, c.engaged
	c.mu.Unlock()
	m.log.Info("Gemini Live session done", "model", cfg.Model, "interrupts", c.interrupts,
		"transcript_lines", len(lines), "first_audio_ms", firstAudio.Milliseconds(), "call_id", session.ID)

	if c.result == nil && !engaged && recvErr != nil {
		return nil, recvErr
	}
	go c.persist(lines)
	return c.result, nil
}

func (c *realtimeCall) stop(ev *aiCallExit) {
	c.stopOnce.Do(func() {
		c.result = ev
		close(c.done)
	})
}

func (c *realtimeCall) stopped() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// watch ends the session at the time limit or when the call itself ends.
func (c *realtimeCall) watch(maxDur time.Duration) {
	limit := time.NewTimer(maxDur)
	defer limit.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-limit.C:
			c.m.log.Info("Gemini Live session reached its time limit", "call_id", c.session.ID)
			c.stop(nil)
			return
		case <-tick.C:
			c.m.mu.RLock()
			current := c.m.sessions[c.session.ID]
			c.m.mu.RUnlock()
			if current != c.session {
				c.stop(nil)
				return
			}
		}
	}
}

// uplink decodes caller Opus, drops echo while the assistant talks, and
// streams 16 kHz PCM to Gemini.
func (c *realtimeCall) uplink() {
	m, session := c.m, c.session
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-c.done; cancel() }()
	track, err := m.waitCallerRemoteTrack(ctx, session, 12*time.Second)
	if err != nil {
		if !c.stopped() {
			m.log.Warn("Gemini Live: no caller audio track", "error", err, "call_id", session.ID)
		}
		return
	}
	dec, err := realtime.NewOpusDecoder()
	if err != nil {
		m.log.Error("Gemini Live: opus decoder", "error", err, "call_id", session.ID)
		c.stop(&aiCallExit{Type: "error"})
		return
	}
	down := realtime.NewDecimator(realtime.OpusRate / realtime.LiveInputRate)
	audioPT := uint8(track.PayloadType())
	bargeRun := 0
	for !c.stopped() {
		_ = track.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if isReadTimeout(err) {
				continue
			}
			c.stop(nil) // caller hung up
			return
		}
		if pkt == nil || len(pkt.Payload) == 0 || pkt.PayloadType != audioPT {
			continue
		}
		pcm48, err := dec.Decode(pkt.Payload)
		if err != nil {
			continue
		}
		pcm16 := down.Process(pcm48)
		if c.timeline != nil {
			c.timeline.WriteUser(pcm16)
		}
		playing, exiting := c.playbackState()
		if playing {
			rms := realtime.RMS(pcm16)
			if rms < realtimeBargeRMS {
				bargeRun = 0
				continue
			}
			// Let farewell / transfer notices finish even if the caller talks.
			if !exiting {
				bargeRun++
				if bargeRun >= realtimeBargeFrames {
					c.flushPlayback()
					bargeRun = 0
					m.log.Info("Gemini Live local barge-in", "rms", rms, "call_id", session.ID)
				}
			}
		} else {
			bargeRun = 0
		}
		if err := c.live.SendAudio(pcm16); err != nil {
			c.stop(nil)
			return
		}
	}
}

func isReadTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline")
}

// receive handles Gemini messages: audio, transcripts, interrupts and tools.
func (c *realtimeCall) receive(start time.Time) {
	m, session := c.m, c.session
	for {
		msg, err := c.live.Recv()
		if err != nil {
			if c.stopped() {
				return
			}
			c.mu.Lock()
			c.recvErr = err
			exiting := c.exitRequested
			c.mu.Unlock()
			m.log.Warn("Gemini Live closed", "error", err, "call_id", session.ID)
			// A pending hangup / transfer still drains queued speech and ends
			// the session with its own event.
			if !exiting {
				c.stop(nil)
			}
			return
		}
		if msg.ToolCall != nil {
			c.mu.Lock()
			c.engaged = true
			c.mu.Unlock()
			if c.transcribe {
				c.addTranscript(c.flushTranscript())
			}
			c.handleToolCall(msg.ToolCall)
			continue
		}
		sc := msg.ServerContent
		if sc == nil {
			if msg.GoAway != nil {
				m.log.Info("Gemini Live go-away", "detail", string(*msg.GoAway), "call_id", session.ID)
			}
			continue
		}
		if sc.Interrupted {
			c.mu.Lock()
			c.interrupts++
			exiting := c.exitRequested
			c.mu.Unlock()
			if c.transcribe {
				c.addTranscript(c.flushTranscript())
			}
			if !exiting {
				c.flushPlayback()
			}
			continue
		}
		if c.transcribe {
			c.handleTranscription(sc)
		}
		if sc.ModelTurn == nil {
			continue
		}
		for _, part := range sc.ModelTurn.Parts {
			if part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}
			c.mu.Lock()
			c.engaged = true
			if c.firstAudio < 0 {
				c.firstAudio = time.Since(start)
				m.log.Info("Gemini Live first audio", "ms", c.firstAudio.Milliseconds(), "call_id", session.ID)
			}
			c.pending = append(c.pending, realtime.BytesToPCM16(part.InlineData.Data)...)
			c.pendingAt = time.Now()
			c.mu.Unlock()
		}
	}
}

func (c *realtimeCall) handleTranscription(sc *realtime.ServerContent) {
	var lines []realtime.TranscriptLine
	c.mu.Lock()
	if in := sc.InputTranscription; in != nil && in.Text != "" {
		lines = append(lines, c.tx.FeedUser(in.Text, in.Finished)...)
	}
	if out := sc.OutputTranscription; out != nil && out.Text != "" {
		lines = append(lines, c.tx.FeedAssistant(out.Text, out.Finished)...)
	}
	if sc.TurnComplete || sc.GenerationComplete {
		lines = append(lines, c.tx.FlushAll()...)
	}
	c.mu.Unlock()
	c.addTranscript(lines)
	for _, l := range lines {
		// The model often says goodbye without calling end_call.
		if l.Role == "assistant" && realtime.FarewellRE.MatchString(l.Text) {
			c.requestHangup("farewell_transcript")
		}
	}
}

func (c *realtimeCall) flushTranscript() []realtime.TranscriptLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tx.FlushAll()
}

func (c *realtimeCall) addTranscript(lines []realtime.TranscriptLine) {
	if len(lines) == 0 {
		return
	}
	c.mu.Lock()
	c.transcript = append(c.transcript, lines...)
	c.mu.Unlock()
	for _, l := range lines {
		c.m.log.Info("Gemini Live transcript", "role", l.Role, "text", truncateStr(l.Text, 160), "call_id", c.session.ID)
	}
}

func (c *realtimeCall) handleToolCall(tc *realtime.ToolCall) {
	m, session := c.m, c.session
	var responses []realtime.FunctionResponse
	for _, fc := range tc.FunctionCalls {
		id := fc.ID
		if id == "" {
			id = fc.Name
		}
		var resp map[string]any
		switch fc.Name {
		case "transfer_to_agent", "transfer":
			reason, _ := fc.Args["reason"].(string)
			m.log.Info("Gemini Live tool transfer_to_agent", "reason", truncateStr(reason, 120), "call_id", session.ID)
			resp = c.requestTransfer(reason)
		case "end_call", "hangup":
			m.log.Info("Gemini Live tool end_call", "call_id", session.ID)
			c.requestHangup("end_call")
			resp = map[string]any{"result": "ending"}
		default:
			args := fc.Args
			if args == nil {
				args = map[string]any{}
			}
			argsJSON, _ := json.Marshal(args)
			result, transfer, hangup := m.runAICallTool(findToolDef(c.prompt.tools, fc.Name), string(argsJSON),
				session, c.ivr, c.prompt.transferTeam)
			switch {
			case transfer:
				resp = c.requestTransfer("")
			case hangup:
				c.requestHangup(fc.Name)
				resp = map[string]any{"result": "ending"}
			default:
				resp = map[string]any{"result": result}
			}
		}
		responses = append(responses, realtime.FunctionResponse{ID: id, Name: fc.Name, Response: resp})
	}
	if len(responses) > 0 {
		if err := c.live.SendToolResponses(responses); err != nil {
			m.log.Warn("Gemini Live tool response failed", "error", err, "call_id", session.ID)
		}
	}
}

func (c *realtimeCall) requestTransfer(reason string) map[string]any {
	speaking := !c.playbackIdle()
	minWait := realtimeHangupMinWait
	if !speaking {
		minWait = realtimeTransferAnnounce
	}
	team := c.prompt.transferTeam
	if c.ivr != nil && c.ivr.Variables["prefer_team_id"] != "" {
		team = c.ivr.Variables["prefer_team_id"]
	}
	c.requestExit(aiCallExit{Type: "transfer", TeamID: team, Reason: reason}, minWait, realtimeTransferMaxWait)
	return transferToolResponse(speaking)
}

func (c *realtimeCall) requestHangup(reason string) {
	if c.requestExit(aiCallExit{Type: "hangup"}, realtimeHangupMinWait, realtimeHangupMaxWait) {
		c.m.log.Info("Gemini Live hangup requested", "reason", reason, "call_id", c.session.ID)
	}
}

// requestExit lets queued speech (farewell, transfer notice) finish before
// ending the session with ev. Only the first request counts.
func (c *realtimeCall) requestExit(ev aiCallExit, minWait, maxWait time.Duration) bool {
	c.mu.Lock()
	if c.exitRequested {
		c.mu.Unlock()
		return false
	}
	c.exitRequested = true
	c.mu.Unlock()
	go c.drainThenStop(ev, minWait, maxWait)
	return true
}

func (c *realtimeCall) drainThenStop(ev aiCallExit, minWait, maxWait time.Duration) {
	started := time.Now()
	var idleSince time.Time
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
wait:
	for time.Since(started) < maxWait {
		select {
		case <-c.done:
			return
		case <-tick.C:
		}
		if !c.playbackIdle() {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
		} else if time.Since(started) >= minWait && time.Since(idleSince) >= realtimeExitIdle {
			break wait
		}
	}
	select {
	case <-c.done:
		return
	case <-time.After(200 * time.Millisecond):
	}
	c.m.log.Info("Gemini Live ending session", "event", ev.Type,
		"after_ms", time.Since(started).Milliseconds(), "call_id", c.session.ID)
	c.stop(&ev)
}

// playbackState reports whether assistant audio is on the wire and whether an
// exit (hangup / transfer) is draining it.
func (c *realtimeCall) playbackState() (playing, exiting bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) > 0 || time.Now().Before(c.playingUntil), c.exitRequested
}

func (c *realtimeCall) playbackIdle() bool {
	playing, _ := c.playbackState()
	return !playing
}

func (c *realtimeCall) flushPlayback() {
	c.mu.Lock()
	c.pending = nil
	c.playingUntil = time.Time{}
	c.mu.Unlock()
}

// nextFrame pops 20 ms of 24 kHz audio, padding a stale partial frame.
func (c *realtimeCall) nextFrame() []int16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return nil
	}
	if len(c.pending) < realtimeFrame24k {
		if time.Since(c.pendingAt) < realtimePartialFrameWait {
			return nil
		}
		c.pending = append(c.pending, make([]int16, realtimeFrame24k-len(c.pending))...)
	}
	frame := append([]int16(nil), c.pending[:realtimeFrame24k]...)
	c.pending = c.pending[realtimeFrame24k:]
	return frame
}

// playback encodes queued assistant audio to Opus at real-time pace.
func (c *realtimeCall) playback() {
	m, session := c.m, c.session
	enc, err := realtime.NewOpusEncoder(realtimeOpusBitrate)
	if err != nil {
		m.log.Error("Gemini Live: opus encoder", "error", err, "call_id", session.ID)
		c.stop(&aiCallExit{Type: "error"})
		return
	}
	up := &realtime.Upsampler2x{}
	rec := realtime.NewDecimator(realtime.OpusRate / realtime.RecordingRate)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	writeErrs := 0
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
		}
		frame := c.nextFrame()
		if frame == nil {
			continue
		}
		pcm48 := up.Process(frame)
		if c.timeline != nil {
			c.timeline.WriteAssistant(rec.Process(pcm48))
		}
		realtime.SoftLimit(pcm48, realtimeSoftClip)
		pkt, err := enc.Encode(pcm48)
		if err != nil {
			continue
		}
		if err := c.player.WriteOpusPayload(pkt); err != nil {
			if writeErrs++; writeErrs == 1 {
				m.log.Warn("Gemini Live playback write failed", "error", err, "call_id", session.ID)
			}
		}
		c.mu.Lock()
		c.playingUntil = time.Now().Add(realtimePlayingHold)
		c.mu.Unlock()
	}
}

// persist stores the transcript and recordings on the call log.
func (c *realtimeCall) persist(lines []realtime.TranscriptLine) {
	m, callID := c.m, c.session.ID
	if c.transcribe && len(lines) > 0 {
		out := make([]map[string]string, len(lines))
		for i, l := range lines {
			out[i] = map[string]string{"role": l.Role, "text": l.Text}
		}
		if err := m.SaveAICallTranscript(callID, out); err != nil {
			m.log.Warn("AI Call transcript save failed", "error", err, "call_id", callID)
		}
	}
	if c.timeline == nil {
		return
	}
	user, asst := c.timeline.Finalize()
	if len(user) == 0 {
		return
	}
	uploads := []struct {
		kind string
		on   bool
		wav  func() []byte
	}{
		{"user", c.recordUser, func() []byte { return realtime.WAV(user, realtime.RecordingRate, 1) }},
		{"assistant", c.recordAsst, func() []byte { return realtime.WAV(asst, realtime.RecordingRate, 1) }},
		{"stereo", c.recordMix, func() []byte { return realtime.WAV(realtime.Interleave(user, asst), realtime.RecordingRate, 2) }},
	}
	for _, u := range uploads {
		if !u.on {
			continue
		}
		if _, err := m.SaveAICallRecording(callID, u.kind, bytes.NewReader(u.wav()), "audio/wav"); err != nil {
			m.log.Warn("AI Call recording save failed", "kind", u.kind, "error", err, "call_id", callID)
		}
	}
}
