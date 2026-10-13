package calling

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/models"
)

// executeAICall runs a multi-turn voice conversation with optional LLM tool calls.
// Outcomes: completed, transfer, max_turns, error.
// A Gemini key runs a realtime Gemini Live session; anything else (or Live
// being unreachable) uses the turn-based Whisper/TTS loop.
func (m *Manager) executeAICall(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	if m.realtimeAICallEnabled() {
		if outcome, handled := m.executeAICallRealtime(session, node, ctx, player); handled {
			return outcome
		}
	}
	return m.executeAICallTurnBased(session, node, ctx, player)
}

func (m *Manager) realtimeAICallEnabled() bool {
	return m != nil && (m.config == nil || !m.config.TurnBasedAICall)
}

func (m *Manager) executeAICallTurnBased(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	welcome, _ := node.Config["welcome_message"].(string)
	systemInstr, _ := node.Config["system_instructions"].(string)
	if systemInstr == "" {
		systemInstr, _ = node.Config["system_prompt"].(string)
	}
	maxTurns := getConfigInt(node.Config, "max_turns", 8)
	if maxTurns <= 0 {
		maxTurns = 8
	}
	listenSecs := getConfigInt(node.Config, "listen_seconds", 8)
	if listenSecs <= 0 {
		listenSecs = 8
	}
	exitDigit, _ := node.Config["exit_digit"].(string)
	transferTeamID, _ := node.Config["transfer_team_id"].(string)
	tools := parseAICallTools(node.Config["tools"])

	session.mu.Lock()
	session.AICallNodeID = node.ID
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.AICallNodeID = ""
		session.mu.Unlock()
	}()

	settings := m.loadOrgAISettings(session.OrganizationID)
	// Always use org chatbot model/provider/key (no per-node model override).
	apiKey, provider, llmModel, maxTokens := m.resolveAILLMCreds(settings, "")
	if apiKey == "" {
		m.log.Warn("IVR ai_call: AI not configured", "call_id", session.ID)
		return "error"
	}
	if systemInstr == "" {
		systemInstr = settings.AI.SystemPrompt
	}
	if systemInstr == "" {
		systemInstr = "You are a helpful phone IVR assistant. Keep replies under 2 short sentences. Use tools when needed."
	}
	systemInstr = interpolateTemplate(systemInstr, ctx.Variables)
	systemInstr, welcome = m.applyAITransferBusy(session, ctx, systemInstr, welcome)
	// Seed with org AI Contexts + optional extra_knowledge; refreshed each turn.
	baseSystem := systemInstr
	systemInstr = m.composeAICallSystemPrompt(session, ctx, baseSystem, "", node.Config)

	messages := []map[string]any{
		{"role": "system", "content": systemInstr},
	}

	// Welcome TTS
	if strings.TrimSpace(welcome) != "" && player != nil && m.tts != nil {
		spoken := interpolateTemplate(welcome, ctx.Variables)
		if filename, err := m.synthesizeTTS(session.OrganizationID, spoken, node.Config); err != nil {
			m.log.Error("IVR ai_call welcome TTS failed", "error", err, "call_id", session.ID)
		} else if m.config != nil && m.config.AudioDir != "" {
			m.playInterruptible(session, player, filepath.Join(m.config.AudioDir, filename))
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": spoken})
	}

	openaiTools := toolsToOpenAISchema(tools)

	for turn := 0; turn < maxTurns; turn++ {
		session.mu.Lock()
		status := session.Status
		session.mu.Unlock()
		if status != models.CallStatusAnswered {
			return "completed"
		}

		// Listen
		path, terminated, err := m.recordCallerAudio(session, player, time.Duration(listenSecs)*time.Second, exitDigit, false)
		if err != nil {
			m.log.Warn("IVR ai_call record failed", "error", err, "call_id", session.ID)
			return "error"
		}
		if terminated {
			_ = os.Remove(path)
			return "completed"
		}
		userText := ""
		func() {
			defer func() { _ = os.Remove(path) }()
			key := m.resolveOpenAIKey(session.OrganizationID)
			if key == "" {
				m.log.Warn("IVR ai_call: no OpenAI key for Whisper STT (Google LLM key cannot be used)", "call_id", session.ID)
				return
			}
			text, terr := transcribeWhisper(key, path, session.Locale)
			if terr != nil {
				m.log.Warn("IVR ai_call Whisper failed", "error", terr, "call_id", session.ID)
				return
			}
			userText = strings.TrimSpace(text)
		}()

		// Silence / failed STT — keep listening; do not treat as exit.
		if userText == "" {
			continue
		}
		if ctx.Variables == nil {
			ctx.Variables = map[string]string{}
		}
		ctx.Variables["ai_last_user"] = userText
		messages = append(messages, map[string]any{"role": "user", "content": userText})

		// Refresh system prompt with AI Contexts + extra knowledge using this turn's transcript.
		messages[0] = map[string]any{
			"role":    "system",
			"content": m.composeAICallSystemPrompt(session, ctx, baseSystem, userText, node.Config),
		}

		reply, transfer, hangup, err := m.aiCallLLMTurn(apiKey, provider, llmModel, maxTokens, messages, openaiTools, tools, session, ctx, transferTeamID)
		if err != nil {
			m.log.Error("IVR ai_call LLM failed", "error", err, "call_id", session.ID)
			return "error"
		}
		if hangup {
			return "completed"
		}
		if transfer {
			if transferTeamID != "" {
				ctx.Variables["prefer_team_id"] = transferTeamID
			}
			return "transfer"
		}
		if strings.TrimSpace(reply) == "" {
			continue
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": reply})
		ctx.Variables["ai_last_reply"] = reply

		if player != nil && m.tts != nil {
			filename, terr := m.synthesizeTTS(session.OrganizationID, reply, node.Config)
			if terr != nil {
				m.log.Error("IVR ai_call reply TTS failed", "error", terr, "call_id", session.ID)
			} else if m.config != nil && m.config.AudioDir != "" {
				m.playInterruptible(session, player, filepath.Join(m.config.AudioDir, filename))
			}
		}
	}
	return "max_turns"
}

func (m *Manager) resolveAILLMCreds(settings models.ChatbotSettings, modelOverride string) (apiKey, provider, model string, maxTokens int) {
	apiKey = settings.AI.APIKey
	provider = string(settings.AI.Provider)
	model = settings.AI.Model
	maxTokens = settings.AI.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 300
	}
	if modelOverride != "" {
		model = modelOverride
	}
	if !settings.AI.Enabled || apiKey == "" {
		if m.aiConfig != nil && m.aiConfig.OpenAIKey != "" {
			apiKey = m.aiConfig.OpenAIKey
			provider = string(models.AIProviderOpenAI)
			if model == "" {
				model = "gpt-4o-mini"
			}
		}
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	if provider == "" {
		provider = string(models.AIProviderOpenAI)
	}
	return apiKey, provider, model, maxTokens
}

type aiCallToolDef struct {
	Name         string
	Description  string
	Parameters   map[string]any
	Handler      string // http | transfer | hangup | set_variable | nodes
	ReturnToCall bool   // legacy; prefer Handler=="nodes" for fn: edges
	HTTP         struct {
		URL          string
		Method       string
		Headers      map[string]string
		BodyTemplate string
	}
}

func parseAICallTools(raw any) []aiCallToolDef {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]aiCallToolDef, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t := aiCallToolDef{}
		t.Name, _ = m["name"].(string)
		t.Description, _ = m["description"].(string)
		t.Handler, _ = m["handler"].(string)
		if t.Handler == "" {
			t.Handler = "http"
		}
		if v, ok := m["return_to_call"].(bool); ok {
			t.ReturnToCall = v
		}
		// return_to_call is UI-only (AI Call continues). Do NOT coerce handler to
		// nodes — that broke inline HTTP tools that still had the checkbox set.
		if p, ok := m["parameters"].(map[string]any); ok {
			t.Parameters = p
		}
		if httpCfg, ok := m["http"].(map[string]any); ok {
			t.HTTP.URL, _ = httpCfg["url"].(string)
			t.HTTP.Method, _ = httpCfg["method"].(string)
			t.HTTP.BodyTemplate, _ = httpCfg["body_template"].(string)
			if hdrs, ok := httpCfg["headers"].(map[string]any); ok {
				t.HTTP.Headers = map[string]string{}
				for k, v := range hdrs {
					if s, ok := v.(string); ok {
						t.HTTP.Headers[k] = s
					}
				}
			}
		}
		if t.Name != "" {
			out = append(out, t)
		}
	}
	return out
}

