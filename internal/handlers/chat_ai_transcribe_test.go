package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func TestTranscribeSettings(t *testing.T) {
	org := func(ai models.AIConfig) *models.ChatbotSettings { return &models.ChatbotSettings{AI: ai} }

	t.Run("same as AI provider reuses its key and model", func(t *testing.T) {
		s, msg := transcribeSettings(org(models.AIConfig{Provider: models.AIProviderGoogle, Model: "gemini-x", APIKey: "g"}))
		require.Empty(t, msg)
		assert.Equal(t, models.AIProviderGoogle, s.AI.Provider)
		assert.Equal(t, "gemini-x", s.AI.Model)
		assert.Equal(t, "g", s.AI.APIKey)
	})

	t.Run("separate provider uses its own key and default model", func(t *testing.T) {
		in := org(models.AIConfig{Provider: models.AIProviderOpenRouter, Model: "openrouter/free", APIKey: "or",
			TranscribeProvider: models.AIProviderGoogle, TranscribeAPIKey: "g"})
		s, msg := transcribeSettings(in)
		require.Empty(t, msg)
		assert.Equal(t, models.AIProviderGoogle, s.AI.Provider)
		assert.Equal(t, defaultGoogleTranscribeModel, s.AI.Model)
		assert.Equal(t, "g", s.AI.APIKey)
		assert.Equal(t, models.AIProviderOpenRouter, in.AI.Provider, "org settings are not mutated")
	})

	t.Run("separate provider without its own key is rejected", func(t *testing.T) {
		_, msg := transcribeSettings(org(models.AIConfig{Provider: models.AIProviderOpenRouter, APIKey: "or",
			TranscribeProvider: models.AIProviderOpenAI}))
		assert.Contains(t, msg, "Add an API key")
	})

	t.Run("anthropic cannot transcribe", func(t *testing.T) {
		_, msg := transcribeSettings(org(models.AIConfig{Provider: models.AIProviderAnthropic, APIKey: "a"}))
		assert.Contains(t, msg, "isn't available with anthropic")
	})

	t.Run("openrouter defaults to an audio-capable model", func(t *testing.T) {
		s, msg := transcribeSettings(org(models.AIConfig{Provider: models.AIProviderOpenRouter, Model: "openrouter/free", APIKey: "or"}))
		require.Empty(t, msg)
		assert.Equal(t, defaultOpenRouterTranscribeModel, s.AI.Model)
		assert.Equal(t, "whisper-1", firstNonEmpty(org(models.AIConfig{}).AI.TranscribeModel, defaultOpenAITranscribeModel))
	})
}

func TestTranscribeOpenRouter(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello world"}}]}`))
	}))
	defer srv.Close()
	prev := openRouterChatURL
	openRouterChatURL = srv.URL
	defer func() { openRouterChatURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderOpenRouter, Model: "google/gemini-test", APIKey: "sk-or"}}
	text, err := app.transcribeOpenRouter(settings, []byte("OggS-audio"), "audio/ogg", transcribePrompt)
	require.NoError(t, err)
	assert.Equal(t, "hello world", text)
	assert.Equal(t, "Bearer sk-or", gotAuth)
	assert.Equal(t, "google/gemini-test", gotBody["model"])

	msgs := gotBody["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	audio := parts[1].(map[string]any)
	assert.Equal(t, "input_audio", audio["type"])
	assert.Equal(t, "ogg", audio["input_audio"].(map[string]any)["format"])
	assert.NotEmpty(t, audio["input_audio"].(map[string]any)["data"])
}

func TestTranscribeGoogle_MaxTokensIsAnError(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"Maswera here, mu"}]}}]}`))
	}))
	defer srv.Close()
	prev := googleGenerateURL
	googleGenerateURL = srv.URL + "/%s"
	defer func() { googleGenerateURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderGoogle, Model: "gemini-test", APIKey: "k"}}
	_, err := app.transcribeGoogle(settings, []byte("OggS"), "audio/ogg", transcribePrompt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ran out of output tokens")
	gc := gotBody["generationConfig"].(map[string]any)
	assert.Equal(t, float64(transcribeMaxTokens), gc["maxOutputTokens"], "reasoning models need room to think")
}

func TestTranscribeGoogle_AudioTranscriptionPart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"audioTranscription":{"text":"hello there"}}]}}]}`))
	}))
	defer srv.Close()
	prev := googleGenerateURL
	googleGenerateURL = srv.URL + "/%s"
	defer func() { googleGenerateURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderGoogle, Model: "gemini-3.5-transcribe", APIKey: "k"}}
	text, err := app.transcribeGoogle(settings, []byte("OggS"), "audio/ogg", transcribePrompt)
	require.NoError(t, err)
	assert.Equal(t, "hello there", text)
}

func TestTranscribeGoogle_RetriesWhenOverloaded(t *testing.T) {
	calls := 0
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"This model is currently experiencing high demand."}}`))
			return
		}
		var body struct {
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPrompt = body.Contents[0].Parts[1].Text
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"Makadii"}]}}]}`))
	}))
	defer srv.Close()
	prevURL, prevDelay := googleGenerateURL, transcribeRetryDelay
	googleGenerateURL, transcribeRetryDelay = srv.URL+"/%s", time.Millisecond
	defer func() { googleGenerateURL, transcribeRetryDelay = prevURL, prevDelay }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderGoogle, Model: "gemini-test", APIKey: "k"}}
	text, err := app.transcribeGoogle(settings, []byte("OggS"), "audio/ogg", transcribePromptWithContext("Customer: Mune api acho"))
	require.NoError(t, err)
	assert.Equal(t, "Makadii", text)
	assert.Equal(t, 2, calls)
	assert.Contains(t, gotPrompt, "Customer: Mune api acho")
	assert.Contains(t, gotPrompt, "do not transcribe it")
}

func TestTranscribePromptWithContext(t *testing.T) {
	assert.Equal(t, transcribePrompt, transcribePromptWithContext(""))
	assert.Contains(t, transcribePromptWithContext("Agent: hi"), "\nAgent: hi")
}

func TestTranscribeOpenRouter_NeedsCredit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"This request requires at least $0.50 in balance for audio"}}`))
	}))
	defer srv.Close()
	prev := openRouterChatURL
	openRouterChatURL = srv.URL
	defer func() { openRouterChatURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderOpenRouter, Model: "m", APIKey: "k"}}
	_, err := app.transcribeOpenRouter(settings, []byte("x"), "audio/ogg", transcribePrompt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "$0.50 in balance")
}
