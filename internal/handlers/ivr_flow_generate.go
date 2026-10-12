package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/trysoft/whatomate/internal/calling"
	"github.com/trysoft/whatomate/internal/flowgraph"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

var aiIVRAllowedNodeTypes = map[calling.IVRNodeType]bool{
	calling.IVRNodeStart:        true,
	calling.IVRNodeGreeting:     true,
	calling.IVRNodeMenu:         true,
	calling.IVRNodeGather:       true,
	calling.IVRNodeConfirm:      true,
	calling.IVRNodeTransfer:     true,
	calling.IVRNodeHangup:       true,
	calling.IVRNodeCondition:    true,
	calling.IVRNodeSetVariable:  true,
	calling.IVRNodeDelay:        true,
	calling.IVRNodeTiming:       true,
	calling.IVRNodeSetLabels:    true,
	calling.IVRNodeSendMessage:  true,
	calling.IVRNodeWebhook:      true,
	calling.IVRNodeHTTPCallback: true,
	calling.IVRNodeSpeechInput:  true,
	calling.IVRNodeAIVoice:      true,
	calling.IVRNodeAICall:       true,
	calling.IVRNodeLanguage:     true,
	calling.IVRNodeVoicemail:    true,
	calling.IVRNodeDialOut:      true,
}

const aiIVRFlowGenerateSystemPrompt = `You design WhatsApp voice call (IVR) flows for Whatomate.

Return ONLY valid JSON (no markdown fences, no commentary) with this shape:
{
  "name": "short flow name",
  "description": "one sentence",
  "menu": {
    "version": 2,
    "entry_node": "__start__",
    "nodes": [
      {
        "id": "__start__",
        "type": "start",
        "label": "Start",
        "position": {"x": 100, "y": 80},
        "config": {}
      }
    ],
    "edges": [
      {"from": "__start__", "to": "n1", "condition": "default"}
    ]
  }
}

Rules:
- Always include a start node with id "__start__" and type "start". entry_node must be "__start__".
- Always end reachable branches with a "hangup" node (optional greeting_text / audio_file in config).
- Allowed node types ONLY: start, greeting, menu, gather, confirm, transfer, hangup, condition, set_variable, delay, timing, set_labels, send_message, webhook, http_callback, speech_input, ai_voice, ai_call, language, voicemail, dial_out.
- Use unique string ids like n1, n2, … (except __start__).
- Lay nodes out vertically: x≈100 (or branch x≈320 / x≈540), y increases by ~160 per step.
- Edge conditions:
  - "default" for sequential flow (greeting → next, hangup terminal)
  - "digit:N" for menu option N (must match options keys)
  - "timeout" / "max_retries" for menu/gather failures
  - "true" / "false" for condition
  - "confirmed" / "retry" for confirm
  - "in_hours" / "out_of_hours" for timing
  - "http:2xx" / "http:non2xx" for http_callback / webhook
- Spoken prompts: put text in config.greeting_text (TTS). Leave audio_file empty.
- greeting config: {"greeting_text":"Welcome…","interruptible":false}
- menu config: {"greeting_text":"Press 1 for sales, 2 for support.","timeout_seconds":10,"max_retries":3,"options":{"1":{"label":"Sales"},"2":{"label":"Support"}}}
- gather config: {"greeting_text":"Enter your account number followed by #.","max_digits":10,"terminator":"#","timeout_seconds":10,"max_retries":3,"store_as":"account_number"}
- confirm config: {"variable":"account_number","prompt_template":"You entered {{value}}. Press 1 to confirm or 2 to retry.","confirm_digit":"1","retry_digit":"2","timeout_seconds":10,"max_retries":3}
- transfer config: {"team_id":"_general","announce_queue":false}
- hangup config: {"greeting_text":"Goodbye."}
- condition config: {"field":"account_number","operator":"equals","value":"123","case_sensitive":false}
- set_variable config: {"assignments":[{"name":"tier","value":"gold"}]}
- delay config: {"seconds":3,"mode":"silence"}
- send_message config: {"body":"Thanks for calling","to":""}
- webhook / http_callback config: {"url":"https://example.com/hook","method":"POST","headers":{},"body_template":"{}","timeout_seconds":10}
- ai_call config: {"welcome_message":"Hello! How can I help?","system_instructions":"You are a helpful phone agent.","voice_provider":"gemini","voice_id":"Puck","listen_seconds":8,"max_turns":8}
- Prefer short spoken copy. Keep graphs small: typically 4–12 nodes. Do not invent team UUIDs, flow UUIDs, or external IDs you were not given — use "_general" for transfer team_id when unsure.`

