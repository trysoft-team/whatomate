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

// Meta limits for template components.
const (
	aiTemplateMaxBody       = 1024
	aiTemplateMaxHeader     = 60
	aiTemplateMaxFooter     = 60
	aiTemplateMaxButtonText = 25
	aiTemplateMaxButtons    = 3 // the template editor caps buttons at 3
	aiTemplateMaxName       = 60
	aiTemplateExistingLimit = 200
)

// aiTemplateLanguages matches the language picker in the template editor.
var aiTemplateLanguages = []string{
	"en", "en_GB", "en_US", "es", "es_AR", "es_MX", "pt_BR", "pt_PT", "hi", "ta", "te", "kn", "ml", "mr", "gu",
	"bn", "pa", "ur", "ar", "fr", "de", "it", "nl", "ja", "ko", "zh_CN", "zh_HK", "zh_TW", "ru", "tr", "id",
	"ms", "th", "vi", "sw", "fil", "pl", "uk", "ro", "sv", "da", "fi", "he", "fa", "af", "zu",
}

var (
	aiTemplateParamRe    = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
	aiTemplateAdjacentRe = regexp.MustCompile(`\}\}\s*\{\{`)
	aiTemplatePhoneRe    = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{5,19}$`)
)

const aiTemplateSystemPrompt = `You write WhatsApp Business message templates for Whatomate. Templates are reviewed by Meta, so they must follow Meta's template guidelines.

Return ONLY valid JSON (no markdown fences, no commentary) with this shape:
{
  "name": "order_shipped",
  "display_name": "Order shipped",
  "category": "UTILITY",
  "language": "en",
  "header_type": "TEXT",
  "header_content": "Your order is on the way",
  "body_content": "Hi {{1}}, your order {{2}} has shipped and should arrive by {{3}}. Reply here if you have any questions.",
  "body_examples": ["Maria", "#48213", "Friday, 3 October"],
  "header_examples": [],
  "footer_content": "Thanks for shopping with us",
  "buttons": [{"type": "URL", "text": "Track order", "url": "https://your-site.example.com/track/{{1}}", "example": "https://your-site.example.com/track/48213"}],
  "notes": "One short sentence telling the admin what to check before creating it (e.g. replace the tracking URL). Empty if nothing."
}

Rules:
- category: UTILITY for transactional updates the customer expects (orders, bookings, account, payments, reminders); MARKETING for promotions, offers, newsletters, re-engagement; AUTHENTICATION only for one-time passcodes (then leave body, header, footer and buttons empty — Meta uses fixed text).
- name: lowercase letters, digits and underscores only, max 60 chars, descriptive.
- language: one of en, en_GB, en_US, es, es_AR, es_MX, pt_BR, pt_PT, hi, ta, te, kn, ml, mr, gu, bn, pa, ur, ar, fr, de, it, nl, ja, ko, zh_CN, zh_HK, zh_TW, ru, tr, id, ms, th, vi, sw, fil, pl, uk, ro, sv, da, fi, he, fa, af, zu. Write the text in that language; pick the language of the request unless it asks for another.
- header_type: NONE, TEXT, IMAGE, VIDEO or DOCUMENT. TEXT headers are max 60 chars with at most one variable {{1}}. Use IMAGE/VIDEO/DOCUMENT only when the request mentions media (the admin uploads the sample).
- body_content: max 1024 chars, friendly and clear. Variables are positional {{1}}, {{2}}, … numbered in order of appearance, each used once. A variable must not be the very first or very last thing in the body, and never put two variables next to each other. Don't use too many variables relative to the text. WhatsApp formatting only: *bold*, _italic_, line breaks. No URLs in the body if a URL button fits better.
- body_examples / header_examples: one realistic sample value per variable, in order.
- footer_content: optional, max 60 chars, no variables.
- buttons: optional, max 3. Types: QUICK_REPLY {"text"}, URL {"text","url"} (https; a variable is only allowed at the very end of the URL as {{1}}, then include "example" with a full sample URL), PHONE_NUMBER {"text","phone_number"} in international format, COPY_CODE {"example"} for a coupon code (MARKETING only). Button text max 25 chars. MARKETING templates should offer an opt-out quick reply such as "Stop promotions".
- Never invent real prices, discount codes, phone numbers or URLs. Use variables or obvious placeholders such as https://your-site.example.com and mention them in notes.`

// GenerateTemplate drafts a WhatsApp message template from a plain-language
// description. The draft is not saved or submitted to Meta; the page opens it
// in the template editor.
func (a *App) GenerateTemplate(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceTemplates, models.ActionWrite)
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

	settings, errMsg := a.chatAISettings(orgID, "")
	if errMsg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errMsg, nil, "")
	}

	var existing []string
	q := a.DB.Model(&models.Template{}).Where("organization_id = ?", orgID)
	if req.WhatsAppAccount != "" {
		q = q.Where("whats_app_account = ?", req.WhatsAppAccount)
	}
	q.Order("updated_at DESC").Limit(aiTemplateExistingLimit).Pluck("name", &existing)

	var userMsg strings.Builder
	var org models.Organization
	if a.DB.Select("name").Where("id = ?", orgID).First(&org).Error == nil && org.Name != "" {
		fmt.Fprintf(&userMsg, "Business name: %s\n", org.Name)
	}
	if len(existing) > 0 {
		fmt.Fprintf(&userMsg, "Existing template names (do not reuse): %s\n", strings.Join(existing, ", "))
	}
	userMsg.WriteString("\nWrite a WhatsApp message template for this request:\n\n")
	userMsg.WriteString(prompt)

	raw, err := a.completeAIChat(settings, aiTemplateSystemPrompt, userMsg.String(),
		aiFlowGenerateMaxTokens, models.AIUsagePurposeTemplateGenerate)
	if err != nil {
		a.Log.Error("GenerateTemplate: LLM call failed", "error", err, "provider", settings.AI.Provider)
		if errors.Is(err, errAIQuotaExceeded) {
			return r.SendErrorEnvelope(fasthttp.StatusTooManyRequests, aiQuotaMessage(settings), nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI generation failed: "+err.Error(), nil, "")
	}

	draft, err := parseAITemplateDraft(raw)
	if err != nil {
		a.Log.Error("GenerateTemplate: parse draft", "error", err, "raw_len", len(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an invalid template. Try again with a clearer description.", nil, "")
	}
	draft.Name = uniqueTemplateName(draft.Name, existing)
	draft.WhatsAppAccount = req.WhatsAppAccount
	return r.SendEnvelope(draft)
}

// aiTemplateDraft mirrors the template editor form.
type aiTemplateDraft struct {
	WhatsAppAccount           string           `json:"whatsapp_account"`
	Name                      string           `json:"name"`
	DisplayName               string           `json:"display_name"`
	Language                  string           `json:"language"`
	Category                  string           `json:"category"`
	HeaderType                string           `json:"header_type"`
	HeaderContent             string           `json:"header_content"`
	BodyContent               string           `json:"body_content"`
	FooterContent             string           `json:"footer_content"`
	Buttons                   []map[string]any `json:"buttons"`
	SampleValues              []map[string]any `json:"sample_values"`
	AddSecurityRecommendation bool             `json:"add_security_recommendation"`
	CodeExpirationMinutes     int              `json:"code_expiration_minutes"`
	Notes                     string           `json:"notes"`
}

func parseAITemplateDraft(raw string) (*aiTemplateDraft, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(cleaned), &in); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	if inner, ok := in["template"].(map[string]any); ok {
		in = inner
	}

	d := &aiTemplateDraft{
		DisplayName:  truncateRunes(strings.TrimSpace(stringField(in, "display_name")), 100),
		Category:     strings.ToUpper(strings.TrimSpace(stringField(in, "category"))),
		Language:     strings.TrimSpace(stringField(in, "language")),
		HeaderType:   strings.ToUpper(strings.TrimSpace(stringField(in, "header_type"))),
		Buttons:      []map[string]any{},
		SampleValues: []map[string]any{},
		Notes:        truncateRunes(strings.TrimSpace(stringField(in, "notes")), 300),
	}
	d.Name = truncateRunes(strings.Trim(normalizeTemplateName(stringField(in, "name")), "_"), aiTemplateMaxName)
	if d.Name == "" {
		d.Name = strings.Trim(normalizeTemplateName(d.DisplayName), "_")
	}
	if d.Name == "" {
		d.Name = "ai_template"
	}
	if d.DisplayName == "" {
		d.DisplayName = strings.ReplaceAll(d.Name, "_", " ")
	}
	d.Language = matchTemplateLanguage(d.Language)
	if d.Category != "MARKETING" && d.Category != "AUTHENTICATION" {
		d.Category = "UTILITY"
	}

	if d.Category == "AUTHENTICATION" {
		d.HeaderType = "NONE"
		d.AddSecurityRecommendation, _ = in["add_security_recommendation"].(bool)
		if n, ok := in["code_expiration_minutes"].(float64); ok && n >= 1 && n <= 90 {
			d.CodeExpirationMinutes = int(n)
		}
		d.Buttons = []map[string]any{{"type": "OTP", "text": "Copy code", "otp_type": "COPY_CODE"}}
		return d, nil
	}

	body, bodyCount, err := renumberTemplateParams(strings.TrimSpace(stringField(in, "body_content")))
	if err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	if body == "" {
		return nil, fmt.Errorf("body is empty")
	}
	if utf8.RuneCountInString(body) > aiTemplateMaxBody {
		return nil, fmt.Errorf("body exceeds %d characters", aiTemplateMaxBody)
	}
	if strings.HasPrefix(body, "{{") {
		return nil, fmt.Errorf("body starts with a variable")
	}
	if strings.HasSuffix(body, "}}") {
		body += "."
	}
	d.BodyContent = body
	d.SampleValues = append(d.SampleValues, templateSamples("body", in["body_examples"], bodyCount)...)

	switch d.HeaderType {
	case "TEXT":
		header, headerCount, err := renumberTemplateParams(strings.TrimSpace(stringField(in, "header_content")))
		if err != nil || header == "" || headerCount > 1 || utf8.RuneCountInString(header) > aiTemplateMaxHeader {
			d.HeaderType = "NONE"
			break
		}
		d.HeaderContent = header
		d.SampleValues = append(d.SampleValues, templateSamples("header", in["header_examples"], headerCount)...)
	case "IMAGE", "VIDEO", "DOCUMENT":
	default:
		d.HeaderType = "NONE"
	}

	if footer := strings.TrimSpace(stringField(in, "footer_content")); footer != "" && !aiTemplateParamRe.MatchString(footer) {
		d.FooterContent = truncateRunes(footer, aiTemplateMaxFooter)
	}

	d.Buttons = sanitizeAITemplateButtons(in["buttons"], d.Category)
	return d, nil
}

// renumberTemplateParams rewrites variables as {{1}}, {{2}}, … in order of
// appearance. Repeated or adjacent variables are rejected because Meta refuses them.
func renumberTemplateParams(s string) (string, int, error) {
	seen := map[string]bool{}
	n := 0
	var dupErr error
	out := aiTemplateParamRe.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSpace(aiTemplateParamRe.FindStringSubmatch(m)[1])
		if seen[name] {
			dupErr = fmt.Errorf("variable %q is used more than once", name)
		}
		seen[name] = true
		n++
		return "{{" + strconv.Itoa(n) + "}}"
	})
	if dupErr != nil {
		return "", 0, dupErr
	}
	if aiTemplateAdjacentRe.MatchString(out) {
		return "", 0, fmt.Errorf("adjacent variables")
	}
	return out, n, nil
}

