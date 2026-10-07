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

	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
)

// executeAIVoice runs one conversational turn: optional speech gather, LLM reply, TTS play.
// Outcomes: default, error.
func (m *Manager) executeAIVoice(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	inputVar, _ := node.Config["input_variable"].(string)
	storeAs, _ := node.Config["store_as"].(string)
	if storeAs == "" {
		storeAs = "ai_reply"
	}
	systemPrompt, _ := node.Config["system_prompt"].(string)
	listenSecs := getConfigInt(node.Config, "listen_seconds", 0)

	userText := ""
	if ctx != nil && ctx.Variables != nil && inputVar != "" {
		userText = ctx.Variables[inputVar]
	}

	// Optional listen turn before LLM.
	if userText == "" && listenSecs > 0 {
		prompt := m.resolvePromptAudio(node, ctx)
		if prompt != "" && m.config.AudioDir != "" && player != nil {
			m.playInterruptible(session, player, filepath.Join(m.config.AudioDir, prompt))
		}
		path, _, err := m.recordCallerAudio(session, player, time.Duration(listenSecs)*time.Second, "#", false)
		if err == nil {
			defer func() { _ = os.Remove(path) }()
			if key := m.resolveOpenAIKey(session.OrganizationID); key != "" {
				if text, terr := transcribeWhisper(key, path, session.Locale); terr == nil {
					userText = text
				} else {
					m.log.Warn("IVR ai_voice Whisper failed", "error", terr, "call_id", session.ID)
				}
			} else {
				m.log.Warn("IVR ai_voice: no OpenAI key for Whisper STT", "call_id", session.ID)
			}
		}
	}

	if strings.TrimSpace(userText) == "" {
		fallback, _ := node.Config["fallback_input"].(string)
		userText = interpolateTemplate(fallback, ctx.Variables)
	}
	if strings.TrimSpace(userText) == "" {
		m.log.Warn("IVR ai_voice: empty user input", "call_id", session.ID)
		return "error"
	}

	reply, err := m.generateIVRAIReply(session, systemPrompt, userText)
	if err != nil {
		m.log.Error("IVR ai_voice LLM failed", "error", err, "call_id", session.ID)
		return "error"
	}
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	ctx.Variables[storeAs] = reply
	if inputVar != "" {
		ctx.Variables[inputVar] = userText
	}

	// Speak reply via TTS when available.
	if player != nil && m.tts != nil && strings.TrimSpace(reply) != "" {
		filename, err := m.synthesizeTTS(session.OrganizationID, reply, node.Config)
		if err != nil {
			m.log.Error("IVR ai_voice TTS failed", "error", err, "call_id", session.ID)
		} else if m.config.AudioDir != "" {
			interruptible, _ := node.Config["interruptible"].(bool)
			full := filepath.Join(m.config.AudioDir, filename)
			if interruptible {
				m.playInterruptible(session, player, full)
			} else {
				_, _ = player.PlayFile(full)
			}
		}
	}
	return "default"
}

func (m *Manager) generateIVRAIReply(session *CallSession, systemPrompt, userMessage string) (string, error) {
	var settings models.ChatbotSettings
	if m.db != nil {
		_ = m.db.Where("organization_id = ?", session.OrganizationID).First(&settings).Error
	}
	apiKey := settings.AI.APIKey
	if apiKey != "" && m.encryptionKey != "" {
		crypto.DecryptFields(m.encryptionKey, &apiKey)
	}
	provider := settings.AI.Provider
	model := settings.AI.Model
	if !settings.AI.Enabled || apiKey == "" {
		// Fall back to server OpenAI key.
		if m.aiConfig == nil || m.aiConfig.OpenAIKey == "" {
			return "", fmt.Errorf("AI not configured")
		}
		apiKey = m.aiConfig.OpenAIKey
		provider = models.AIProviderOpenAI
		if model == "" {
			model = "gpt-4o-mini"
		}
	}
	if systemPrompt == "" {
		systemPrompt = settings.AI.SystemPrompt
	}
	if systemPrompt == "" {
		systemPrompt = "You are a helpful phone IVR assistant. Keep replies under 2 short sentences."
	}
	maxTokens := settings.AI.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 200
	}

	switch provider {
	case models.AIProviderAnthropic:
		return ivrAnthropicChat(apiKey, model, systemPrompt, userMessage, maxTokens)
	case models.AIProviderGoogle:
		return ivrGoogleChat(apiKey, model, systemPrompt, userMessage, maxTokens)
	case models.AIProviderOpenRouter:
		return ivrOpenAIChatAt(ivrOpenRouterChatURL, apiKey, model, systemPrompt, userMessage, maxTokens)
	default:
		if model == "" {
			model = "gpt-4o-mini"
		}
		return ivrOpenAIChat(apiKey, model, systemPrompt, userMessage, maxTokens)
	}
}

const ivrOpenRouterChatURL = "https://openrouter.ai/api/v1/chat/completions"

func ivrOpenAIChat(apiKey, model, systemPrompt, userMessage string, maxTokens int) (string, error) {
	return ivrOpenAIChatAt("https://api.openai.com/v1/chat/completions", apiKey, model, systemPrompt, userMessage, maxTokens)
}

func ivrOpenAIChatAt(endpoint, apiKey, model, systemPrompt, userMessage string, maxTokens int) (string, error) {
	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userMessage},
	}
	payload := map[string]any{
		"model":    model,
		"messages": messages,
	}
	// OpenRouter rejects output caps larger than the model's context window.
	if maxTokens > 0 && (endpoint != ivrOpenRouterChatURL || maxTokens <= 32768) {
		payload["max_tokens"] = maxTokens
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("empty openai response")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func ivrAnthropicChat(apiKey, model, systemPrompt, userMessage string, maxTokens int) (string, error) {
	if model == "" {
		model = "claude-3-5-haiku-latest"
	}
	payload := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages":   []map[string]string{{"role": "user", "content": userMessage}},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
	}
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Content) == 0 {
		return "", fmt.Errorf("empty anthropic response")
	}
	return strings.TrimSpace(parsed.Content[0].Text), nil
}

func ivrGoogleChat(apiKey, model, systemPrompt, userMessage string, maxTokens int) (string, error) {
	if model == "" {
		model = "gemini-flash-latest"
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)
	payload := map[string]any{
		"system_instruction": map[string]any{"parts": []map[string]string{{"text": systemPrompt}}},
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]string{{"text": userMessage}}},
		},
		"generationConfig": map[string]any{"maxOutputTokens": maxTokens},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("google %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
	}
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty google response")
	}
	return strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text), nil
}
