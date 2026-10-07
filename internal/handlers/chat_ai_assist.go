package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

const (
	chatAIHistoryLimit    = 30
	chatAIMessageMaxRunes = 600
	chatAIDraftMaxRunes   = 4000
	chatAIMaxTokens       = 4096 // reasoning models spend part of this before answering
	chatAIMaxTemplates    = 40
	chatAIMaxTagOptions   = 100
)

// Chat assist modes.
const (
	chatAIModeSuggestReply = "suggest_reply"
	chatAIModeRewrite      = "rewrite"
	chatAIModeSummarize    = "summarize"
	chatAIModeTranslate    = "translate"
	chatAIModeQuickReplies = "quick_replies"
	chatAIModeSuggestTags  = "suggest_tags_fields"
	chatAIModeTemplate     = "suggest_template"
)

var chatAIRewriteTones = map[string]string{
	"friendly":     "Make it warmer and friendlier while keeping the meaning.",
	"professional": "Make it polite, clear and professional.",
	"shorter":      "Make it noticeably shorter; keep only what matters.",
	"grammar":      "Fix spelling, grammar and punctuation only. Do not change tone or meaning.",
	"whatsapp":     "Format it for WhatsApp: short paragraphs, *bold* for key facts, bullet lines starting with • where it helps. Do not add new content.",
}

// chatAIContactFields are the contact fields the AI may propose values for.
// profile_name is a column; the rest live in contact metadata.
var chatAIContactFields = []string{"profile_name", "email", "company", "city", "language"}

var chatAITemplateVarRe = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

type chatAIAssistRequest struct {
	Mode           string `json:"mode"`
	Draft          string `json:"draft"`
	Tone           string `json:"tone"`
	Text           string `json:"text"`
	TargetLanguage string `json:"target_language"` // a language name, or "customer" for the customer's language
	MessageID      string `json:"message_id"`
}

// ChatAIAssist runs one AI helper for the agent on a conversation.
func (a *App) ChatAIAssist(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	var req chatAIAssistRequest
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
	}
	req.Mode = strings.TrimSpace(req.Mode)
	if utf8.RuneCountInString(req.Draft) > chatAIDraftMaxRunes || utf8.RuneCountInString(req.Text) > chatAIDraftMaxRunes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "text is too long", nil, "")
	}

	var contact models.Contact
	q := a.scopeAssignedContact(a.DB.Where("id = ? AND organization_id = ?", contactID, orgID), userID, orgID)
	if err := q.First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	settings, errMsg := a.chatAISettings(orgID, contact.WhatsAppAccount)
	if errMsg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errMsg, nil, "")
	}

	var agent models.User
	a.DB.Select("id", "full_name").Where("id = ?", userID).First(&agent)

	var result any
	switch req.Mode {
	case chatAIModeSuggestReply:
		result, err = a.chatAISuggestReply(settings, &contact, agent.FullName)
	case chatAIModeRewrite:
		result, err = a.chatAIRewrite(settings, &contact, req)
	case chatAIModeSummarize:
		result, err = a.chatAISummarize(settings, &contact)
	case chatAIModeTranslate:
		result, err = a.chatAITranslate(settings, &contact, req)
	case chatAIModeQuickReplies:
		result, err = a.chatAIQuickReplies(settings, &contact, agent.FullName)
	case chatAIModeSuggestTags:
		result, err = a.chatAISuggestTagsFields(settings, &contact)
	case chatAIModeTemplate:
		result, err = a.chatAISuggestTemplate(settings, &contact)
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unknown mode", nil, "")
	}
	if err != nil {
		if ue, ok := err.(chatAIUserError); ok {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, string(ue), nil, "")
		}
		a.Log.Error("ChatAIAssist failed", "mode", req.Mode, "error", err, "provider", settings.AI.Provider)
		if errors.Is(err, errAIQuotaExceeded) {
			return r.SendErrorEnvelope(fasthttp.StatusTooManyRequests, aiQuotaMessage(settings), nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI request failed: "+err.Error(), nil, "")
	}
	return r.SendEnvelope(result)
}

