package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	aiCannedMaxContent       = 4096 // WhatsApp text message limit
	aiCannedMaxButtonContent = 1024 // interactive message body limit
	aiCannedMaxShortcut      = 30
	aiCannedExistingLimit    = 100
)

// aiCannedCategories matches CANNED_RESPONSE_CATEGORIES in the frontend.
var aiCannedCategories = []string{"greeting", "support", "sales", "closing", "general"}

var aiCannedShortcutInvalid = regexp.MustCompile(`[^a-z0-9_-]+`)

const aiCannedSystemPrompt = `You write "canned responses" for agents in Whatomate, a WhatsApp business inbox. A canned response is a saved reply an agent inserts with /shortcut.

Return ONLY valid JSON (no markdown fences, no commentary) with this shape:
{
  "name": "Order delayed apology",
  "shortcut": "delay",
  "category": "support",
  "content": "Hi {{contact_name}}, ...",
  "buttons": [],
  "notes": "One short sentence telling the admin what to check before saving (e.g. fill in the refund policy link). Empty if nothing."
}

Rules:
- category is one of: greeting, support, sales, closing, general.
- name is short (max 40 chars). shortcut is lowercase letters, digits, - or _ (max 20 chars, no leading slash).
- content is the WhatsApp message. Keep it friendly, concise and ready to send. Use WhatsApp formatting only: *bold*, _italic_, ~strike~, line breaks and bullet lines starting with "• ". No markdown headings, tables or [text](url) links. Emojis sparingly.
- Placeholders use double braces. These fill in automatically: {{contact_name}} (customer name), {{phone_number}} (customer phone), {{agent_name}} (the agent sending). Any other {{token}} (for example {{order_id}} or {{tracking_link}}) makes the agent type a value before sending; use such tokens for details that change per customer instead of inventing them.
- Write in the language of the request unless it asks for another language.
- buttons is optional. Either up to 3 quick-reply buttons [{"type":"reply","title":"Yes"}] or exactly one link button [{"type":"url","title":"Track order","url":"https://..."}]. Never mix them. Titles max 20 characters. Only add buttons when the request asks for choices or a link.
- Never invent real prices, policies, phone numbers or URLs. Use tokens or obvious placeholders such as https://your-site.example.com and mention them in notes.`

