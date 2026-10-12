package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// maxTranscribeBytes stays under Gemini's 20 MB inline-data limit (Whisper allows 25 MB).
const maxTranscribeBytes = 19 << 20

var (
	openAITranscribeURL = "https://api.openai.com/v1/audio/transcriptions"
	googleGenerateURL   = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"
)

const (
	defaultOpenAITranscribeModel     = "whisper-1"
	defaultGoogleTranscribeModel     = "gemini-flash-latest"
	defaultOpenRouterTranscribeModel = "google/gemini-3.5-flash-lite"
)

// transcribeSettings returns a copy of the org settings with AI provider, model
// and key swapped for the voice-transcription ones. An empty transcription
// provider means "same as the AI provider", and an empty transcription key
// reuses the AI key only when both providers match.
func transcribeSettings(org *models.ChatbotSettings) (*models.ChatbotSettings, string) {
	s := *org
	ai := org.AI
	provider := ai.TranscribeProvider
	if provider == "" {
		provider = ai.Provider
	}
	key := ai.TranscribeAPIKey
	if key == "" && provider == ai.Provider {
		key = ai.APIKey
	}
	model := ai.TranscribeModel
	switch provider {
	case models.AIProviderOpenAI:
		model = firstNonEmpty(model, defaultOpenAITranscribeModel)
	case models.AIProviderGoogle:
		if model == "" && ai.Provider == models.AIProviderGoogle {
			model = ai.Model
		}
		model = firstNonEmpty(model, defaultGoogleTranscribeModel)
	case models.AIProviderOpenRouter:
		model = firstNonEmpty(model, defaultOpenRouterTranscribeModel)
	default:
		return nil, fmt.Sprintf("Voice transcription isn't available with %s. Choose OpenAI, Google or OpenRouter under Settings → AI → Voice transcription.",
			firstNonEmpty(string(provider), "this provider"))
	}
	if key == "" {
		return nil, "Add an API key for voice transcription in Settings → AI → Voice transcription."
	}
	s.AI.Provider, s.AI.Model, s.AI.APIKey = provider, model, key
	return &s, ""
}

const transcribePrompt = "Transcribe this WhatsApp voice note verbatim in the language it is spoken; do not translate. " +
	"Return only the transcript text, with no labels, timestamps, alternatives or commentary. If there is no speech, return an empty string."

// transcribeMaxTokens leaves room for reasoning models, which can spend
// thousands of tokens thinking about a short voice note before answering.
const transcribeMaxTokens = 16384

const (
	transcribeContextMessages = 8
	transcribeContextMaxRunes = 1500
	whisperPromptMaxRunes     = 800 // Whisper only reads the last 224 tokens of its prompt
)

// transcribeRetryDelay is the base backoff when the provider reports it is overloaded.
var transcribeRetryDelay = 1500 * time.Millisecond

