package calling

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
)

// runIVRFlow parses the IVR flow graph and executes the node loop.
func (m *Manager) runIVRFlow(session *CallSession, waAccount *whatsapp.Account) {
	if session.IVRFlow == nil || session.IVRFlow.Menu == nil {
		m.log.Info("No IVR flow or menu configured", "call_id", session.ID)
		return
	}

	// Parse the menu JSONB into the v2 flow graph
	menuBytes, err := json.Marshal(session.IVRFlow.Menu)
	if err != nil {
		m.log.Error("Failed to marshal IVR menu", "error", err, "call_id", session.ID)
		return
	}

	var graph IVRFlowGraph
	if err := json.Unmarshal(menuBytes, &graph); err != nil {
		m.log.Error("Failed to parse IVR flow graph", "error", err, "call_id", session.ID)
		return
	}

	if graph.Version != 2 || graph.EntryNode == "" {
		m.log.Error("Invalid IVR flow graph version or missing entry_node", "call_id", session.ID, "version", graph.Version)
		return
	}

	graph.BuildMaps()

	// Initialize IVR context — load existing path for goto_flow continuity
	ivrCtx := &IVRContext{
		Variables: map[string]string{
			"caller_phone": session.CallerPhone,
			"caller_name":  session.CallerName,
			"call_id":      session.ID,
		},
		CallerPhone:    session.CallerPhone,
		CallID:         session.ID,
		OrganizationID: session.OrganizationID,
		CurrentNode:    graph.EntryNode,
	}

	// Load existing IVR path from call log (for goto_flow accumulation)
	var existingLog models.CallLog
	if err := m.db.Select("ivr_path").Where("id = ?", session.CallLogID).First(&existingLog).Error; err == nil {
		if existingLog.IVRPath != nil {
			if steps, ok := existingLog.IVRPath["steps"].([]any); ok {
				for _, s := range steps {
					if stepMap, ok := s.(map[string]any); ok {
						entry := map[string]string{}
						for k, v := range stepMap {
							if str, ok := v.(string); ok {
								entry[k] = str
							}
						}
						ivrCtx.Path = append(ivrCtx.Path, entry)
					}
				}
			}
		}
	}

	// Record flow start if this is the first entry
	if len(ivrCtx.Path) == 0 {
		ivrCtx.Path = append(ivrCtx.Path, map[string]string{"action": "flow_start", "flow": session.IVRFlow.Name})
	}

	// Store graph + context on the session
	session.mu.Lock()
	session.IVRGraph = &graph
	session.IVRCtx = ivrCtx
	session.mu.Unlock()

	// For outgoing calls, play IVR audio on the WA track (contact hears it)
	// and start DTMF detection on the WA remote track.
	if session.Direction == models.CallDirectionOutgoing {
		session.mu.Lock()
		waRemote := session.WARemoteTrack
		if session.DTMFBuffer == nil {
			session.DTMFBuffer = make(chan byte, 32)
		}
		session.BridgeStarted = make(chan struct{})
		session.mu.Unlock()
		if waRemote != nil {
			go m.consumeAudioWithDTMF(session, waRemote)
		}
	}

	// Reuse the session's IVR player to maintain RTP sequence continuity
	session.mu.Lock()
	if session.IVRPlayer == nil {
		var ivrTrack *webrtc.TrackLocalStaticRTP
		if session.Direction == models.CallDirectionOutgoing {
			ivrTrack = session.WAAudioTrack
		} else {
			ivrTrack = session.AudioTrack
		}
		player := NewAudioPlayer(ivrTrack)
		// For outgoing post-call IVR, the bridge was forwarding agent audio
		// to WAAudioTrack with high RTP seq numbers. Seed the player so its
		// packets aren't dropped as "old" by the WA endpoint.
		if session.LastRTPSeq > 0 {
			player.SetSequence(session.LastRTPSeq, session.LastRTPTimestamp)
		}
		session.IVRPlayer = player
	}
	player := session.IVRPlayer
	session.mu.Unlock()

	// Brief delay to let the media path stabilize after bridge teardown
	// (same fix as incoming calls in webrtc.go).
	if session.Direction == models.CallDirectionOutgoing {
		time.Sleep(500 * time.Millisecond)
	}

	m.executeNodeLoop(session, waAccount, &graph, ivrCtx, player)
}

