package handlers

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	aiWAFlowMaxScreens    = 8
	aiWAFlowMaxComponents = 20
	aiWAFlowMaxOptions    = 10
)

var aiWAFlowCategories = map[string]bool{
	"SIGN_UP": true, "SIGN_IN": true, "APPOINTMENT_BOOKING": true, "LEAD_GENERATION": true,
	"CONTACT_US": true, "CUSTOMER_SUPPORT": true, "SURVEY": true, "OTHER": true,
}

// aiWAFlowTextLimits caps the text property per component type (Meta's limits).
var aiWAFlowTextLimits = map[string]int{
	"TextHeading":    80,
	"TextSubheading": 80,
	"TextBody":       4096,
	"TextCaption":    409,
}

// aiWAFlowLabelLimits caps the label property per component type (Meta's limits).
var aiWAFlowLabelLimits = map[string]int{
	"TextInput":         20,
	"TextArea":          20,
	"Dropdown":          20,
	"RadioButtonsGroup": 30,
	"CheckboxGroup":     30,
	"DatePicker":        40,
	"OptIn":             120,
	"PhotoPicker":       30,
	"DocumentPicker":    30,
	"Footer":            35,
}

var aiWAFlowInputTypes = map[string]bool{
	"text": true, "number": true, "email": true, "password": true, "passcode": true, "phone": true,
}

const aiWAFlowGenerateSystemPrompt = `You design WhatsApp Flows (Meta Flow JSON forms shown inside WhatsApp) for Whatomate.

Return ONLY valid JSON (no markdown fences, no commentary) with this shape:
{
  "name": "short flow name",
  "category": "LEAD_GENERATION",
  "screens": [
    {
      "id": "WELCOME",
      "title": "Welcome",
      "layout": {
        "type": "SingleColumnLayout",
        "children": [
          {"type": "TextHeading", "text": "Book a demo"},
          {"type": "TextInput", "name": "full_name", "label": "Full name", "input-type": "text", "required": true},
          {"type": "Footer", "label": "Continue", "on-click-action": {"name": "navigate", "next": {"type": "screen", "name": "DETAILS"}, "payload": {}}}
        ]
      }
    }
  ]
}

Rules:
- category is one of: SIGN_UP, SIGN_IN, APPOINTMENT_BOOKING, LEAD_GENERATION, CONTACT_US, CUSTOMER_SUPPORT, SURVEY, OTHER.
- Screen ids: UPPERCASE letters and underscores only, unique, never "SUCCESS". Titles are short (a few words).
- Use 1–5 screens. Each screen has 1–10 components and EXACTLY ONE Footer, as its last child.
- Footer on every screen except the last: {"name":"navigate","next":{"type":"screen","name":"<NEXT_SCREEN_ID>"},"payload":{}}.
- Footer on the last screen: {"name":"complete","payload":{}}. Payloads are filled in automatically — leave them empty.
- Allowed component types ONLY: TextHeading, TextSubheading, TextBody, TextCaption, TextInput, TextArea, Dropdown, RadioButtonsGroup, CheckboxGroup, DatePicker, OptIn, PhotoPicker, DocumentPicker, Footer.
- Text components: {"type":"TextBody","text":"…"} (TextHeading/TextSubheading ≤ 80 chars).
- Input components need a unique snake_case "name" across the whole flow and a "label".
- TextInput: "input-type" is one of text, number, email, password, passcode, phone. Label ≤ 20 chars.
- TextArea: label ≤ 20 chars.
- Dropdown / RadioButtonsGroup / CheckboxGroup: "data-source": [{"id":"snake_case_id","title":"Option"}] with 2–10 options, titles ≤ 30 chars. Dropdown label ≤ 20 chars, others ≤ 30.
- DatePicker: {"type":"DatePicker","name":"preferred_date","label":"Preferred date"}.
- OptIn: {"type":"OptIn","name":"accept_terms","label":"I agree to the terms","required":true}.
- PhotoPicker / DocumentPicker: at most one per screen, never both on one screen, set "max-uploaded-photos" / "max-uploaded-documents" to 1.
- Footer label ≤ 35 chars.
- Set "required": true on inputs the business needs. Prefer short, friendly WhatsApp copy. Do not invent URLs, images or IDs you were not given.`