func toolsToOpenAISchema(tools []aiCallToolDef) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

func (m *Manager) aiCallLLMTurn(
	apiKey, provider, model string, maxTokens int,
	messages []map[string]any,
	openaiTools []map[string]any,
	toolDefs []aiCallToolDef,
	session *CallSession,
	ctx *IVRContext,
	defaultTransferTeam string,
) (reply string, transfer, hangup bool, err error) {
	// Tool-calling is OpenAI-shaped. Non-OpenAI providers use plain chat (tools ignored).
	if provider != string(models.AIProviderOpenAI) && provider != "" {
		if len(openaiTools) > 0 && session != nil {
			m.log.Info("IVR ai_call: skipping tools for non-OpenAI provider",
				"provider", provider, "call_id", session.ID)
		}
		userMsg := ""
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i]["role"] == "user" {
				userMsg, _ = messages[i]["content"].(string)
				break
			}
		}
		sys := ""
		if len(messages) > 0 {
			if c, ok := messages[0]["content"].(string); ok {
				sys = c
			}
		}
		switch provider {
		case string(models.AIProviderAnthropic):
			reply, err = ivrAnthropicChat(apiKey, model, sys, userMsg, maxTokens)
		case string(models.AIProviderGoogle):
			reply, err = ivrGoogleChat(apiKey, model, sys, userMsg, maxTokens)
		case string(models.AIProviderOpenRouter):
			reply, err = ivrOpenAIChatAt(ivrOpenRouterChatURL, apiKey, model, sys, userMsg, maxTokens)
		default:
			reply, err = ivrOpenAIChat(apiKey, model, sys, userMsg, maxTokens)
		}
		return reply, false, false, err
	}

	const maxToolRounds = 5
	working := append([]map[string]any{}, messages...)
	for round := 0; round < maxToolRounds; round++ {
		content, toolCalls, err := ivrOpenAIChatTools(apiKey, model, working, openaiTools, maxTokens)
		if err != nil {
			return "", false, false, err
		}
		if len(toolCalls) == 0 {
			return content, false, false, nil
		}
		// Append assistant message with tool_calls
		asst := map[string]any{"role": "assistant", "content": content}
		if content == "" {
			asst["content"] = nil
		}
		asst["tool_calls"] = toolCalls
		working = append(working, asst)

		for _, tc := range toolCalls {
			name, _ := tc["function"].(map[string]any)["name"].(string)
			argsStr, _ := tc["function"].(map[string]any)["arguments"].(string)
			id, _ := tc["id"].(string)
			def := findToolDef(toolDefs, name)
			result, isTransfer, isHangup := m.runAICallTool(def, argsStr, session, ctx, defaultTransferTeam)
			if isHangup {
				return "", false, true, nil
			}
			if isTransfer {
				return "", true, false, nil
			}
			working = append(working, map[string]any{
				"role":         "tool",
				"tool_call_id": id,
				"content":      result,
			})
		}
	}
	return "", false, false, fmt.Errorf("tool round limit exceeded")
}