// executeNodeLoop dispatches to type-specific executors in a loop.
func (m *Manager) executeNodeLoop(session *CallSession, waAccount *whatsapp.Account, graph *IVRFlowGraph, ctx *IVRContext, player *AudioPlayer) {
	for {
		// Check session is still active
		session.mu.Lock()
		status := session.Status
		session.mu.Unlock()
		if status != models.CallStatusAnswered {
			break
		}

		node := graph.Node(ctx.CurrentNode)
		if node == nil {
			m.log.Error("IVR node not found", "call_id", session.ID, "node_id", ctx.CurrentNode)
			break
		}

		m.log.Info("Executing IVR node", "call_id", session.ID, "node_id", node.ID, "type", node.Type, "label", node.Label)

		var outcome string

		switch node.Type {
		case IVRNodeStart:
			// Entry sentinel: optional default variables, then follow default edge.
			n := applyIVRStartDefaults(node.Config, ctx)
			if n > 0 {
				m.log.Info("IVR start defaults applied", "call_id", session.ID, "node", node.ID, "count", n)
			}
			outcome = "default"
		case IVRNodeGreeting:
			outcome = m.executeGreeting(session, node, ctx, player)
		case IVRNodeMenu:
			outcome = m.executeMenu(session, node, ctx, player)
		case IVRNodeGather:
			outcome = m.executeGather(session, node, ctx, player)
		case IVRNodeHTTPCallback:
			outcome = m.executeHTTPCallback(session, node, ctx)
		case IVRNodeTransfer:
			ctx.Path = append(ctx.Path, map[string]string{
				"node": node.ID, "type": string(node.Type), "label": node.Label,
			})
			outcome = m.executeTransfer(session, node, ctx, graph, player)
			if outcome == "" {
				return // terminal — no next node
			}
			// Transfer created a fresh IVRPlayer — pick it up so subsequent
			// nodes use the correct RTP sequence.
			session.mu.Lock()
			player = session.IVRPlayer
			session.mu.Unlock()
		case IVRNodeGotoFlow:
			ctx.Path = append(ctx.Path, map[string]string{
				"node": node.ID, "type": string(node.Type), "label": node.Label,
			})
			m.executeGotoFlow(session, node, ctx, waAccount)
			return // terminal (recursive call to runIVRFlow)
		case IVRNodeTiming:
			outcome = m.executeTiming(session, node)
		case IVRNodeCondition:
			outcome = m.executeCondition(session, node, ctx)
		case IVRNodeSetVariable:
			outcome = m.executeSetVariable(session, node, ctx)
		case IVRNodeConfirm:
			outcome = m.executeConfirm(session, node, ctx, player)
		case IVRNodeDelay:
			outcome = m.executeDelay(session, node, ctx, player)
		case IVRNodeSetLabels:
			outcome = m.executeSetLabels(session, node, ctx)
		case IVRNodeSendMessage:
			outcome = m.executeSendMessage(session, node, ctx)
		case IVRNodeWebhook:
			outcome = m.executeWebhook(session, node, ctx)
		case IVRNodeLanguage:
			outcome = m.executeLanguage(session, node, ctx)
		case IVRNodeVoicemail:
			outcome = m.executeVoicemail(session, node, ctx, player)
		case IVRNodeSpeechInput:
			outcome = m.executeSpeechInput(session, node, ctx, player)
		case IVRNodeAIVoice:
			outcome = m.executeAIVoice(session, node, ctx, player)
		case IVRNodeAICall:
			outcome = m.executeAICall(session, node, ctx, player)
		case IVRNodeExecuteFunction:
			outcome = m.executeFunction(session, node, ctx)
		case IVRNodeDialOut:
			outcome = m.executeDialOut(session, node, ctx, player, waAccount)
		case IVRNodeHangup:
			ctx.Path = append(ctx.Path, map[string]string{
				"node": node.ID, "type": string(node.Type), "label": node.Label,
			})
			m.executeHangup(session, node, ctx, waAccount, player)
			return // terminal
		default:
			m.log.Error("Unknown IVR node type", "call_id", session.ID, "type", node.Type)
			return
		}

		// Record this step after execution so we can include the outcome.
		step := map[string]string{
			"node":  node.ID,
			"type":  string(node.Type),
			"label": node.Label,
		}

		// For menu nodes, record the selected digit and option label
		if node.Type == IVRNodeMenu && strings.HasPrefix(outcome, "digit:") {
			digit := strings.TrimPrefix(outcome, "digit:")
			step["digit"] = digit
			if opts, ok := node.Config["options"].(map[string]any); ok {
				if optMap, ok := opts[digit].(map[string]any); ok {
					if optLabel, ok := optMap["label"].(string); ok {
						step["option_label"] = optLabel
					}
				}
			}
		}
		if outcome != "" {
			step["outcome"] = outcome
		}

		ctx.Path = append(ctx.Path, step)

		// Resolve the next node via edges
		nextID := graph.ResolveEdge(node.ID, outcome)
		if nextID == "" {
			m.log.Info("No matching edge, ending IVR flow", "call_id", session.ID, "node", node.ID, "outcome", outcome)
			break
		}

		ctx.CurrentNode = nextID
	}

	// Save the IVR path on exit
	m.saveIVRPath(session, ctx.Path)
}

