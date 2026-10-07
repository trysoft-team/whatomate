package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	aiCustomActionMaxHeaders = 10
	aiCustomActionMaxCode    = 20000
)

// aiCustomActionIcons matches the icon picker on the Custom Actions page.
var aiCustomActionIcons = []string{
	"ticket", "user", "bar-chart", "link", "phone", "mail", "file-text", "external-link", "zap", "globe", "code",
}

var aiCustomActionMethods = []string{"POST", "GET", "PUT", "PATCH"}

var aiCustomActionVarRe = regexp.MustCompile(`\{\{[^}]+\}\}`)

const aiCustomActionSystemPrompt = `You design "custom actions" for Whatomate, a WhatsApp business inbox. A custom action is a button agents click in a chat; it runs against the open contact.

Return ONLY valid JSON (no markdown fences, no commentary) with this shape:
{
  "name": "Create ticket",
  "icon": "ticket",
  "action_type": "webhook",
  "config": { ... },
  "notes": "One short sentence telling the admin what to fill in before saving (e.g. replace the URL or API key). Empty if nothing."
}

action_type is one of:
- "webhook": the server sends an HTTP request. config = {"url": "https://...", "method": "POST" | "GET" | "PUT" | "PATCH", "headers": {"Header-Name": "value"}, "body": "<JSON text with {{variables}}>"}. The body is a string containing JSON. Leave body empty to send the whole context object.
- "url": opens a link in the agent's browser. config = {"url": "https://...?phone={{contact.phone_number}}", "open_in_new_tab": true}.
- "javascript": runs sandboxed JavaScript on the server (no network, no fetch, no require, no timers). config = {"code": "..."}. The code is the body of a function with contact, user, organization and context in scope, and may return an object {"toast": {"message": "...", "type": "success" | "error" | "info" | "warning"}, "clipboard": "text to copy", "url": "https://... to open", "message": "..."}.

Variables available as {{...}} in webhook url/headers/body and url actions (and as objects in JavaScript):
- contact.id, contact.phone_number (E.164 digits, no +), contact.name, contact.profile_name, contact.tags (array), contact.metadata (object)
- user.id, user.name, user.email, user.role (the agent clicking the button)
- organization.id, organization.name

Rules:
- icon is one of: ticket, user, bar-chart, link, phone, mail, file-text, external-link, zap, globe, code.
- name is short (max 40 chars) and starts with a verb when possible.
- Prefer "url" for opening another app (CRM, maps, search), "webhook" for creating or updating records in another system, and "javascript" for formatting text to copy or deriving a link.
- Never invent real credentials. Use obvious placeholders such as YOUR_API_KEY and https://your-helpdesk.example.com, and mention them in notes.
- Webhook bodies must be valid JSON once variables are replaced; wrap string variables in quotes.`