func templateSamples(component string, raw any, count int) []map[string]any {
	examples, _ := raw.([]any)
	out := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		value := ""
		if i < len(examples) {
			value = truncateRunes(strings.TrimSpace(scalarString(examples[i])), 200)
		}
		if value == "" {
			value = "sample " + strconv.Itoa(i+1)
		}
		out = append(out, map[string]any{"component": component, "index": i + 1, "value": value})
	}
	return out
}

func sanitizeAITemplateButtons(raw any, category string) []map[string]any {
	list, _ := raw.([]any)
	out := []map[string]any{}
	for _, item := range list {
		if len(out) >= aiTemplateMaxButtons {
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text := truncateAtWord(strings.TrimSpace(stringField(m, "text")), aiTemplateMaxButtonText)
		switch strings.ToUpper(strings.TrimSpace(stringField(m, "type"))) {
		case "QUICK_REPLY":
			if text != "" {
				out = append(out, map[string]any{"type": "QUICK_REPLY", "text": text})
			}
		case "URL":
			u, count, err := renumberTemplateParams(strings.TrimSpace(stringField(m, "url")))
			if err != nil || text == "" || count > 1 || !isHTTPURLTemplate(u) || (count == 1 && !strings.HasSuffix(u, "{{1}}")) {
				continue
			}
			b := map[string]any{"type": "URL", "text": text, "url": u}
			if count == 1 {
				example := strings.TrimSpace(stringField(m, "example"))
				if !isSendableButtonURL(example) {
					example = strings.TrimSuffix(u, "{{1}}") + "123"
				}
				b["example"] = example
			}
			out = append(out, b)
		case "PHONE_NUMBER":
			phone := strings.TrimSpace(stringField(m, "phone_number"))
			if text != "" && aiTemplatePhoneRe.MatchString(phone) {
				out = append(out, map[string]any{"type": "PHONE_NUMBER", "text": text, "phone_number": phone})
			}
		case "COPY_CODE":
			example := strings.TrimSpace(stringField(m, "example"))
			if category == "MARKETING" && example != "" {
				out = append(out, map[string]any{"type": "COPY_CODE", "text": "Copy offer code", "example": truncateRunes(example, 15)})
			}
		}
	}
	return out
}

func matchTemplateLanguage(code string) string {
	code = strings.ReplaceAll(strings.TrimSpace(code), "-", "_")
	for _, l := range aiTemplateLanguages {
		if strings.EqualFold(l, code) {
			return l
		}
	}
	if base, _, ok := strings.Cut(code, "_"); ok {
		return matchTemplateLanguage(base)
	}
	return "en"
}

// uniqueTemplateName appends _2, _3, … when the name is already taken.
func uniqueTemplateName(name string, existing []string) string {
	taken := map[string]bool{}
	for _, n := range existing {
		taken[n] = true
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		candidate := truncateRunes(name, aiTemplateMaxName-len(suffix)) + suffix
		if !taken[candidate] {
			return candidate
		}
	}
}