// --- Node Executors ---

// resolvePromptAudio returns the audio filename to play for a node.
// If greeting_text is set, it is interpolated with IVR variables (including
// {{caller_name}}) and rendered through TTS when available.
func (m *Manager) resolvePromptAudio(node *IVRNode, ctx *IVRContext) string {
	audioFile, _ := node.Config["audio_file"].(string)
	greetingText, _ := node.Config["greeting_text"].(string)
	return m.resolvePromptAudioText(node.ID, audioFile, greetingText, ctx)
}

// resolvePromptAudioText renders TTS for explicit text, falling back to audioFile.
func (m *Manager) resolvePromptAudioText(nodeID, audioFile, greetingText string, ctx *IVRContext) string {
	if greetingText == "" || m.tts == nil {
		return audioFile
	}
	vars := map[string]string{}
	if ctx != nil && ctx.Variables != nil {
		vars = ctx.Variables
	}
	rendered := interpolateTemplate(greetingText, vars)
	orgID := uuid.Nil
	if ctx != nil {
		orgID = ctx.OrganizationID
	}
	opts := m.resolveSynthOpts(orgID, nil)
	filename, err := m.tts.Generate(context.Background(), rendered, opts)
	if err != nil {
		m.log.Error("IVR TTS generation failed", "error", err, "node", nodeID)
		return audioFile
	}
	return filename
}

// executeGreeting plays audio or TTS, returns "default".
func (m *Manager) executeGreeting(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	audioFile := m.resolvePromptAudio(node, ctx)
	interruptible, _ := node.Config["interruptible"].(bool)

	if audioFile != "" && m.config.AudioDir != "" {
		fullPath := filepath.Join(m.config.AudioDir, audioFile)
		m.drainDTMF(session)

		if interruptible {
			m.playInterruptible(session, player, fullPath)
		} else {
			packets, err := player.PlayFile(fullPath)
			if err != nil {
				m.log.Error("Failed to play greeting audio", "error", err, "call_id", session.ID)
			} else {
				m.log.Info("Greeting playback finished", "call_id", session.ID, "packets", packets)
			}
		}
	}

	return "default"
}