func findToolDef(defs []aiCallToolDef, name string) *aiCallToolDef {
	for i := range defs {
		if defs[i].Name == name {
			return &defs[i]
		}
	}
	return nil
}

func (m *Manager) runAICallTool(def *aiCallToolDef, argsJSON string, session *CallSession, ctx *IVRContext, defaultTransferTeam string) (result string, transfer, hangup bool) {
	if def == nil {
		return `{"error":"unknown tool"}`, false, false
	}
	// Linked Call Flow nodes (fn:<name>) run as a subroutine, then resume AI Call.
	if linked, ok := m.runAICallToolSubroutine(session, def, argsJSON, ctx); ok {
		return linked, false, false
	}
	handler := strings.ToLower(def.Handler)
	switch handler {
	case "nodes":
		return `{"ok":false,"success":false,"error":"no linked nodes","message":"This function has no canvas edge. Link fn:<name> to Send Message, Condition, HTTP, or other logic nodes."}`, false, false
	case "hangup":
		return `{"ok":true}`, false, true
	case "transfer":
		team := defaultTransferTeam
		var args map[string]any
		_ = json.Unmarshal([]byte(argsJSON), &args)
		if t, ok := args["team_id"].(string); ok && t != "" {
			team = t
		}
		if team != "" && ctx != nil {
			if ctx.Variables == nil {
				ctx.Variables = map[string]string{}
			}
			ctx.Variables["prefer_team_id"] = team
		}
		return `{"ok":true,"action":"transfer"}`, true, false
	case "set_variable":
		var args map[string]any
		_ = json.Unmarshal([]byte(argsJSON), &args)
		if ctx.Variables == nil {
			ctx.Variables = map[string]string{}
		}
		for k, v := range args {
			ctx.Variables[k] = fmt.Sprint(v)
		}
		return `{"ok":true}`, false, false
	default: // http
		vars := map[string]string{}
		if ctx != nil && ctx.Variables != nil {
			for k, v := range ctx.Variables {
				vars[k] = v
			}
		}
		var args map[string]any
		_ = json.Unmarshal([]byte(argsJSON), &args)
		for k, v := range args {
			vars[k] = fmt.Sprint(v)
		}
		url := interpolateTemplate(def.HTTP.URL, vars)
		method := def.HTTP.Method
		if method == "" {
			method = "POST"
		}
		body := interpolateTemplate(def.HTTP.BodyTemplate, vars)
		if body == "" && argsJSON != "" {
			body = argsJSON
		}
		headers := map[string]string{}
		for k, v := range def.HTTP.Headers {
			headers[k] = interpolateTemplate(v, vars)
		}
		res, err := executeHTTPCallback(url, method, headers, body, 15*time.Second)
		if err != nil {
			return fmt.Sprintf(`{"ok":false,"success":false,"error":%q,"message":"The HTTP request could not be sent with the data you gathered. Tell the caller it was not processed and offer to try again or transfer to an agent."}`, err.Error()), false, false
		}
		ok := res.StatusCode >= 200 && res.StatusCode < 300
		bodyOut := res.Body
		if len(bodyOut) > 4000 {
			bodyOut = bodyOut[:4000] + "…"
		}
		payload := map[string]any{
			"ok":      ok,
			"success": ok,
			"status":  res.StatusCode,
			"body":    bodyOut,
		}
		if ok {
			payload["message"] = "The HTTP request succeeded: the data you gathered was processed. Confirm the outcome to the caller using body if it contains useful details."
		} else {
			payload["message"] = "The HTTP request failed: the data you gathered was not processed. Tell the caller it did not go through and offer to try again or transfer to an agent."
		}
		b, _ := json.Marshal(payload)
		return string(b), false, false
	}
}

func ivrOpenAIChatTools(apiKey, model string, messages []map[string]any, tools []map[string]any, maxTokens int) (content string, toolCalls []map[string]any, err error) {
	return ivrOpenAIChatToolsAt("https://api.openai.com/v1/chat/completions", apiKey, model, messages, tools, maxTokens)
}

func ivrOpenAIChatToolsAt(endpoint, apiKey, model string, messages []map[string]any, tools []map[string]any, maxTokens int) (string, []map[string]any, error) {
	payload := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("openai %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", nil, err
	}
	if len(parsed.Choices) == 0 {
		return "", nil, fmt.Errorf("empty openai response")
	}
	msg := parsed.Choices[0].Message
	var tcs []map[string]any
	for _, tc := range msg.ToolCalls {
		tcs = append(tcs, map[string]any{
			"id":   tc.ID,
			"type": tc.Type,
			"function": map[string]any{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		})
	}
	return strings.TrimSpace(msg.Content), tcs, nil
}
