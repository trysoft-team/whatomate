package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func orModel(id string, ctx int, out []string, free bool, params ...string) openRouterCatalogModel {
	m := openRouterCatalogModel{ID: id, Name: id + " (free)", ContextLength: ctx, SupportedParameters: params}
	m.Architecture.OutputModalities = out
	if free {
		m.Pricing.Prompt, m.Pricing.Completion = "0", "0"
	} else {
		m.Pricing.Prompt, m.Pricing.Completion = "0.000001", "0.000002"
	}
	return m
}

func TestRankOpenRouterFreeModels(t *testing.T) {
	text := []string{"text"}
	catalog := []openRouterCatalogModel{
		orModel("stealth/space-bunny-alpha", 1000000, text, true, "response_format"),
		orModel("liquid/lfm-2.5-2.6b:free", 65536, text, true, "response_format"),
		orModel("cohere/north-mini-code:free", 256000, text, true),
		orModel("nvidia/nemotron-3.5-content-safety:free", 128000, text, true),
		orModel("google/lyria-3-pro-preview", 1048576, []string{"text", "audio"}, true),
		orModel("openai/gpt-5", 400000, text, false, "response_format"),
		orModel("google/gemma-4-31b-it:free", 262144, text, true, "response_format"),
		orModel("qwen/qwen3.8-27b:free", 262144, text, true),
		orModel("nvidia/nemotron-3-ultra-550b-a55b:free", 1000000, text, true),
		orModel("nvidia/nemotron-3-super-120b-a12b:free", 262144, text, true, "response_format"),
		orModel("openrouter/free", 200000, text, true, "response_format"),
		orModel("poolside/laguna-s-2.1:free", 262144, text, true),
		orModel("thinkingmachines/inkling:free", 1000000, text, true),
		orModel("acme/chat-small:free", 128000, text, true),
		orModel("acme/chat:free", 128000, text, true),
	}
	catalog[len(catalog)-1].Architecture.InputModalities = []string{"text", "audio"}
	got := rankOpenRouterFreeModels(catalog)
	ids := make([]string, len(got))
	for i, m := range got {
		ids[i] = m.ID
	}
	assert.Equal(t, []string{
		"openrouter/free",
		"nvidia/nemotron-3-ultra-550b-a55b:free",
		"nvidia/nemotron-3-super-120b-a12b:free",
		"google/gemma-4-31b-it:free",
		"qwen/qwen3.8-27b:free",
		"acme/chat:free",
		"acme/chat-small:free",
		"liquid/lfm-2.5-2.6b:free",
		"poolside/laguna-s-2.1:free",
		"nvidia/nemotron-3.5-content-safety:free",
		"cohere/north-mini-code:free",
	}, ids, "paid, audio-output, stealth and harness-only models are dropped; known-size models beat unknown ones; tiny and specialised models rank last")
	assert.True(t, got[5].SupportsAudio)
	assert.False(t, got[6].SupportsAudio)

	assert.Equal(t, "Auto (best available free model)", got[0].Name)
	assert.True(t, got[0].Recommended)
	assert.True(t, got[1].Recommended)
	assert.True(t, got[6].Recommended)
	assert.False(t, got[7].Recommended, "tiny models are not recommended")
	assert.False(t, got[8].Recommended, "code models are not recommended")
	assert.Equal(t, "google/gemma-4-31b-it:free", got[3].Name, "the (free) suffix is trimmed from names")
	assert.True(t, got[3].SupportsJSON)
}

func TestOpenRouterModelSizeB(t *testing.T) {
	assert.Equal(t, 550.0, openRouterModelSizeB("nvidia/nemotron-3-ultra-550b-a55b:free"))
	assert.Equal(t, 2.6, openRouterModelSizeB("liquid/lfm-2.5-2.6b:free"))
	assert.Equal(t, 30.0, openRouterModelSizeB("nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free"))
	assert.Equal(t, float64(openRouterUnknownSizeB), openRouterModelSizeB("thinkingmachines/inkling:free"))
}

func TestCompleteAIChat_OpenRouter(t *testing.T) {
	var gotAuth, gotTitle string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTitle = r.Header.Get("X-Title")
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" hi there "}}]}`))
	}))
	defer srv.Close()
	prev := openRouterChatURL
	openRouterChatURL = srv.URL
	defer func() { openRouterChatURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderOpenRouter, Model: "openrouter/free", APIKey: "sk-or-test"}}
	out, err := app.completeAIChat(settings, "sys", "hello", 50, models.AIUsagePurposeChatAssist)
	require.NoError(t, err)
	assert.Equal(t, "hi there", out)
	assert.Equal(t, "Bearer sk-or-test", gotAuth)
	assert.Equal(t, "Whatomate", gotTitle)
	assert.Equal(t, "openrouter/free", gotBody["model"])
	assert.Equal(t, float64(50), gotBody["max_tokens"])

	_, err = app.completeAIChat(settings, "sys", "hello", 5000000, models.AIUsagePurposeChatAssist)
	require.NoError(t, err)
	assert.NotContains(t, gotBody, "max_tokens", "oversized output caps are dropped for OpenRouter")
}

func TestCompleteAIChat_OpenRouterQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded: free-models-per-day"}}`))
	}))
	defer srv.Close()
	prev := openRouterChatURL
	openRouterChatURL = srv.URL
	defer func() { openRouterChatURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderOpenRouter, Model: "x:free", APIKey: "k"}}
	_, err := app.completeAIChat(settings, "", "hello", 50, "")
	assert.ErrorIs(t, err, errAIQuotaExceeded)
}