// GenerateWhatsAppFlow drafts WhatsApp Flow screens from a plain-language
// description using the org's AI settings.
func (a *App) GenerateWhatsAppFlow(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceFlowsWhatsApp, models.ActionWrite)
	if err != nil {
		return nil
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
		a.Log.Error("GenerateWhatsAppFlow: load settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load AI settings", nil, "")
	}
	if !settings.AI.Enabled || settings.AI.Provider == "" || settings.AI.APIKey == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"AI is not configured. Enable AI and add an API key in Settings → AI.", nil, "")
	}
	if settings.AI.Model == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "AI model is not configured in Settings → AI", nil, "")
	}

	userMsg := "Design a WhatsApp Flow for this use case:\n\n" + prompt
	raw, err := a.completeAIChat(settings, aiWAFlowGenerateSystemPrompt, userMsg, aiFlowGenerateMaxTokens, models.AIUsagePurposeFlowGenerate)
	if err != nil {
		a.Log.Error("GenerateWhatsAppFlow: LLM call failed", "error", err, "provider", settings.AI.Provider)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI generation failed: "+err.Error(), nil, "")
	}

	draft, err := parseAIWhatsAppFlowDraft(raw)
	if err != nil {
		a.Log.Error("GenerateWhatsAppFlow: parse draft", "error", err, "raw_len", len(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an invalid flow. Try again with a clearer description.", nil, "")
	}
	return r.SendEnvelope(draft)
}

// aiWhatsAppFlowDraft is what the builder receives.
type aiWhatsAppFlowDraft struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Screens  []any  `json:"screens"`
}

func parseAIWhatsAppFlowDraft(raw string) (*aiWhatsAppFlowDraft, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var in struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Screens  []any  `json:"screens"`
		FlowJSON *struct {
			Screens []any `json:"screens"`
		} `json:"flow_json"`
	}
	if err := json.Unmarshal([]byte(cleaned), &in); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	if len(in.Screens) == 0 && in.FlowJSON != nil {
		in.Screens = in.FlowJSON.Screens
	}
	screens := sanitizeAIWhatsAppFlowScreens(in.Screens)
	if len(screens) == 0 {
		return nil, fmt.Errorf("no usable screens")
	}
	if err := validateFlowStructure(screens); err != nil {
		return nil, fmt.Errorf("invalid flow: %w", err)
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "AI draft"
	}
	name = truncateRunes(name, 100)
	category := strings.ToUpper(strings.TrimSpace(in.Category))
	if !aiWAFlowCategories[category] {
		category = "OTHER"
	}
	return &aiWhatsAppFlowDraft{Name: name, Category: category, Screens: screens}, nil
}

// sanitizeAIWhatsAppFlowScreens turns LLM output into screens the builder and
// Meta accept without an endpoint: known components, unique ids and field
// names, one Footer per screen that navigates forward, and Complete on the last.
func sanitizeAIWhatsAppFlowScreens(raw []any) []any {
	type draftScreen struct {
		id, title  string
		components []map[string]any
		footer     map[string]any
	}

	var drafts []*draftScreen
	screenIDs := map[string]bool{}
	fieldNames := map[string]bool{}

	for _, s := range raw {
		if len(drafts) >= aiWAFlowMaxScreens {
			break
		}
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		id := uniqueName(aiScreenID(stringField(sm, "id"), len(drafts)+1), screenIDs)
		title := strings.TrimSpace(stringField(sm, "title"))
		if title == "" {
			title = fmt.Sprintf("Screen %d", len(drafts)+1)
		}
		ds := &draftScreen{id: id, title: truncateRunes(title, 30)}

		mediaOnScreen := false
		for _, c := range screenChildren(sm) {
			comp, ok := c.(map[string]any)
			if !ok {
				continue
			}
			typ := stringField(comp, "type")
			if typ == "Footer" {
				if ds.footer == nil {
					ds.footer = comp
				}
				continue
			}
			if len(ds.components) >= aiWAFlowMaxComponents {
				continue
			}
			if typ == "PhotoPicker" || typ == "DocumentPicker" {
				if mediaOnScreen {
					continue
				}
				mediaOnScreen = true
			}
			if clean := sanitizeAIFlowComponent(typ, comp, fieldNames); clean != nil {
				ds.components = append(ds.components, clean)
			}
		}
		if len(ds.components) == 0 {
			continue
		}
		screenIDs[id] = true
		drafts = append(drafts, ds)
	}

	out := make([]any, 0, len(drafts))
	for i, ds := range drafts {
		label := "Continue"
		if i == len(drafts)-1 {
			label = "Submit"
		}
		if ds.footer != nil {
			if l := strings.TrimSpace(stringField(ds.footer, "label")); l != "" {
				label = l
			}
		}
		var action map[string]any
		if i == len(drafts)-1 {
			action = map[string]any{"name": "complete", "payload": map[string]any{}}
		} else {
			next := drafts[i+1].id
			if target := aiFooterTarget(ds.footer); target != "" && target != ds.id && screenIDs[target] {
				next = target
			}
			action = map[string]any{
				"name":    "navigate",
				"next":    map[string]any{"type": "screen", "name": next},
				"payload": map[string]any{},
			}
		}
		children := make([]any, 0, len(ds.components)+1)
		for _, c := range ds.components {
			children = append(children, c)
		}
		children = append(children, map[string]any{
			"id":              aiComponentID(),
			"type":            "Footer",
			"label":           truncateRunes(label, aiWAFlowLabelLimits["Footer"]),
			"on-click-action": action,
		})
		out = append(out, map[string]any{
			"id":    ds.id,
			"title": ds.title,
			"data":  map[string]any{},
			"layout": map[string]any{
				"type":     "SingleColumnLayout",
				"children": children,
			},
		})
	}
	return out
}