// executeMenu plays a prompt, waits for single DTMF, validates against
// configured options, and retries on timeout or invalid digit.
// Returns "digit:N" on valid input, "timeout" on single-attempt timeout,
// or "max_retries" when all attempts are exhausted.
func (m *Manager) executeMenu(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	audioFile := m.resolvePromptAudio(node, ctx)
	timeoutSecs := getConfigInt(node.Config, "timeout_seconds", 10)
	maxRetries := getConfigInt(node.Config, "max_retries", 3)
	timeout := time.Duration(timeoutSecs) * time.Second

	// Build set of valid digits from menu options
	validDigits := make(map[string]bool)
	if opts, ok := node.Config["options"].(map[string]any); ok {
		for digit := range opts {
			validDigits[digit] = true
		}
	}

	var fullPath string
	if audioFile != "" && m.config.AudioDir != "" {
		fullPath = filepath.Join(m.config.AudioDir, audioFile)
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		m.drainDTMF(session)

		var digit byte
		var gotDigit bool

		if fullPath != "" {
			// Play audio prompt (interruptible by DTMF)
			playDone := make(chan struct{})
			go func() {
				if _, err := player.PlayFile(fullPath); err != nil {
					m.log.Error("Failed to play menu audio", "error", err, "call_id", session.ID)
				}
				close(playDone)
			}()

			select {
			case <-playDone:
				// Audio finished playing, wait for digit input
				digit, gotDigit = m.waitForDTMF(session, timeout, 1)
			case d, chOk := <-session.DTMFBuffer:
				// Caller interrupted audio with a digit
				player.Stop()
				<-playDone
				player.ResetAfterInterrupt()
				if chOk {
					digit = d
					gotDigit = true
				}
			}
		} else {
			digit, gotDigit = m.waitForDTMF(session, timeout, 1)
		}

		if !gotDigit {
			m.log.Debug("Menu timeout", "call_id", session.ID, "attempt", attempt+1)
			continue
		}

		digitStr := string(digit)
		if len(validDigits) == 0 || validDigits[digitStr] {
			// Store the selected digit in context for use by subsequent nodes
			ctx.Variables["menu_"+node.ID] = digitStr
			ctx.Variables["last_menu_digit"] = digitStr
			return fmt.Sprintf("digit:%s", digitStr)
		}

		// Invalid digit — log and retry with prompt replay
		m.log.Debug("Menu invalid digit", "call_id", session.ID, "digit", digitStr, "attempt", attempt+1)
	}

	return "max_retries"
}

// executeGather collects multi-digit input, stores in context.
func (m *Manager) executeGather(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	audioFile := m.resolvePromptAudio(node, ctx)
	maxDigits := getConfigInt(node.Config, "max_digits", 10)
	terminator, _ := node.Config["terminator"].(string)
	if terminator == "" {
		terminator = "#"
	}
	timeoutSecs := getConfigInt(node.Config, "timeout_seconds", 10)
	maxRetries := getConfigInt(node.Config, "max_retries", 3)
	storeAs, _ := node.Config["store_as"].(string)

	m.drainDTMF(session)

	// Play prompt (non-interruptible for gather — we need all digits)
	if audioFile != "" && m.config.AudioDir != "" {
		fullPath := filepath.Join(m.config.AudioDir, audioFile)
		if _, err := player.PlayFile(fullPath); err != nil {
			m.log.Error("Failed to play gather audio", "error", err, "call_id", session.ID)
		}
	}

	// Collect digits
	for attempt := 0; attempt < maxRetries; attempt++ {
		collected := m.collectDTMFDigits(session, maxDigits, terminator, time.Duration(timeoutSecs)*time.Second)
		if collected != "" {
			if storeAs != "" {
				ctx.Variables[storeAs] = collected
			}
			m.log.Info("Gather collected", "call_id", session.ID, "store_as", storeAs, "value", collected)
			return "default"
		}
		m.log.Debug("Gather timeout", "call_id", session.ID, "attempt", attempt+1)
	}

	return "max_retries"
}