// GenerateIVRFlow uses the org's chatbot AI settings to draft a v2 call-flow graph
// from a plain-language description (same AI config as chatbot Generate with AI).
func (a *App) GenerateIVRFlow(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite)
	if err != nil {
		return nil
	}
	_ = userID

	var req struct {
		Prompt          string `json:"prompt"`
		WhatsAppAccount string `json:"whatsapp_account"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "prompt is required", nil, "")
	}
	if utf8.RuneCountInString(prompt) > aiFlowGenerateMaxPromptRunes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "prompt is too long", nil, "")
	}

	settings, err := a.getChatbotSettingsCached(orgID, strings.TrimSpace(req.WhatsAppAccount))
	if err != nil || settings == nil {
		a.Log.Error("GenerateIVRFlow: load settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load AI settings", nil, "")
	}
	if !settings.AI.Enabled || settings.AI.Provider == "" || settings.AI.APIKey == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"AI is not configured. Enable AI and add an API key in Settings → AI.", nil, "")
	}
	if settings.AI.Model == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "AI model is not configured in Settings → AI", nil, "")
	}

	userMsg := "Describe a WhatsApp voice call (IVR) flow for this workflow:\n\n" + prompt
	raw, err := a.completeAIChat(settings, aiIVRFlowGenerateSystemPrompt, userMsg, aiFlowGenerateMaxTokens, models.AIUsagePurposeFlowGenerate)
	if err != nil {
		a.Log.Error("GenerateIVRFlow: LLM call failed", "error", err, "provider", settings.AI.Provider)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI generation failed: "+err.Error(), nil, "")
	}

	draft, err := parseAIIVRFlowDraft(raw)
	if err != nil {
		a.Log.Error("GenerateIVRFlow: parse draft", "error", err, "raw_len", len(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an invalid flow. Try again with a clearer description.", nil, "")
	}

	menu := sanitizeAIGeneratedIVRMenu(draft.Menu)
	if menu == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an empty or invalid graph. Try again.", nil, "")
	}

	name := strings.TrimSpace(draft.Name)
	if name == "" {
		name = "AI call flow"
	}
	if utf8.RuneCountInString(name) > 100 {
		name = string([]rune(name)[:100])
	}
	desc := strings.TrimSpace(draft.Description)

	return r.SendEnvelope(map[string]any{
		"name":        name,
		"description": desc,
		"menu":        menu,
	})
}

type aiIVRFlowDraft struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Menu        *calling.IVRFlowGraph `json:"menu"`
}

func parseAIIVRFlowDraft(raw string) (*aiIVRFlowDraft, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var draft aiIVRFlowDraft
	if err := json.Unmarshal([]byte(cleaned), &draft); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	if draft.Menu == nil {
		// Some models nest under "graph" or return the menu at the top level.
		var wrapper struct {
			Graph *calling.IVRFlowGraph `json:"graph"`
		}
		if err := json.Unmarshal([]byte(cleaned), &wrapper); err == nil && wrapper.Graph != nil {
			draft.Menu = wrapper.Graph
		} else {
			var asMenu calling.IVRFlowGraph
			if err := json.Unmarshal([]byte(cleaned), &asMenu); err == nil && asMenu.Version == 2 && len(asMenu.Nodes) > 0 {
				draft.Menu = &asMenu
			} else {
				return nil, fmt.Errorf("missing menu")
			}
		}
	}
	return &draft, nil
}

// sanitizeAIGeneratedIVRMenu normalizes an LLM-produced IVR menu into a usable
// v2 flow graph for the Call Flow builder.
func sanitizeAIGeneratedIVRMenu(g *calling.IVRFlowGraph) map[string]any {
	if g == nil {
		return nil
	}

	nodes := make([]calling.IVRNode, 0, len(g.Nodes)+1)
	idSet := map[string]bool{}
	hasStart := false

	for _, n := range g.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" {
			continue
		}
		typ := n.Type
		if typ == "" {
			continue
		}
		if !aiIVRAllowedNodeTypes[typ] {
			continue
		}
		if typ == calling.IVRNodeStart {
			id = "__start__"
			hasStart = true
		}
		if idSet[id] {
			continue
		}
		if n.Config == nil {
			n.Config = map[string]any{}
		}
		label := strings.TrimSpace(n.Label)
		if label == "" {
			label = string(typ)
		}
		pos := n.Position
		if pos.X == 0 && pos.Y == 0 && id != "__start__" {
			pos.X = 100
			pos.Y = float64(160 * (len(nodes) + 1))
		}
		nodes = append(nodes, calling.IVRNode{
			ID:       id,
			Type:     typ,
			Label:    label,
			Position: pos,
			Config:   n.Config,
		})
		idSet[id] = true
	}

	if !hasStart {
		start := calling.IVRNode{
			ID:       "__start__",
			Type:     calling.IVRNodeStart,
			Label:    "Start",
			Position: flowgraph.Position{X: 100, Y: 80},
			Config:   map[string]any{},
		}
		nodes = append([]calling.IVRNode{start}, nodes...)
		idSet["__start__"] = true
	}

	edges := make([]calling.IVREdge, 0, len(g.Edges))
	edgeSeen := map[string]bool{}
	for _, e := range g.Edges {
		from := strings.TrimSpace(e.From)
		to := strings.TrimSpace(e.To)
		if from == "start" {
			from = "__start__"
		}
		if !idSet[from] || !idSet[to] || from == to {
			continue
		}
		cond := strings.TrimSpace(e.Condition)
		if cond == "" {
			cond = "default"
		}
		key := from + "|" + to + "|" + cond
		if edgeSeen[key] {
			continue
		}
		edgeSeen[key] = true
		edges = append(edges, calling.IVREdge{From: from, To: to, Condition: cond})
	}

	hasStartOut := false
	for _, e := range edges {
		if e.From == "__start__" {
			hasStartOut = true
			break
		}
	}
	if !hasStartOut {
		for _, n := range nodes {
			if n.ID != "__start__" {
				edges = append(edges, calling.IVREdge{From: "__start__", To: n.ID, Condition: "default"})
				break
			}
		}
	}

	out := &calling.IVRFlowGraph{
		Version:   2,
		EntryNode: "__start__",
		Nodes:     nodes,
		Edges:     edges,
	}
	out.BuildMaps()
	if out.Node("__start__") == nil {
		return nil
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	var asMap map[string]any
	if err := json.Unmarshal(b, &asMap); err != nil {
		return nil
	}
	return asMap
}