// sanitizeAIFlowComponent returns a builder-ready component, or nil if the
// type is unsupported or unusable.
func sanitizeAIFlowComponent(typ string, comp map[string]any, fieldNames map[string]bool) map[string]any {
	out := map[string]any{"id": aiComponentID(), "type": typ}

	if limit, ok := aiWAFlowTextLimits[typ]; ok {
		text := strings.TrimSpace(stringField(comp, "text"))
		if text == "" {
			return nil
		}
		out["text"] = truncateRunes(text, limit)
		if typ == "TextBody" || typ == "TextCaption" {
			out["markdown"] = false
		}
		return out
	}

	labelLimit, ok := aiWAFlowLabelLimits[typ]
	if !ok || typ == "Footer" {
		return nil
	}
	label := strings.TrimSpace(stringField(comp, "label"))
	if label == "" {
		return nil
	}
	out["label"] = truncateRunes(label, labelLimit)
	out["name"] = uniqueName(aiFieldName(stringField(comp, "name"), typ), fieldNames)
	fieldNames[out["name"].(string)] = true
	required, _ := comp["required"].(bool)
	out["required"] = required

	switch typ {
	case "TextInput":
		it := strings.ToLower(stringField(comp, "input-type"))
		if !aiWAFlowInputTypes[it] {
			it = "text"
		}
		out["input-type"] = it
	case "Dropdown", "RadioButtonsGroup", "CheckboxGroup":
		opts := sanitizeAIFlowOptions(comp["data-source"])
		if len(opts) == 0 {
			return nil
		}
		out["data-source"] = opts
	case "PhotoPicker":
		out["photo-source"] = "camera_gallery"
		out["min-uploaded-photos"] = 0
		if required {
			out["min-uploaded-photos"] = 1
		}
		out["max-uploaded-photos"] = 1
		delete(out, "required")
	case "DocumentPicker":
		out["min-uploaded-documents"] = 0
		if required {
			out["min-uploaded-documents"] = 1
		}
		out["max-uploaded-documents"] = 1
		delete(out, "required")
	}
	if desc := strings.TrimSpace(stringField(comp, "description")); desc != "" && (typ == "PhotoPicker" || typ == "DocumentPicker") {
		out["description"] = truncateRunes(desc, 300)
	}
	return out
}

func sanitizeAIFlowOptions(v any) []any {
	list, _ := v.([]any)
	ids := map[string]bool{}
	out := make([]any, 0, len(list))
	for _, item := range list {
		if len(out) >= aiWAFlowMaxOptions {
			break
		}
		var id, title string
		switch o := item.(type) {
		case map[string]any:
			id, title = stringField(o, "id"), strings.TrimSpace(stringField(o, "title"))
		case string:
			title = strings.TrimSpace(o)
		}
		if title == "" {
			continue
		}
		id = uniqueName(aiFieldName(firstNonEmpty(id, title), "option"), ids)
		ids[id] = true
		out = append(out, map[string]any{"id": id, "title": truncateRunes(title, 30)})
	}
	return out
}

func aiFooterTarget(footer map[string]any) string {
	if footer == nil {
		return ""
	}
	action, _ := footer["on-click-action"].(map[string]any)
	next, _ := action["next"].(map[string]any)
	name := stringField(next, "name")
	if name == "" {
		return ""
	}
	return aiScreenID(name, 0)
}

// aiScreenID normalizes to UPPER_SNAKE letters (Meta rejects digits), falling
// back to SCREEN_A, SCREEN_B, … by position.
func aiScreenID(id string, position int) string {
	id = strings.ToUpper(sanitizeID(strings.ReplaceAll(strings.TrimSpace(id), " ", "_")))
	id = strings.Trim(id, "_")
	if id == "SUCCESS" {
		id = "SUCCESS_SCREEN"
	}
	if id == "" && position > 0 {
		id = "SCREEN_" + numberToLetters(position)
	}
	return id
}

// aiFieldName normalizes to lower_snake letters, falling back to the type.
func aiFieldName(name, fallback string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r == ' ' || r == '-' || r == '.':
			return '_'
		}
		return -1
	}, name)
	name = strings.Trim(sanitizeID(name), "_")
	if name == "" {
		name = strings.ToLower(sanitizeID(fallback))
	}
	return strings.ToLower(name)
}

// uniqueName appends _b, _c, … (letters only, as Meta requires) until unused.
func uniqueName(name string, used map[string]bool) string {
	if !used[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := name + "_" + strings.ToLower(numberToLetters(i))
		if !used[candidate] {
			return candidate
		}
	}
}

func numberToLetters(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

// aiComponentID matches the builder's id_xxxxxxxxxxxx component ids.
func aiComponentID() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 12)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			b[i] = letters[i%len(letters)]
			continue
		}
		b[i] = letters[n.Int64()]
	}
	return "id_" + string(b)
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