// GenerateCustomAction drafts a custom action from a plain-language
// description. The draft is not saved; the page opens it in the create dialog.
func (a *App) GenerateCustomAction(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceCustomActions, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req struct {
		Prompt string `json:"prompt"`
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

	settings, errMsg := a.chatAISettings(orgID, "")
	if errMsg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errMsg, nil, "")
	}

	raw, err := a.completeAIChat(settings, aiCustomActionSystemPrompt, "Design a custom action for this request:\n\n"+prompt,
		aiFlowGenerateMaxTokens, models.AIUsagePurposeCustomActionGenerate)
	if err != nil {
		a.Log.Error("GenerateCustomAction: LLM call failed", "error", err, "provider", settings.AI.Provider)
		if errors.Is(err, errAIQuotaExceeded) {
			return r.SendErrorEnvelope(fasthttp.StatusTooManyRequests, aiQuotaMessage(settings), nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI generation failed: "+err.Error(), nil, "")
	}

	draft, err := parseAICustomActionDraft(raw)
	if err != nil {
		a.Log.Error("GenerateCustomAction: parse draft", "error", err, "raw_len", len(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an invalid custom action. Try again with a clearer description.", nil, "")
	}
	return r.SendEnvelope(draft)
}

// aiCustomActionDraft mirrors the custom action dialog form.
type aiCustomActionDraft struct {
	Name       string            `json:"name"`
	Icon       string            `json:"icon"`
	ActionType models.ActionType `json:"action_type"`
	Config     map[string]any    `json:"config"`
	Notes      string            `json:"notes"`
}

func parseAICustomActionDraft(raw string) (*aiCustomActionDraft, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(cleaned), &in); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	if inner, ok := in["action"].(map[string]any); ok {
		in = inner
	}
	cfg, _ := in["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}

	d := &aiCustomActionDraft{
		Name:       truncateRunes(strings.TrimSpace(stringField(in, "name")), 100),
		Icon:       strings.ToLower(strings.TrimSpace(stringField(in, "icon"))),
		ActionType: models.ActionType(strings.ToLower(strings.TrimSpace(stringField(in, "action_type")))),
		Notes:      truncateRunes(strings.TrimSpace(stringField(in, "notes")), 300),
	}
	if !contains(aiCustomActionIcons, d.Icon) {
		d.Icon = "zap"
	}

	switch d.ActionType {
	case models.ActionTypeWebhook:
		u := strings.TrimSpace(stringField(cfg, "url"))
		if !isHTTPURLTemplate(u) {
			return nil, fmt.Errorf("webhook url %q is not http(s)", u)
		}
		method := strings.ToUpper(strings.TrimSpace(stringField(cfg, "method")))
		if !contains(aiCustomActionMethods, method) {
			method = "POST"
		}
		d.Config = map[string]any{
			"url":     u,
			"method":  method,
			"headers": sanitizeAICustomActionHeaders(cfg["headers"]),
			"body":    aiCustomActionBody(cfg["body"]),
		}
	case models.ActionTypeURL:
		u := strings.TrimSpace(stringField(cfg, "url"))
		if !isHTTPURLTemplate(u) {
			return nil, fmt.Errorf("url %q is not http(s)", u)
		}
		newTab := true
		if v, ok := cfg["open_in_new_tab"].(bool); ok {
			newTab = v
		}
		d.Config = map[string]any{"url": u, "open_in_new_tab": newTab}
	case models.ActionTypeJavascript:
		code := strings.TrimSpace(stringField(cfg, "code"))
		if code == "" || utf8.RuneCountInString(code) > aiCustomActionMaxCode {
			return nil, fmt.Errorf("javascript code is empty or too long")
		}
		if _, err := goja.Compile("action.js", "(function(context, contact, user, organization) {\n"+code+"\n})", false); err != nil {
			return nil, fmt.Errorf("javascript does not compile: %w", err)
		}
		d.Config = map[string]any{"code": code}
	default:
		return nil, fmt.Errorf("unknown action type %q", d.ActionType)
	}

	if d.Name == "" {
		d.Name = map[models.ActionType]string{
			models.ActionTypeWebhook:    "Send to webhook",
			models.ActionTypeURL:        "Open link",
			models.ActionTypeJavascript: "Run script",
		}[d.ActionType]
	}
	return d, nil
}

// isHTTPURLTemplate accepts http(s) URLs that may contain {{variables}}.
func isHTTPURLTemplate(s string) bool {
	if s == "" {
		return false
	}
	probe := aiCustomActionVarRe.ReplaceAllString(s, "x")
	u, err := url.Parse(probe)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func sanitizeAICustomActionHeaders(raw any) map[string]string {
	out := map[string]string{}
	m, _ := raw.(map[string]any)
	for k, v := range m {
		if len(out) >= aiCustomActionMaxHeaders {
			break
		}
		name := strings.TrimSpace(k)
		if name == "" || strings.ContainsAny(name, " :\r\n") || strings.EqualFold(name, "Content-Type") {
			continue
		}
		value := strings.TrimSpace(scalarString(v))
		if value == "" || strings.ContainsAny(value, "\r\n") {
			continue
		}
		out[name] = truncateRunes(value, 500)
	}
	return out
}

// aiCustomActionBody returns the webhook body as pretty JSON text. Models
// sometimes return the body as an object instead of a JSON string.
func aiCustomActionBody(raw any) string {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		var parsed any
		if json.Unmarshal([]byte(aiCustomActionVarRe.ReplaceAllString(s, "0")), &parsed) == nil {
			if pretty, err := json.MarshalIndent(json.RawMessage(s), "", "  "); err == nil {
				return string(pretty)
			}
		}
		return s
	case map[string]any, []any:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return ""
	}
}