// chatAIUserError is a request problem shown to the agent as-is (400).
type chatAIUserError string

func (e chatAIUserError) Error() string { return string(e) }

// chatAISettings loads decrypted AI settings and returns a user-facing
// message when AI is not usable.
func (a *App) chatAISettings(orgID uuid.UUID, account string) (*models.ChatbotSettings, string) {
	settings, err := a.getChatbotSettingsCached(orgID, account)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "Failed to load AI settings"
	}
	if settings == nil || !settings.AI.Enabled || settings.AI.Provider == "" || settings.AI.APIKey == "" {
		return nil, "AI is not configured. Enable AI and add an API key in Settings → AI."
	}
	if settings.AI.Model == "" {
		return nil, "AI model is not configured in Settings → AI"
	}
	return settings, ""
}

// chatAIConversation renders the latest messages oldest-first as a transcript.
func (a *App) chatAIConversation(contact *models.Contact, limit int) (string, int) {
	var msgs []models.Message
	a.DB.Where("contact_id = ? AND organization_id = ?", contact.ID, contact.OrganizationID).
		Order("created_at DESC").Limit(limit).Find(&msgs)
	if len(msgs) == 0 {
		return "", 0
	}

	userIDs := map[uuid.UUID]bool{}
	for _, m := range msgs {
		if m.SentByUserID != nil {
			userIDs[*m.SentByUserID] = true
		}
	}
	names := map[uuid.UUID]string{}
	if len(userIDs) > 0 {
		ids := make([]uuid.UUID, 0, len(userIDs))
		for id := range userIDs {
			ids = append(ids, id)
		}
		var users []models.User
		a.DB.Select("id", "full_name").Where("id IN ?", ids).Find(&users)
		for _, u := range users {
			names[u.ID] = u.FullName
		}
	}

	customer := strings.TrimSpace(contact.ProfileName)
	if customer == "" {
		customer = "Customer"
	}
	var b strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		speaker := "Customer (" + customer + ")"
		if m.Direction == models.DirectionOutgoing {
			speaker = "Business (bot)"
			if m.SentByUserID != nil {
				speaker = "Agent"
				if n := names[*m.SentByUserID]; n != "" {
					speaker = "Agent " + n
				}
			}
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", m.CreatedAt.Format("Jan 2 15:04"), speaker, chatAIMessageText(m))
	}
	return b.String(), len(msgs)
}