// collectDTMFDigits collects multiple digits until maxDigits, terminator, or timeout.
func (m *Manager) collectDTMFDigits(session *CallSession, maxDigits int, terminator string, timeout time.Duration) string {
	var digits []byte
	deadline := time.After(timeout)

	for len(digits) < maxDigits {
		select {
		case d, ok := <-session.DTMFBuffer:
			if !ok {
				return string(digits)
			}
			if string(d) == terminator {
				return string(digits)
			}
			digits = append(digits, d)
		case <-deadline:
			return string(digits)
		}
	}

	return string(digits)
}

// executeHTTPCallback makes an HTTP request and branches on response status.
func (m *Manager) executeHTTPCallback(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	url, _ := node.Config["url"].(string)
	method, _ := node.Config["method"].(string)
	if method == "" {
		method = "GET"
	}
	bodyTemplate, _ := node.Config["body_template"].(string)
	timeoutSecs := getConfigInt(node.Config, "timeout_seconds", 10)
	responseStoreAs, _ := node.Config["response_store_as"].(string)

	// Build headers map
	headersRaw, _ := node.Config["headers"].(map[string]any)
	headers := make(map[string]string, len(headersRaw))
	for k, v := range headersRaw {
		if s, ok := v.(string); ok {
			headers[k] = interpolateTemplate(s, ctx.Variables)
		}
	}

	// Interpolate URL and body
	url = interpolateTemplate(url, ctx.Variables)
	body := interpolateTemplate(bodyTemplate, ctx.Variables)

	result, err := executeHTTPCallback(url, method, headers, body, time.Duration(timeoutSecs)*time.Second)
	if err != nil {
		m.log.Error("HTTP callback failed", "error", err, "call_id", session.ID, "url", url)
		return "http:non2xx"
	}

	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	if responseStoreAs != "" {
		ctx.Variables[responseStoreAs] = result.Body
	}
	ctx.Variables["last_http_body"] = result.Body
	ctx.Variables["last_http_status"] = fmt.Sprintf("%d", result.StatusCode)

	m.log.Info("HTTP callback completed", "call_id", session.ID, "url", url, "status", result.StatusCode)

	if result.StatusCode >= 200 && result.StatusCode < 300 {
		return "http:2xx"
	}
	return "http:non2xx"
}

// executeTransfer routes the call to an agent team. If the transfer node has
// outgoing edges in the graph it blocks until the transfer completes and
// returns the outcome ("completed", "no_answer", "abandoned") so the IVR loop
// can continue. When there are no outgoing edges it behaves as before (terminal,
// returns "").
//
// Config extras (Wave 2): agent_id, announce_queue, queue_announce_text, overflow_team_id.
func (m *Manager) executeTransfer(session *CallSession, node *IVRNode, ctx *IVRContext, graph *IVRFlowGraph, player *AudioPlayer) string {
	teamID, overflowTeamID := m.prepareTransferFromNode(session, node, ctx, player)
	m.saveIVRPath(session, ctx.Path)

	session.mu.Lock()
	session.TransferCallbacks = parseTransferCallbacks(node.Config)
	session.mu.Unlock()

	edges := graph.OutgoingEdges(node.ID)
	if len(edges) == 0 {
		m.initiateTransfer(session, session.AccountName, teamID, ctx.Path)
		return "" // terminal
	}

	transferDone := make(chan string, 1)
	session.mu.Lock()
	session.TransferDone = transferDone
	session.mu.Unlock()

	m.initiateTransfer(session, session.AccountName, teamID, ctx.Path)

	outcome, ok := <-transferDone
	if !ok || outcome == "" {
		outcome = "completed"
	}

	// Overflow: on no_answer, try overflow team once before returning.
	if outcome == "no_answer" && overflowTeamID != "" && overflowTeamID != teamID {
		m.log.Info("IVR transfer overflow to secondary team",
			"call_id", session.ID, "overflow_team", overflowTeamID)
		transferDone2 := make(chan string, 1)
		session.mu.Lock()
		session.TransferDone = transferDone2
		session.mu.Unlock()
		m.initiateTransfer(session, session.AccountName, overflowTeamID, ctx.Path)
		outcome2, ok2 := <-transferDone2
		if ok2 && outcome2 != "" {
			outcome = outcome2
		}
	}

	m.log.Info("Transfer done, resuming IVR", "call_id", session.ID, "outcome", outcome)

	// Create a fresh IVRPlayer for post-transfer audio. EndTransfer saved
	// the last RTP seq/ts from the bridge so we can continue from there.
	session.mu.Lock()
	var postTransferTrack *webrtc.TrackLocalStaticRTP
	if session.Direction == models.CallDirectionOutgoing {
		postTransferTrack = session.WAAudioTrack
	} else {
		postTransferTrack = session.AudioTrack
	}
	newPlayer := NewAudioPlayer(postTransferTrack)
	if session.LastRTPSeq > 0 {
		newPlayer.SetSequence(session.LastRTPSeq, session.LastRTPTimestamp)
	}
	session.IVRPlayer = newPlayer
	session.mu.Unlock()

	return outcome
}