// GenerateCannedResponse drafts a canned response from a plain-language
// description. The draft is not saved; the page opens it in the editor.
func (a *App) GenerateCannedResponse(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceCannedResponses, models.ActionWrite)
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

	var existing []models.CannedResponse
	a.DB.Select("name", "shortcut").Where("organization_id = ?", orgID).
		Order("usage_count DESC").Limit(aiCannedExistingLimit).Find(&existing)

	var userMsg strings.Builder
	var org models.Organization
	if a.DB.Select("name").Where("id = ?", orgID).First(&org).Error == nil && org.Name != "" {
		fmt.Fprintf(&userMsg, "Business name: %s\n", org.Name)
	}
	if len(existing) > 0 {
		userMsg.WriteString("Existing canned responses (do not reuse these names or shortcuts):\n")
		for _, cr := range existing {
			fmt.Fprintf(&userMsg, "- %s", cr.Name)
			if cr.Shortcut != "" {
				fmt.Fprintf(&userMsg, " (/%s)", cr.Shortcut)
			}
			userMsg.WriteString("\n")
		}
	}
	userMsg.WriteString("\nWrite a canned response for this request:\n\n")
	userMsg.WriteString(prompt)

	raw, err := a.completeAIChat(settings, aiCannedSystemPrompt, userMsg.String(),
		aiFlowGenerateMaxTokens, models.AIUsagePurposeCannedResponseGenerate)
	if err != nil {
		a.Log.Error("GenerateCannedResponse: LLM call failed", "error", err, "provider", settings.AI.Provider)
		if errors.Is(err, errAIQuotaExceeded) {
			return r.SendErrorEnvelope(fasthttp.StatusTooManyRequests, aiQuotaMessage(settings), nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI generation failed: "+err.Error(), nil, "")
	}

	draft, err := parseAICannedResponseDraft(raw)
	if err != nil {
		a.Log.Error("GenerateCannedResponse: parse draft", "error", err, "raw_len", len(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an invalid canned response. Try again with a clearer description.", nil, "")
	}
	draft.Name, draft.Shortcut = uniqueCannedNameAndShortcut(draft.Name, draft.Shortcut, existing)
	return r.SendEnvelope(draft)
}

// aiCannedResponseDraft mirrors the canned response editor form.
type aiCannedResponseDraft struct {
	Name     string                 `json:"name"`
	Shortcut string                 `json:"shortcut"`
	Category string                 `json:"category"`
	Content  string                 `json:"content"`
	Buttons  []CannedResponseButton `json:"buttons"`
	Notes    string                 `json:"notes"`
}

func parseAICannedResponseDraft(raw string) (*aiCannedResponseDraft, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(cleaned), &in); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	if inner, ok := in["canned_response"].(map[string]any); ok {
		in = inner
	}

	content := strings.TrimSpace(stringField(in, "content"))
	if content == "" {
		content = strings.TrimSpace(stringField(in, "message"))
	}
	if content == "" {
		return nil, fmt.Errorf("content is empty")
	}

	d := &aiCannedResponseDraft{
		Name:     truncateRunes(strings.TrimSpace(stringField(in, "name")), 100),
		Shortcut: sanitizeCannedShortcut(stringField(in, "shortcut")),
		Category: strings.ToLower(strings.TrimSpace(stringField(in, "category"))),
		Content:  truncateRunes(content, aiCannedMaxContent),
		Buttons:  sanitizeAICannedButtons(in["buttons"]),
		Notes:    truncateRunes(strings.TrimSpace(stringField(in, "notes")), 300),
	}
	if !contains(aiCannedCategories, d.Category) {
		d.Category = "general"
	}
	if len(d.Buttons) > 0 && utf8.RuneCountInString(d.Content) > aiCannedMaxButtonContent {
		d.Buttons = []CannedResponseButton{}
	}
	if d.Name == "" {
		d.Name = truncateRunes(strings.Split(d.Content, "\n")[0], 40)
	}
	return d, nil
}

func sanitizeCannedShortcut(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimLeft(s, "/")
	s = strings.ReplaceAll(s, " ", "-")
	s = aiCannedShortcutInvalid.ReplaceAllString(s, "")
	s = strings.Trim(s, "-_")
	return truncateRunes(s, aiCannedMaxShortcut)
}

// sanitizeAICannedButtons keeps only a sendable combination: up to 3 reply
// buttons, or a single URL button when there are no replies.
func sanitizeAICannedButtons(raw any) []CannedResponseButton {
	list, _ := raw.([]any)
	var replies []CannedResponseButton
	var link *CannedResponseButton
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title := truncateAtWord(strings.TrimSpace(stringField(m, "title")), maxButtonTitleRunes)
		if title == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringField(m, "type"))) {
		case "url":
			u := strings.TrimSpace(stringField(m, "url"))
			if link == nil && isSendableButtonURL(u) {
				link = &CannedResponseButton{Title: title, Type: "url", URL: u}
			}
		case "", "reply":
			if len(replies) < 3 {
				replies = append(replies, CannedResponseButton{Title: title, Type: "reply"})
			}
		}
	}
	out := replies
	if len(out) == 0 && link != nil {
		out = []CannedResponseButton{*link}
	}
	for i := range out {
		out[i].ID = "btn_" + strconv.Itoa(i+1)
	}
	if out == nil {
		out = []CannedResponseButton{}
	}
	return out
}

// truncateAtWord caps s at n runes, cutting at the last space when that keeps
// at least half of the allowed length.
func truncateAtWord(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := []rune(s)[:n]
	if i := strings.LastIndex(string(cut), " "); i >= 0 && utf8.RuneCountInString(string(cut)[:i]) >= n/2 {
		return strings.TrimSpace(string(cut)[:i])
	}
	return string(cut)
}

// uniqueCannedNameAndShortcut appends a number when the org already uses the
// name or shortcut, so the draft saves without a duplicate-name conflict.
func uniqueCannedNameAndShortcut(name, shortcut string, existing []models.CannedResponse) (string, string) {
	names := map[string]bool{}
	shortcuts := map[string]bool{}
	for _, cr := range existing {
		names[strings.ToLower(cr.Name)] = true
		if cr.Shortcut != "" {
			shortcuts[strings.ToLower(cr.Shortcut)] = true
		}
	}
	if names[strings.ToLower(name)] {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s (%d)", truncateRunes(name, 94), i)
			if !names[strings.ToLower(candidate)] {
				name = candidate
				break
			}
		}
	}
	if shortcut != "" && shortcuts[shortcut] {
		for i := 2; ; i++ {
			candidate := truncateRunes(shortcut, aiCannedMaxShortcut-3) + strconv.Itoa(i)
			if !shortcuts[candidate] {
				shortcut = candidate
				break
			}
		}
	}
	return name, shortcut
}