// chatAIMessageText is a one-line text rendering of any message type.
func chatAIMessageText(m models.Message) string {
	content := strings.TrimSpace(m.Content)
	transcript, _ := m.Metadata["transcript"].(string)
	var text string
	switch m.MessageType {
	case models.MessageTypeText, models.MessageTypeInteractive, models.MessageTypeFlow:
		text = content
	case models.MessageTypeTemplate:
		text = "[Template " + m.TemplateName + "] " + content
	case models.MessageTypeAudio:
		if transcript != "" {
			text = "[Voice note, transcribed] " + transcript
		} else {
			text = "[Voice note]"
		}
	case models.MessageTypeImage, models.MessageTypeVideo, models.MessageTypeDocument:
		text = "[" + string(m.MessageType) + "]"
		if content != "" {
			text += " " + content
		}
	default:
		text = content
		if text == "" {
			text = "[" + string(m.MessageType) + "]"
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	return truncateRunes(text, chatAIMessageMaxRunes)
}

// chatAIKnowledge returns the org's static AI contexts (no external API calls).
func (a *App) chatAIKnowledge(orgID uuid.UUID, account string) string {
	contexts, err := a.getAIContextsCached(orgID, account)
	if err != nil {
		return ""
	}
	var parts []string
	for _, c := range contexts {
		if c.StaticContent != "" {
			parts = append(parts, "### "+c.Name+"\n"+truncateRunes(c.StaticContent, 4000))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Business knowledge:\n" + strings.Join(parts, "\n\n")
}

func (a *App) chatAIReplySystemPrompt(settings *models.ChatbotSettings, contact *models.Contact, agentName string) string {
	var b strings.Builder
	b.WriteString("You help a customer support agent reply to a customer on WhatsApp.\n")
	if agentName != "" {
		fmt.Fprintf(&b, "The agent's name is %s.\n", agentName)
	}
	b.WriteString(`Rules:
- Reply in the customer's language (the language of their latest messages).
- Keep it short and natural for WhatsApp: 1-4 sentences, no email-style greetings or signatures.
- Use WhatsApp formatting only (*bold*, _italic_), never Markdown headings or links in brackets.
- Only state facts found in the conversation or the business knowledge. If something is unknown, ask or say you will check — never invent prices, dates, links or policies.
`)
	if p := strings.TrimSpace(settings.AI.SystemPrompt); p != "" {
		b.WriteString("\nBusiness instructions:\n" + truncateRunes(p, 3000) + "\n")
	}
	if k := a.chatAIKnowledge(contact.OrganizationID, contact.WhatsAppAccount); k != "" {
		b.WriteString("\n" + k + "\n")
	}
	return b.String()
}

type chatAITextResult struct {
	Text           string `json:"text"`
	TargetLanguage string `json:"target_language,omitempty"`
}

func (a *App) chatAISuggestReply(settings *models.ChatbotSettings, contact *models.Contact, agentName string) (any, error) {
	conv, n := a.chatAIConversation(contact, chatAIHistoryLimit)
	if n == 0 {
		return nil, chatAIUserError("There are no messages to reply to yet")
	}
	user := "Conversation so far:\n" + conv + "\nWrite the agent's next reply. Return only the message text."
	out, err := a.completeAIChat(settings, a.chatAIReplySystemPrompt(settings, contact, agentName), user, chatAIMaxTokens, models.AIUsagePurposeChatAssist)
	if err != nil {
		return nil, err
	}
	return chatAITextResult{Text: cleanAIMessageText(out)}, nil
}

func (a *App) chatAIRewrite(settings *models.ChatbotSettings, contact *models.Contact, req chatAIAssistRequest) (any, error) {
	draft := strings.TrimSpace(req.Draft)
	if draft == "" {
		return nil, chatAIUserError("Type a message to rewrite first")
	}
	instruction, ok := chatAIRewriteTones[req.Tone]
	if !ok {
		return nil, chatAIUserError("Unknown rewrite style")
	}
	system := "You rewrite a customer support agent's WhatsApp message. " + instruction +
		" Keep the same language as the draft, keep any {{placeholders}}, names, numbers and links exactly. Return only the rewritten message."
	out, err := a.completeAIChat(settings, system, "Draft:\n"+draft, chatAIMaxTokens, models.AIUsagePurposeChatAssist)
	if err != nil {
		return nil, err
	}
	return chatAITextResult{Text: cleanAIMessageText(out)}, nil
}

type chatAISummary struct {
	Summary       string   `json:"summary"`
	KeyPoints     []string `json:"key_points"`
	OpenQuestions []string `json:"open_questions"`
	Sentiment     string   `json:"sentiment"`
}

const chatAISummarySystemPrompt = `You summarize a WhatsApp support conversation for the agent taking over.
Return ONLY valid JSON (no markdown fences):
{"summary":"2-3 sentences: who the customer is, what they want, what has happened so far",
 "key_points":["facts the agent needs: order numbers, dates, products, promises made"],
 "open_questions":["what is still unresolved or what the customer is waiting for"],
 "sentiment":"positive | neutral | frustrated | angry"}
Write in English. Keep each list to at most 5 short items; use [] when empty. Never invent facts.`

func (a *App) chatAISummarize(settings *models.ChatbotSettings, contact *models.Contact) (any, error) {
	conv, n := a.chatAIConversation(contact, 60)
	if n == 0 {
		return nil, chatAIUserError("There are no messages to summarize yet")
	}
	return a.summarizeConversation(settings, conv, models.AIUsagePurposeChatAssist)
}

func (a *App) summarizeConversation(settings *models.ChatbotSettings, conv, purpose string) (*chatAISummary, error) {
	out, err := a.completeAIChat(settings, chatAISummarySystemPrompt, "Conversation:\n"+conv, chatAIMaxTokens, purpose)
	if err != nil {
		return nil, err
	}
	return parseChatAISummary(out)
}

func parseChatAISummary(raw string) (*chatAISummary, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("AI returned no summary")
	}
	var s chatAISummary
	if err := json.Unmarshal([]byte(cleaned), &s); err != nil {
		return nil, fmt.Errorf("AI returned an invalid summary")
	}
	s.Summary = truncateRunes(strings.TrimSpace(s.Summary), 1000)
	if s.Summary == "" {
		return nil, fmt.Errorf("AI returned an empty summary")
	}
	s.KeyPoints = cleanStringList(s.KeyPoints, 5, 200)
	s.OpenQuestions = cleanStringList(s.OpenQuestions, 5, 200)
	switch s.Sentiment = strings.ToLower(strings.TrimSpace(s.Sentiment)); s.Sentiment {
	case "positive", "neutral", "frustrated", "angry":
	default:
		s.Sentiment = "neutral"
	}
	return &s, nil
}

// formatHandoffSummary renders a summary as plain text for the transfer record.
func formatHandoffSummary(s *chatAISummary) string {
	var b strings.Builder
	b.WriteString(s.Summary)
	if len(s.KeyPoints) > 0 {
		b.WriteString("\n\nKey points:")
		for _, p := range s.KeyPoints {
			b.WriteString("\n• " + p)
		}
	}
	if len(s.OpenQuestions) > 0 {
		b.WriteString("\n\nOpen:")
		for _, q := range s.OpenQuestions {
			b.WriteString("\n• " + q)
		}
	}
	if s.Sentiment != "" && s.Sentiment != "neutral" {
		b.WriteString("\n\nMood: " + s.Sentiment)
	}
	return b.String()
}

func (a *App) chatAITranslate(settings *models.ChatbotSettings, contact *models.Contact, req chatAIAssistRequest) (any, error) {
	target := truncateRunes(strings.TrimSpace(req.TargetLanguage), 40)
	if target == "" {
		target = "English"
	}

	// Incoming message bubble: translate the stored message and cache it.
	if req.MessageID != "" {
		msgID, err := uuid.Parse(req.MessageID)
		if err != nil {
			return nil, chatAIUserError("Invalid message")
		}
		var msg models.Message
		if err := a.DB.Where("id = ? AND contact_id = ? AND organization_id = ?", msgID, contact.ID, contact.OrganizationID).First(&msg).Error; err != nil {
			return nil, chatAIUserError("Message not found")
		}
		source := strings.TrimSpace(msg.Content)
		if t, _ := msg.Metadata["transcript"].(string); msg.MessageType == models.MessageTypeAudio && t != "" {
			source = t
		}
		if source == "" {
			return nil, chatAIUserError("This message has no text to translate")
		}
		cacheKey := strings.ToLower(target)
		if cached, ok := msg.Metadata["translations"].(map[string]any); ok {
			if s, ok := cached[cacheKey].(string); ok && s != "" {
				return chatAITextResult{Text: s, TargetLanguage: target}, nil
			}
		}
		out, err := a.translateText(settings, source, target)
		if err != nil {
			return nil, err
		}
		meta := models.JSONB{}
		for k, v := range msg.Metadata {
			meta[k] = v
		}
		translations, _ := meta["translations"].(map[string]any)
		if translations == nil {
			translations = map[string]any{}
		}
		translations[cacheKey] = out
		meta["translations"] = translations
		a.DB.Model(&models.Message{}).Where("id = ?", msg.ID).Update("metadata", meta)
		return chatAITextResult{Text: out, TargetLanguage: target}, nil
	}

	text := strings.TrimSpace(firstNonEmpty(req.Text, req.Draft))
	if text == "" {
		return nil, chatAIUserError("Type a message to translate first")
	}
	if strings.EqualFold(target, "customer") {
		conv, n := a.chatAIConversation(contact, 10)
		if n == 0 {
			return nil, chatAIUserError("Can't tell the customer's language without messages")
		}
		system := "You translate a support agent's WhatsApp reply into the language the customer writes in. " +
			"First decide the customer's language from their messages, then translate the draft. Keep {{placeholders}}, names, numbers, links and *formatting* exactly.\n" +
			`Return ONLY JSON: {"language":"<language name in English>","text":"<translated message>"}`
		out, err := a.completeAIChat(settings, system, "Recent conversation:\n"+conv+"\nDraft to translate:\n"+text, chatAIMaxTokens, models.AIUsagePurposeChatAssist)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Language string `json:"language"`
			Text     string `json:"text"`
		}
		if json.Unmarshal([]byte(extractJSONObject(out)), &parsed) != nil || strings.TrimSpace(parsed.Text) == "" {
			return nil, fmt.Errorf("AI returned an invalid translation")
		}
		return chatAITextResult{Text: cleanAIMessageText(parsed.Text), TargetLanguage: truncateRunes(strings.TrimSpace(parsed.Language), 40)}, nil
	}
	out, err := a.translateText(settings, text, target)
	if err != nil {
		return nil, err
	}
	return chatAITextResult{Text: out, TargetLanguage: target}, nil
}

func (a *App) translateText(settings *models.ChatbotSettings, text, target string) (string, error) {
	system := "Translate the user's text into " + target + ". Keep {{placeholders}}, names, numbers, links, emoji and *formatting* exactly. " +
		"If it is already in " + target + ", return it unchanged. Return only the translation."
	out, err := a.completeAIChat(settings, system, text, chatAIMaxTokens, models.AIUsagePurposeChatAssist)
	if err != nil {
		return "", err
	}
	return cleanAIMessageText(out), nil
}

type chatAIQuickRepliesResult struct {
	Replies []string `json:"replies"`
}

func (a *App) chatAIQuickReplies(settings *models.ChatbotSettings, contact *models.Contact, agentName string) (any, error) {
	conv, n := a.chatAIConversation(contact, 15)
	if n == 0 {
		return chatAIQuickRepliesResult{Replies: []string{}}, nil
	}
	system := a.chatAIReplySystemPrompt(settings, contact, agentName) +
		"\nSuggest 3 different short replies the agent could send next (each under 120 characters, different intents, e.g. answer / ask for details / next step).\n" +
		`Return ONLY JSON: {"replies":["...","...","..."]}`
	out, err := a.completeAIChat(settings, system, "Conversation so far:\n"+conv, chatAIMaxTokens, models.AIUsagePurposeChatAssist)
	if err != nil {
		return nil, err
	}
	var parsed chatAIQuickRepliesResult
	if json.Unmarshal([]byte(extractJSONObject(out)), &parsed) != nil {
		return nil, fmt.Errorf("AI returned invalid suggestions")
	}
	parsed.Replies = cleanStringList(parsed.Replies, 3, 300)
	for i, r := range parsed.Replies {
		parsed.Replies[i] = cleanAIMessageText(r)
	}
	return parsed, nil
}

type chatAIFieldSuggestion struct {
	Key     string `json:"key"`
	Current string `json:"current"`
	Value   string `json:"value"`
}

type chatAITagsFieldsResult struct {
	Tags   []string                `json:"tags"`
	Fields []chatAIFieldSuggestion `json:"fields"`
}

func (a *App) chatAISuggestTagsFields(settings *models.ChatbotSettings, contact *models.Contact) (any, error) {
	conv, n := a.chatAIConversation(contact, chatAIHistoryLimit)
	if n == 0 {
		return nil, chatAIUserError("There are no messages to learn from yet")
	}
	var tags []models.Tag
	a.DB.Where("organization_id = ?", contact.OrganizationID).Order("name").Limit(chatAIMaxTagOptions).Find(&tags)
	tagNames := make([]string, 0, len(tags))
	for _, t := range tags {
		tagNames = append(tagNames, t.Name)
	}
	current := chatAICurrentFields(contact)
	currentTags := contactTagNames(contact)

	var b strings.Builder
	b.WriteString("You help a support agent keep contact records tidy. From the conversation, propose tags and contact details.\n")
	if len(tagNames) > 0 {
		b.WriteString("Existing tags (prefer these, exact spelling): " + strings.Join(tagNames, ", ") + "\n")
	}
	b.WriteString("You may propose at most 2 new tags only if none fit (lowercase, 1-3 words).\n")
	b.WriteString("Contact fields you may fill: " + strings.Join(chatAIContactFields, ", ") + ". profile_name is the customer's real name.\n")
	b.WriteString("Only include a field when the customer clearly stated it. Never guess emails or names.\n")
	b.WriteString(`Return ONLY JSON: {"tags":["..."],"fields":{"email":"...","company":"..."}}`)

	user := fmt.Sprintf("Current tags: %s\nCurrent fields: %s\n\nConversation:\n%s",
		strings.Join(currentTags, ", "), chatAIFieldsJSON(current), conv)
	out, err := a.completeAIChat(settings, b.String(), user, chatAIMaxTokens, models.AIUsagePurposeChatAssist)
	if err != nil {
		return nil, err
	}
	return parseChatAITagsFields(out, tagNames, currentTags, current)
}

func parseChatAITagsFields(raw string, orgTags, currentTags []string, current map[string]string) (*chatAITagsFieldsResult, error) {
	var parsed struct {
		Tags   []any          `json:"tags"`
		Fields map[string]any `json:"fields"`
	}
	if json.Unmarshal([]byte(extractJSONObject(raw)), &parsed) != nil {
		return nil, fmt.Errorf("AI returned invalid suggestions")
	}
	known := map[string]string{}
	for _, t := range orgTags {
		known[strings.ToLower(t)] = t
	}
	have := map[string]bool{}
	for _, t := range currentTags {
		have[strings.ToLower(t)] = true
	}
	res := &chatAITagsFieldsResult{Tags: []string{}, Fields: []chatAIFieldSuggestion{}}
	seen := map[string]bool{}
	newCount := 0
	for _, v := range parsed.Tags {
		name := strings.TrimSpace(scalarString(v))
		lower := strings.ToLower(name)
		if name == "" || have[lower] || seen[lower] || utf8.RuneCountInString(name) > 50 {
			continue
		}
		if canonical, ok := known[lower]; ok {
			name = canonical
		} else {
			if newCount >= 2 {
				continue
			}
			newCount++
			name = lower
		}
		seen[lower] = true
		res.Tags = append(res.Tags, name)
		if len(res.Tags) >= 5 {
			break
		}
	}
	for _, key := range chatAIContactFields {
		val := truncateRunes(strings.TrimSpace(scalarString(parsed.Fields[key])), 200)
		if val == "" || strings.EqualFold(val, current[key]) {
			continue
		}
		if key == "email" && !strings.Contains(val, "@") {
			continue
		}
		res.Fields = append(res.Fields, chatAIFieldSuggestion{Key: key, Current: current[key], Value: val})
	}
	return res, nil
}

func chatAICurrentFields(contact *models.Contact) map[string]string {
	out := map[string]string{"profile_name": contact.ProfileName}
	for _, k := range chatAIContactFields[1:] {
		if v, ok := contact.Metadata[k]; ok {
			out[k] = scalarString(v)
		}
	}
	return out
}

func chatAIFieldsJSON(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func contactTagNames(contact *models.Contact) []string {
	out := make([]string, 0, len(contact.Tags))
	for _, t := range contact.Tags {
		if s, ok := t.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

type chatAITemplateSuggestion struct {
	TemplateID   string            `json:"template_id"`
	TemplateName string            `json:"template_name"`
	DisplayName  string            `json:"display_name"`
	Params       map[string]string `json:"params"`
	Reason       string            `json:"reason"`
}

func (a *App) chatAISuggestTemplate(settings *models.ChatbotSettings, contact *models.Contact) (any, error) {
	var templates []models.Template
	q := a.DB.Where("organization_id = ? AND status = ?", contact.OrganizationID, "APPROVED")
	if contact.WhatsAppAccount != "" {
		q = q.Where("whats_app_account = ?", contact.WhatsAppAccount)
	}
	q.Order("updated_at DESC").Limit(chatAIMaxTemplates).Find(&templates)
	if len(templates) == 0 {
		return nil, chatAIUserError("No approved templates for this account")
	}
	conv, _ := a.chatAIConversation(contact, 20)

	var list strings.Builder
	for _, t := range templates {
		fmt.Fprintf(&list, "- name: %s | category: %s | language: %s | variables: %s\n  body: %s\n",
			t.Name, t.Category, t.Language, strings.Join(templateVariableNames(t.BodyContent), ", "),
			truncateRunes(strings.Join(strings.Fields(t.BodyContent), " "), 400))
	}
	system := `The 24-hour WhatsApp service window is closed, so the agent can only send an approved template.
Pick the single best template to continue this conversation and fill its variables from the conversation and contact.
Return ONLY JSON: {"template_name":"<exact name from the list>","params":{"<variable>":"<value>"},"reason":"one short sentence"}
Use only variables listed for that template. Leave a variable as "" when the value is unknown — never invent order numbers, dates or links.`
	user := fmt.Sprintf("Contact name: %s\n\nApproved templates:\n%s\nConversation:\n%s", contact.ProfileName, list.String(), conv)
	out, err := a.completeAIChat(settings, system, user, chatAIMaxTokens, models.AIUsagePurposeChatAssist)
	if err != nil {
		return nil, err
	}
	return parseChatAITemplateSuggestion(out, templates)
}

func parseChatAITemplateSuggestion(raw string, templates []models.Template) (*chatAITemplateSuggestion, error) {
	var parsed struct {
		TemplateName string         `json:"template_name"`
		Params       map[string]any `json:"params"`
		Reason       string         `json:"reason"`
	}
	if json.Unmarshal([]byte(extractJSONObject(raw)), &parsed) != nil {
		return nil, fmt.Errorf("AI returned an invalid suggestion")
	}
	name := strings.TrimSpace(parsed.TemplateName)
	for _, t := range templates {
		if t.Name != name {
			continue
		}
		params := map[string]string{}
		for _, v := range templateVariableNames(t.BodyContent) {
			if val := truncateRunes(strings.TrimSpace(scalarString(parsed.Params[v])), 500); val != "" {
				params[v] = val
			}
		}
		return &chatAITemplateSuggestion{
			TemplateID:   t.ID.String(),
			TemplateName: t.Name,
			DisplayName:  firstNonEmpty(t.DisplayName, t.Name),
			Params:       params,
			Reason:       truncateRunes(strings.TrimSpace(parsed.Reason), 300),
		}, nil
	}
	return nil, fmt.Errorf("AI picked an unknown template")
}

func templateVariableNames(body string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range chatAITemplateVarRe.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// cleanAIMessageText strips wrapping quotes/fences models sometimes add.
func cleanAIMessageText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```text")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	for _, q := range [][2]string{{`"`, `"`}, {"“", "”"}} {
		if len(s) > len(q[0])+len(q[1]) && strings.HasPrefix(s, q[0]) && strings.HasSuffix(s, q[1]) &&
			!strings.Contains(s[len(q[0]):len(s)-len(q[1])], q[0]) {
			s = strings.TrimSpace(s[len(q[0]) : len(s)-len(q[1])])
		}
	}
	return truncateRunes(s, chatAIDraftMaxRunes)
}

func cleanStringList(in []string, max, maxRunes int) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = truncateRunes(strings.TrimSpace(s), maxRunes)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}