// transcribeChatContext returns the text messages sent just before a voice note,
// oldest first. Models guess the spoken language far more reliably with it,
// especially for low-resource languages such as Shona.
func (a *App) transcribeChatContext(msg *models.Message) string {
	var msgs []models.Message
	a.DB.Select("direction", "content").
		Where("contact_id = ? AND organization_id = ? AND message_type = ? AND content <> '' AND created_at < ?",
			msg.ContactID, msg.OrganizationID, models.MessageTypeText, msg.CreatedAt).
		Order("created_at DESC").Limit(transcribeContextMessages).Find(&msgs)
	lines := make([]string, 0, len(msgs))
	total := 0
	for _, m := range msgs {
		who := "Agent"
		if m.Direction == models.DirectionIncoming {
			who = "Customer"
		}
		line := who + ": " + truncateRunes(strings.TrimSpace(m.Content), 200)
		total += len([]rune(line))
		if total > transcribeContextMaxRunes {
			break
		}
		lines = append(lines, line)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func transcribePromptWithContext(chatContext string) string {
	if chatContext == "" {
		return transcribePrompt
	}
	return transcribePrompt + "\n\nFor context only (do not transcribe it), the recent messages in this chat. " +
		"The voice note is usually in the same language:\n" + chatContext
}

// TranscribeMessage transcribes an audio message and caches the transcript
// in the message metadata.
func (a *App) TranscribeMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	msgID, err := parsePathUUID(r, "id", "message")
	if err != nil {
		return nil
	}

	var msg models.Message
	if err := a.DB.Where("id = ? AND organization_id = ?", msgID, orgID).First(&msg).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Message not found", nil, "")
	}
	var contact models.Contact
	q := a.scopeAssignedContact(a.DB.Where("id = ? AND organization_id = ?", msg.ContactID, orgID), userID, orgID)
	if err := q.First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Message not found", nil, "")
	}
	if msg.MessageType != models.MessageTypeAudio || msg.MediaURL == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only voice notes and audio messages can be transcribed", nil, "")
	}
	force := string(r.RequestCtx.QueryArgs().Peek("force")) == "true"
	if cached, _ := msg.Metadata["transcript"].(string); cached != "" && !force {
		return r.SendEnvelope(map[string]string{"text": cached})
	}

	orgSettings, errMsg := a.chatAISettings(orgID, contact.WhatsAppAccount)
	if errMsg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errMsg, nil, "")
	}
	settings, errMsg := transcribeSettings(orgSettings)
	if errMsg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errMsg, nil, "")
	}

	path, err := a.localMediaPath(msg.MediaURL)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Audio file not found", nil, "")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Audio file not found", nil, "")
	}
	if len(data) > maxTranscribeBytes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Audio is too large to transcribe", nil, "")
	}

	mimeType := audioMimeType(msg.MediaMimeType, path)
	chatContext := a.transcribeChatContext(&msg)
	var text string
	switch settings.AI.Provider {
	case models.AIProviderOpenAI:
		text, err = a.transcribeOpenAI(settings, data, filepath.Base(path), truncateRunes(chatContext, whisperPromptMaxRunes))
	case models.AIProviderOpenRouter:
		text, err = a.transcribeOpenRouter(settings, data, mimeType, transcribePromptWithContext(chatContext))
	default:
		text, err = a.transcribeGoogle(settings, data, mimeType, transcribePromptWithContext(chatContext))
	}
	if err != nil {
		a.Log.Error("TranscribeMessage failed", "error", err, "provider", settings.AI.Provider, "message_id", msg.ID)
		if errors.Is(err, errAIQuotaExceeded) {
			return r.SendErrorEnvelope(fasthttp.StatusTooManyRequests, aiQuotaMessage(settings), nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Transcription failed: "+err.Error(), nil, "")
	}
	text = strings.TrimSpace(cleanAIMessageText(text))
	if text == "" {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity,
			"No speech was recognised in this voice note. Try again, or choose another transcription model in Settings → AI.", nil, "")
	}

	meta := models.JSONB{}
	for k, v := range msg.Metadata {
		meta[k] = v
	}
	meta["transcript"] = text
	a.DB.Model(&models.Message{}).Where("id = ?", msg.ID).Update("metadata", meta)

	return r.SendEnvelope(map[string]string{"text": text})
}

// audioMimeType returns a bare audio MIME type (no codec parameters).
func audioMimeType(stored, path string) string {
	if mt := strings.TrimSpace(strings.SplitN(stored, ";", 2)[0]); strings.HasPrefix(mt, "audio/") {
		return mt
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return "audio/mpeg"
	case ".aac":
		return "audio/aac"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".amr":
		return "audio/amr"
	case ".wav":
		return "audio/wav"
	case ".webm":
		return "audio/webm"
	default:
		return "audio/ogg"
	}
}