// executeGotoFlow jumps to another IVR flow. Terminal.
func (m *Manager) executeGotoFlow(session *CallSession, node *IVRNode, ctx *IVRContext, waAccount *whatsapp.Account) {
	flowID, _ := node.Config["flow_id"].(string)
	if flowID == "" {
		m.log.Error("goto_flow missing flow_id", "call_id", session.ID)
		m.saveIVRPath(session, ctx.Path)
		return
	}

	targetFlowID, err := uuid.Parse(flowID)
	if err != nil {
		m.log.Error("Invalid goto_flow target ID", "error", err, "call_id", session.ID)
		m.saveIVRPath(session, ctx.Path)
		return
	}

	targetFlowPtr := m.getIVRFlowCached(targetFlowID)
	if targetFlowPtr == nil {
		m.log.Error("Failed to load goto_flow target", "call_id", session.ID, "flow_id", flowID)
		m.saveIVRPath(session, ctx.Path)
		return
	}
	targetFlow := *targetFlowPtr

	if !targetFlow.IsActive {
		m.log.Warn("goto_flow target is disabled", "call_id", session.ID, "flow_id", flowID)
		m.saveIVRPath(session, ctx.Path)
		return
	}

	ctx.Path = append(ctx.Path, map[string]string{"action": "goto_flow", "flow": targetFlow.Name})
	m.saveIVRPath(session, ctx.Path)

	// Switch to the new flow
	session.mu.Lock()
	session.IVRFlow = &targetFlow
	session.mu.Unlock()

	m.db.Model(&models.CallLog{}).
		Where("id = ?", session.CallLogID).
		Update("ivr_flow_id", targetFlow.ID)

	m.runIVRFlow(session, waAccount)
}

// executeTiming branches based on business hours schedule.
func (m *Manager) executeTiming(session *CallSession, node *IVRNode) string {
	now := time.Now()
	dayName := strings.ToLower(now.Weekday().String())

	scheduleRaw, _ := node.Config["schedule"].([]any)
	for _, item := range scheduleRaw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		day, _ := entry["day"].(string)
		if strings.ToLower(day) != dayName {
			continue
		}
		enabled, _ := entry["enabled"].(bool)
		if !enabled {
			return "out_of_hours"
		}
		startStr, _ := entry["start_time"].(string)
		endStr, _ := entry["end_time"].(string)

		startTime, err1 := time.Parse("15:04", startStr)
		endTime, err2 := time.Parse("15:04", endStr)
		if err1 != nil || err2 != nil {
			m.log.Error("Invalid schedule time format", "call_id", session.ID, "start", startStr, "end", endStr)
			return "out_of_hours"
		}

		nowMinutes := now.Hour()*60 + now.Minute()
		startMinutes := startTime.Hour()*60 + startTime.Minute()
		endMinutes := endTime.Hour()*60 + endTime.Minute()

		if nowMinutes >= startMinutes && nowMinutes < endMinutes {
			return "in_hours"
		}
		return "out_of_hours"
	}

	// Day not found in schedule — treat as out of hours
	return "out_of_hours"
}

