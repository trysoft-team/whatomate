package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/models"
)

// errAIQuotaExceeded marks provider rate-limit / quota responses (HTTP 429).
var errAIQuotaExceeded = errors.New("AI provider quota exceeded")

func aiProviderError(label string, status int, msg string) error {
	if status == http.StatusTooManyRequests {
		return fmt.Errorf("%w: %s: %s", errAIQuotaExceeded, label, msg)
	}
	return fmt.Errorf("%s: %s", label, msg)
}

// aiQuotaMessage is the agent-facing text for errAIQuotaExceeded.
func aiQuotaMessage(settings *models.ChatbotSettings) string {
	return fmt.Sprintf("AI usage limit reached for %s. Try again later, or raise the limit (enable billing or choose another model) in Settings → AI.",
		firstNonEmpty(settings.AI.Model, string(settings.AI.Provider)))
}

// completeAIChat runs a one-shot chat completion with an explicit system prompt
// and no session history / AI-context injection. Used for structured tasks like
// flow generation. maxTokens overrides settings when > 0.
// purpose is stored on ai_usage_logs (e.g. flow_generate).
func (a *App) completeAIChat(settings *models.ChatbotSettings, systemPrompt, userMessage string, maxTokens int, purpose string) (string, error) {
	if settings == nil {
		return "", fmt.Errorf("chatbot settings required")
	}
	if maxTokens <= 0 {
		maxTokens = settings.AI.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	if purpose == "" {
		purpose = models.AIUsagePurposeAIComplete
	}
	uctx := aiUsageContext{Settings: settings, Purpose: purpose}

	switch settings.AI.Provider {
	case models.AIProviderOpenAI, models.AIProviderOpenRouter:
		return a.completeOpenAIChat(settings, systemPrompt, userMessage, maxTokens, uctx)
	case models.AIProviderAnthropic:
		return a.completeAnthropicChat(settings, systemPrompt, userMessage, maxTokens, uctx)
	case models.AIProviderGoogle:
		return a.completeGoogleChat(settings, systemPrompt, userMessage, maxTokens, uctx)
	default:
		return "", fmt.Errorf("unsupported AI provider: %s", settings.AI.Provider)
	}
}

func (a *App) completeOpenAIChat(settings *models.ChatbotSettings, systemPrompt, userMessage string, maxTokens int, uctx aiUsageContext) (string, error) {
	messages := []map[string]string{}
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": systemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": userMessage})

	payload := map[string]any{
		"model":    settings.AI.Model,
		"messages": messages,
	}
	setOpenAICompatibleMaxTokens(payload, settings.AI.Provider, maxTokens)
	if settings.AI.Temperature > 0 {
		payload["temperature"] = settings.AI.Temperature
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}
	endpoint, errLabel := openAICompatibleEndpoint(settings.AI.Provider)
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	setOpenAICompatibleHeaders(req, settings)

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &errResp)
		msg := errResp.Error.Message
		if msg == "" {
			msg = string(body)
		}
		return "", aiProviderError(errLabel, resp.StatusCode, msg)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from %s", settings.AI.Provider)
	}
	p, c, t := parseOpenAIUsage(body)
	a.recordAIUsage(uctx, string(settings.AI.Provider), settings.AI.Model, p, c, t)
	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

func (a *App) completeAnthropicChat(settings *models.ChatbotSettings, systemPrompt, userMessage string, maxTokens int, uctx aiUsageContext) (string, error) {
	payload := map[string]any{
		"model":      settings.AI.Model,
		"messages":   []map[string]string{{"role": "user", "content": userMessage}},
		"max_tokens": maxTokens,
	}
	if strings.TrimSpace(systemPrompt) != "" {
		payload["system"] = systemPrompt
	}
	if settings.AI.Temperature > 0 {
		payload["temperature"] = settings.AI.Temperature
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", settings.AI.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &errResp)
		msg := errResp.Error.Message
		if msg == "" {
			msg = string(body)
		}
		return "", aiProviderError("anthropic API error", resp.StatusCode, msg)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	for _, content := range result.Content {
		if content.Type == "text" {
			p, c, t := parseAnthropicUsage(body)
			a.recordAIUsage(uctx, string(models.AIProviderAnthropic), settings.AI.Model, p, c, t)
			return strings.TrimSpace(content.Text), nil
		}
	}
	return "", fmt.Errorf("no text response from Anthropic")
}

// aiOverloadedRetryDelay is the base backoff when Gemini reports it is overloaded.
var aiOverloadedRetryDelay = 1500 * time.Millisecond

func (a *App) completeGoogleChat(settings *models.ChatbotSettings, systemPrompt, userMessage string, maxTokens int, uctx aiUsageContext) (string, error) {
	endpoint := fmt.Sprintf(googleGenerateURL, url.PathEscape(settings.AI.Model))

	genCfg := map[string]any{"maxOutputTokens": maxTokens}
	if settings.AI.Temperature > 0 {
		genCfg["temperature"] = settings.AI.Temperature
	}
	payload := map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]string{{"text": userMessage}},
		}},
		"generationConfig": genCfg,
	}
	if strings.TrimSpace(systemPrompt) != "" {
		payload["systemInstruction"] = map[string]any{
			"parts": []map[string]string{{"text": systemPrompt}},
		}
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}
	var body []byte
	var status int
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(jsonPayload))
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", settings.AI.APIKey)

		resp, err := a.HTTPClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("request failed: %w", err)
		}
		body, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		status = resp.StatusCode
		// Gemini often answers 503 "high demand" for a few seconds at a time.
		if status == http.StatusServiceUnavailable && attempt < 2 {
			time.Sleep(aiOverloadedRetryDelay * time.Duration(attempt+1))
			continue
		}
		break
	}
	if status != 200 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &errResp)
		msg := errResp.Error.Message
		if msg == "" {
			msg = string(body)
		}
		return "", aiProviderError("google AI API error", status, msg)
	}
	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response from Google AI")
	}
	p, c, t := parseGoogleUsage(body)
	a.recordAIUsage(uctx, string(models.AIProviderGoogle), settings.AI.Model, p, c, t)
	return strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text), nil
}