func (a *App) transcribeOpenAI(settings *models.ChatbotSettings, data []byte, filename, prompt string) (string, error) {
	if filepath.Ext(filename) == "" {
		filename += ".ogg"
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	_ = w.WriteField("model", firstNonEmpty(settings.AI.Model, defaultOpenAITranscribeModel))
	_ = w.WriteField("response_format", "json")
	if prompt != "" {
		_ = w.WriteField("prompt", prompt)
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", openAITranscribeURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+settings.AI.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", aiProviderError("OpenAI API error", resp.StatusCode, apiErrorMessage(raw))
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	return parsed.Text, nil
}

func (a *App) transcribeGoogle(settings *models.ChatbotSettings, data []byte, mimeType, prompt string) (string, error) {
	payload := map[string]any{
		"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"inlineData": map[string]string{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)}},
				{"text": prompt},
			},
		}},
		"generationConfig": map[string]any{"maxOutputTokens": transcribeMaxTokens, "temperature": 0},
	}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var raw []byte
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("POST", fmt.Sprintf(googleGenerateURL, url.PathEscape(settings.AI.Model)), bytes.NewReader(jsonPayload))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", settings.AI.APIKey)
		resp, err := a.HTTPClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("request failed: %w", err)
		}
		raw, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			break
		}
		if resp.StatusCode == http.StatusServiceUnavailable && attempt < 2 {
			time.Sleep(transcribeRetryDelay * time.Duration(attempt+1))
			continue
		}
		return "", aiProviderError("google AI API error", resp.StatusCode, apiErrorMessage(raw))
	}
	var result struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text string `json:"text"`
					// Dedicated transcribe models answer with audioTranscription instead of text.
					AudioTranscription struct {
						Text string `json:"text"`
					} `json:"audioTranscription"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	p, c, t := parseGoogleUsage(raw)
	a.recordAIUsage(aiUsageContext{Settings: settings, Purpose: models.AIUsagePurposeTranscribe},
		string(models.AIProviderGoogle), settings.AI.Model, p, c, t)
	if len(result.Candidates) == 0 {
		return "", fmt.Errorf("no response from Google AI")
	}
	if result.Candidates[0].FinishReason == "MAX_TOKENS" {
		return "", fmt.Errorf("%s ran out of output tokens before finishing the transcript", settings.AI.Model)
	}
	var b strings.Builder
	for _, part := range result.Candidates[0].Content.Parts {
		b.WriteString(part.Text)
		b.WriteString(part.AudioTranscription.Text)
	}
	return b.String(), nil
}

// transcribeOpenRouter sends the audio as an input_audio part to an
// audio-capable OpenRouter model (OpenRouter requires account credit for audio,
// even on free models).
func (a *App) transcribeOpenRouter(settings *models.ChatbotSettings, data []byte, mimeType, prompt string) (string, error) {
	payload := map[string]any{
		"model":       settings.AI.Model,
		"max_tokens":  transcribeMaxTokens,
		"temperature": 0,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": prompt},
				{"type": "input_audio", "input_audio": map[string]string{
					"data":   base64.StdEncoding.EncodeToString(data),
					"format": openRouterAudioFormat(mimeType),
				}},
			},
		}},
	}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", openRouterChatURL, bytes.NewReader(jsonPayload))
	if err != nil {
		return "", err
	}
	setOpenAICompatibleHeaders(req, settings)
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", aiProviderError("OpenRouter API error", resp.StatusCode, apiErrorMessage(raw))
	}
	var result struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	p, c, t := parseOpenAIUsage(raw)
	a.recordAIUsage(aiUsageContext{Settings: settings, Purpose: models.AIUsagePurposeTranscribe},
		string(models.AIProviderOpenRouter), settings.AI.Model, p, c, t)
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenRouter")
	}
	if result.Choices[0].FinishReason == "length" {
		return "", fmt.Errorf("%s ran out of output tokens before finishing the transcript", settings.AI.Model)
	}
	return result.Choices[0].Message.Content, nil
}

// openRouterAudioFormat maps a MIME type to OpenRouter's input_audio format name.
func openRouterAudioFormat(mimeType string) string {
	switch mimeType {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav", "audio/x-wav":
		return "wav"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a"
	case "audio/aac":
		return "aac"
	case "audio/flac":
		return "flac"
	default:
		return "ogg"
	}
}

func apiErrorMessage(body []byte) string {
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &errResp)
	if errResp.Error.Message != "" {
		return errResp.Error.Message
	}
	return truncateRunes(string(body), 300)
}