// executeCondition branches on a variable comparison. Outcomes: "true", "false".
func (m *Manager) executeCondition(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	left := resolveIVRLeftValue(node.Config, ctx)
	matched := matchIVRCondition(node.Config, left)
	m.log.Info("IVR condition evaluated",
		"call_id", session.ID,
		"node", node.ID,
		"left", left,
		"matched", matched,
	)
	if matched {
		return "true"
	}
	return "false"
}

// executeSetVariable assigns template values into session variables. Outcome: "default".
func (m *Manager) executeSetVariable(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	n := applyIVRSetVariables(node.Config, ctx)
	m.log.Info("IVR set_variable applied", "call_id", session.ID, "node", node.ID, "count", n)
	return "default"
}

// executeConfirm plays back a gathered value and waits for confirm/retry DTMF.
// Outcomes: "confirmed", "retry", "timeout", "max_retries".
func (m *Manager) executeConfirm(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	varName, _ := node.Config["variable"].(string)
	varName = strings.TrimSpace(varName)
	value := ""
	if ctx != nil && ctx.Variables != nil && varName != "" {
		value = ctx.Variables[varName]
	}
	confirmDigit := ivrConfirmDigit(node.Config, "confirm_digit", "1")
	retryDigit := ivrConfirmDigit(node.Config, "retry_digit", "2")
	timeoutSecs := getConfigInt(node.Config, "timeout_seconds", 10)
	maxRetries := getConfigInt(node.Config, "max_retries", 3)
	timeout := time.Duration(timeoutSecs) * time.Second

	// Prefer explicit greeting_text / audio_file; otherwise synthesize from prompt_template.
	audioFile, _ := node.Config["audio_file"].(string)
	greetingText, _ := node.Config["greeting_text"].(string)
	if strings.TrimSpace(greetingText) == "" && audioFile == "" {
		greetingText = ivrConfirmPrompt(node.Config, value)
	}
	// Ensure {{value}} and variable name resolve in TTS templates.
	if ctx != nil {
		if ctx.Variables == nil {
			ctx.Variables = map[string]string{}
		}
		ctx.Variables["value"] = value
		if varName != "" {
			ctx.Variables[varName] = value
		}
	}
	audioFile = m.resolvePromptAudioText(node.ID, audioFile, greetingText, ctx)
	var fullPath string
	if audioFile != "" && m.config.AudioDir != "" {
		fullPath = filepath.Join(m.config.AudioDir, audioFile)
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		m.drainDTMF(session)

		var digit byte
		var gotDigit bool

		if fullPath != "" {
			playDone := make(chan struct{})
			go func() {
				if _, err := player.PlayFile(fullPath); err != nil {
					m.log.Error("Failed to play confirm audio", "error", err, "call_id", session.ID)
				}
				close(playDone)
			}()

			select {
			case <-playDone:
				digit, gotDigit = m.waitForDTMF(session, timeout, 1)
			case d, chOk := <-session.DTMFBuffer:
				player.Stop()
				<-playDone
				player.ResetAfterInterrupt()
				if chOk {
					digit = d
					gotDigit = true
				}
			}
		} else {
			digit, gotDigit = m.waitForDTMF(session, timeout, 1)
		}

		if !gotDigit {
			if attempt+1 >= maxRetries {
				return "max_retries"
			}
			continue
		}

		d := string(digit)
		switch d {
		case confirmDigit:
			return "confirmed"
		case retryDigit:
			return "retry"
		default:
			// Invalid digit — retry
			if attempt+1 >= maxRetries {
				return "max_retries"
			}
		}
	}
	return "max_retries"
}

