package handlers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/trysoft/whatomate/internal/flowgraph"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	aiFlowGenerateMaxPromptRunes = 4000
	aiFlowGenerateMaxTokens      = 4096
)

var aiFlowAllowedNodeTypes = map[ChatNodeType]bool{
	ChatNodeStart:       true,
	ChatNodeMessage:     true,
	ChatNodeButtons:     true,
	ChatNodeList:        true,
	ChatNodePrompt:      true,
	ChatNodeDelay:       true,
	ChatNodeCondition:   true,
	ChatNodeTransfer:    true,
	ChatNodeSetLabels:   true,
	ChatNodeSetVariable: true,
	ChatNodeWebhook:     true,
	ChatNodeSendEmail:   true,
	ChatNodeEnd:         true,
}

const aiFlowGenerateSystemPrompt = `You design WhatsApp chatbot conversation flows for Whatomate.

Return ONLY valid JSON (no markdown fences, no commentary) with this shape:
{
  "name": "short flow name",
  "description": "one sentence",
  "trigger_keywords": ["keyword1", "keyword2"],
  "graph": {
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
- Always end reachable branches with an "end" node (optional message in config.message).
- Allowed node types ONLY: start, message, buttons, list, prompt, delay, condition, transfer, set_labels, set_variable, webhook, send_email, end.
- Use unique string ids like n1, n2, … (except __start__).
- Lay nodes out vertically: x≈100 (or branch x≈320 / x≈540), y increases by ~160 per step.
- Edge conditions:
  - "default" for sequential flow
  - "button:<id>" for buttons/list row replies (id must match button/row id)
  - "true" / "false" for simple condition expression mode
  - "else" for condition rules fallback
- message config: {"message":"text","wait_for_reply":false,"store_as":""}
- buttons config: {"body":"Choose:","buttons":[{"id":"opt_a","title":"Option A"}],"store_as":"choice"}
- list config: {"body":"Pick one","button":"Options","sections":[{"title":"","rows":[{"id":"r1","title":"Row","description":""}]}],"store_as":"choice"}
- prompt config: {"body":"Ask…","store_as":"answer","validation_regex":"","validation_error":"","max_retries":2}
- delay config: {"seconds": 60} (1–3600)
- condition config: {"mode":"expression","expression":"{{choice}} == \"opt_a\""} OR {"mode":"rules","variable":"choice","rules":[{"name":"A","operator":"eq","value":"opt_a","handle":"a"}]}
- transfer config: {"body":"Connecting you…","team_id":"_general","notes":""}
- set_labels config: {"labels":["onboarded"],"mode":"add"}
- set_variable config: {"set":{"var_name":"value or {{template}}"}}
- webhook config: {"url":"https://example.com/hook","method":"POST","headers":{},"body":"{{{inbound}}}"}
- send_email config: {"to":"team@example.com","subject":"New lead","html":"<p>…</p>"}
- Prefer WhatsApp-friendly short copy. Use {{contact_name}} / {{senderMessage}} when helpful.
- Keep graphs small: typically 4–12 nodes. Do not invent APIs, UUIDs, or external IDs you were not given.`

// GenerateChatbotFlow uses the org's chatbot AI settings to draft a v2 flow graph
// from a plain-language description.
func (a *App) GenerateChatbotFlow(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.HasPermission(userID, models.ResourceFlowsChatbot, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Permission denied", nil, "")
	}

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
		a.Log.Error("GenerateChatbotFlow: load settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load chatbot settings", nil, "")
	}
	if !settings.AI.Enabled || settings.AI.Provider == "" || settings.AI.APIKey == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"AI is not configured. Enable AI and add an API key in Settings → AI.", nil, "")
	}
	if settings.AI.Model == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "AI model is not configured in Settings → AI", nil, "")
	}

	userMsg := "Describe a WhatsApp chatbot flow for this workflow:\n\n" + prompt
	raw, err := a.completeAIChat(settings, aiFlowGenerateSystemPrompt, userMsg, aiFlowGenerateMaxTokens, models.AIUsagePurposeFlowGenerate)
	if err != nil {
		a.Log.Error("GenerateChatbotFlow: LLM call failed", "error", err, "provider", settings.AI.Provider)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI generation failed: "+err.Error(), nil, "")
	}

	draft, err := parseAIFlowDraft(raw)
	if err != nil {
		a.Log.Error("GenerateChatbotFlow: parse draft", "error", err, "raw_len", len(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an invalid flow. Try again with a clearer description.", nil, "")
	}

	graph := sanitizeAIGeneratedGraph(draft.Graph)
	if graph == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an empty or invalid graph. Try again.", nil, "")
	}

	name := strings.TrimSpace(draft.Name)
	if name == "" {
		name = "AI draft"
	}
	if utf8.RuneCountInString(name) > 100 {
		name = string([]rune(name)[:100])
	}
	desc := strings.TrimSpace(draft.Description)
	keywords := sanitizeTriggerKeywords(draft.TriggerKeywords)

	return r.SendEnvelope(map[string]any{
		"name":             name,
		"description":      desc,
		"trigger_keywords": keywords,
		"graph":            graph,
	})
}

type aiFlowDraft struct {
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	TriggerKeywords []string   `json:"trigger_keywords"`
	Graph           *ChatGraph `json:"graph"`
}

func parseAIFlowDraft(raw string) (*aiFlowDraft, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var draft aiFlowDraft
	if err := json.Unmarshal([]byte(cleaned), &draft); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	if draft.Graph == nil {
		// Some models nest under "flow" or return the graph at the top level.
		var asGraph ChatGraph
		if err := json.Unmarshal([]byte(cleaned), &asGraph); err == nil && asGraph.Version == 2 && len(asGraph.Nodes) > 0 {
			draft.Graph = &asGraph
		} else {
			return nil, fmt.Errorf("missing graph")
		}
	}
	return &draft, nil
}

var jsonFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if m := jsonFenceRe.FindStringSubmatch(s); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return ""
}

func sanitizeTriggerKeywords(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, k := range in {
		k = strings.TrimSpace(strings.ToLower(k))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
		if len(out) >= 10 {
			break
		}
	}
	return out
}

// sanitizeAIGeneratedGraph normalizes and validates an LLM-produced graph into
// a usable ChatFlowGraph map for the builder (version 2, start entry, known types).
func sanitizeAIGeneratedGraph(g *ChatGraph) map[string]any {
	if g == nil {
		return nil
	}

	nodes := make([]ChatNode, 0, len(g.Nodes)+1)
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
		if !aiFlowAllowedNodeTypes[typ] {
			continue
		}
		if typ == ChatNodeStart {
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
		nodes = append(nodes, ChatNode{
			ID:       id,
			Type:     typ,
			Label:    label,
			Position: pos,
			Config:   n.Config,
		})
		idSet[id] = true
	}

	if !hasStart {
		start := ChatNode{
			ID:       "__start__",
			Type:     ChatNodeStart,
			Label:    "Start",
			Position: flowgraph.Position{X: 100, Y: 80},
			Config:   map[string]any{},
		}
		nodes = append([]ChatNode{start}, nodes...)
		idSet["__start__"] = true
	}

	edges := make([]ChatEdge, 0, len(g.Edges))
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
		edges = append(edges, ChatEdge{From: from, To: to, Condition: cond})
	}

	// Ensure start has at least one outgoing edge when other nodes exist.
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
				edges = append(edges, ChatEdge{From: "__start__", To: n.ID, Condition: "default"})
				break
			}
		}
	}

	out := &ChatGraph{
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