// executeDelay pauses the call with silence or hold music. Outcome: "default".
func (m *Manager) executeDelay(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	secs := getConfigInt(node.Config, "seconds", 3)
	if secs < 0 {
		secs = 0
	}
	if secs > 300 {
		secs = 300
	}
	mode, _ := node.Config["mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "silence"
	}
	duration := time.Duration(secs) * time.Second

	if mode == "hold_music" || mode == "music" {
		audioFile, _ := node.Config["audio_file"].(string)
		if audioFile == "" {
			audioFile = m.config.HoldMusicFile
		}
		fullPath := audioFile
		if audioFile != "" && m.config.AudioDir != "" && !filepath.IsAbs(audioFile) {
			fullPath = filepath.Join(m.config.AudioDir, audioFile)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = player.PlayFileLoop(fullPath)
		}()
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			player.Stop()
			<-done
		case <-done:
		}
		// Check call still active
		session.mu.Lock()
		status := session.Status
		session.mu.Unlock()
		if status != models.CallStatusAnswered {
			return "default"
		}
		return "default"
	}

	player.PlaySilence(duration)
	return "default"
}

// executeHangup plays optional goodbye audio and terminates the call. Terminal.
func (m *Manager) executeHangup(session *CallSession, node *IVRNode, ctx *IVRContext, waAccount *whatsapp.Account, player *AudioPlayer) {
	audioFile := m.resolvePromptAudio(node, ctx)
	if audioFile != "" && m.config.AudioDir != "" {
		fullPath := filepath.Join(m.config.AudioDir, audioFile)
		if _, err := player.PlayFile(fullPath); err != nil {
			m.log.Error("Failed to play hangup audio", "error", err, "call_id", session.ID)
		}
	}

	// Mark as system-initiated hangup before terminating so the webhook
	// handler (which defaults to "client") doesn't overwrite it.
	if session.CallLogID != uuid.Nil {
		m.db.Model(&models.CallLog{}).
			Where("id = ?", session.CallLogID).
			Update("disconnected_by", models.DisconnectedBySystem)
	}

	m.saveIVRPath(session, ctx.Path)
	if waAccount != nil {
		m.terminateCall(session, waAccount)
	} else {
		m.terminateCallBySession(session)
	}
}

// --- Helpers ---

// playInterruptible plays audio but stops if a DTMF digit arrives.
func (m *Manager) playInterruptible(session *CallSession, player *AudioPlayer, audioFile string) {
	playDone := make(chan struct{})
	go func() {
		if _, err := player.PlayFile(audioFile); err != nil {
			m.log.Error("Failed to play audio", "error", err, "call_id", session.ID)
		}
		close(playDone)
	}()

	select {
	case <-playDone:
		// Played fully
	case _, ok := <-session.DTMFBuffer:
		player.Stop()
		<-playDone
		player.ResetAfterInterrupt()
		if ok {
			m.log.Info("Audio interrupted by DTMF", "call_id", session.ID)
		}
	}
}

// drainDTMF discards any buffered DTMF digits.
func (m *Manager) drainDTMF(session *CallSession) {
	for {
		select {
		case <-session.DTMFBuffer:
		default:
			return
		}
	}
}

// waitForDTMF waits for a DTMF digit with timeout and retries.
func (m *Manager) waitForDTMF(session *CallSession, timeout time.Duration, maxRetries int) (byte, bool) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case digit, ok := <-session.DTMFBuffer:
			if !ok {
				return 0, false
			}
			return digit, true
		case <-time.After(timeout):
			m.log.Debug("DTMF timeout", "call_id", session.ID, "attempt", attempt+1)
		}
	}
	return 0, false
}

// saveIVRPath saves the recorded IVR navigation path to the call log.
func (m *Manager) saveIVRPath(session *CallSession, path []map[string]string) {
	if len(path) == 0 {
		return
	}

	pathJSON := models.JSONB{}
	pathJSON["steps"] = path

	m.db.Model(&models.CallLog{}).
		Where("id = ?", session.CallLogID).
		Update("ivr_path", pathJSON)
}

// getConfigInt extracts an int from a config map with a default fallback.
func getConfigInt(config map[string]any, key string, defaultVal int) int {
	v, ok := config[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return defaultVal
}
