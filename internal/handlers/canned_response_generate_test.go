package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestParseAICannedResponseDraft(t *testing.T) {
	t.Parallel()
	raw := "Here it is:\n```json\n" + `{
		"name": "Order delayed apology",
		"shortcut": "/Order Delay!",
		"category": "Support",
		"content": "Hi {{contact_name}}, sorry your order {{order_id}} is late.",
		"buttons": [
			{"type": "reply", "title": "Track my order please now"},
			{"type": "url", "title": "Track", "url": "https://shop.example.com/track"},
			{"type": "phone", "title": "Call us", "phone_number": "+100"},
			{"title": "Refund"},
			{"type": "reply", "title": ""}
		],
		"notes": "Check the tracking link."
	}` + "\n```"

	d, err := parseAICannedResponseDraft(raw)
	require.NoError(t, err)
	assert.Equal(t, "Order delayed apology", d.Name)
	assert.Equal(t, "order-delay", d.Shortcut)
	assert.Equal(t, "support", d.Category)
	assert.Contains(t, d.Content, "{{order_id}}")
	assert.Equal(t, []CannedResponseButton{
		{ID: "btn_1", Title: "Track my order", Type: "reply"},
		{ID: "btn_2", Title: "Refund", Type: "reply"},
	}, d.Buttons, "replies win over url; phone dropped; titles capped at 20")
	assert.NoError(t, validateCannedResponseButtons(d.Buttons))
	assert.Equal(t, "Check the tracking link.", d.Notes)
}

func TestParseAICannedResponseDraft_Defaults(t *testing.T) {
	t.Parallel()

	d, err := parseAICannedResponseDraft(`{"canned_response":{"message":"Thanks for reaching out!\nWe reply within an hour.","category":"vip","buttons":[{"type":"url","title":"Open","url":"https://x.example.com"}]}}`)
	require.NoError(t, err)
	assert.Equal(t, "Thanks for reaching out!", d.Name, "name falls back to the first line")
	assert.Equal(t, "general", d.Category)
	assert.Empty(t, d.Shortcut)
	require.Len(t, d.Buttons, 1)
	assert.Equal(t, "url", d.Buttons[0].Type)
	assert.NoError(t, validateCannedResponseButtons(d.Buttons))

	long := strings.Repeat("a", aiCannedMaxButtonContent+1)
	d, err = parseAICannedResponseDraft(`{"name":"Long","content":"` + long + `","buttons":[{"title":"Ok"}]}`)
	require.NoError(t, err)
	assert.Empty(t, d.Buttons, "interactive bodies are capped at 1024 characters")
	assert.NotNil(t, d.Buttons)

	for _, bad := range []string{"no json", `{"name":"x","content":"  "}`} {
		_, err := parseAICannedResponseDraft(bad)
		assert.Error(t, err, bad)
	}
}

func TestCompleteGoogleChat_RetriesWhenOverloaded(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"This model is currently experiencing high demand."}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer srv.Close()
	prevURL, prevDelay := googleGenerateURL, aiOverloadedRetryDelay
	googleGenerateURL, aiOverloadedRetryDelay = srv.URL+"/%s", 0
	defer func() { googleGenerateURL, aiOverloadedRetryDelay = prevURL, prevDelay }()

	app := &App{Log: testutil.NopLogger(), HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{}
	settings.AI.Provider = models.AIProviderGoogle
	settings.AI.APIKey = "k"
	settings.AI.Model = "gemini-test"

	out, err := app.completeAIChat(settings, "sys", "hi", 100, models.AIUsagePurposeCannedResponseGenerate)
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
	assert.Equal(t, int32(3), calls.Load())

	calls.Store(-10)
	_, err = app.completeAIChat(settings, "sys", "hi", 100, models.AIUsagePurposeCannedResponseGenerate)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "high demand")
	assert.Equal(t, int32(-7), calls.Load(), "gives up after two retries")
}

func TestUniqueCannedNameAndShortcut(t *testing.T) {
	t.Parallel()
	existing := []models.CannedResponse{
		{Name: "Welcome", Shortcut: "hi"},
		{Name: "Welcome (2)", Shortcut: "hi2"},
	}
	name, shortcut := uniqueCannedNameAndShortcut("welcome", "hi", existing)
	assert.Equal(t, "welcome (3)", name)
	assert.Equal(t, "hi3", shortcut)

	name, shortcut = uniqueCannedNameAndShortcut("Goodbye", "", existing)
	assert.Equal(t, "Goodbye", name)
	assert.Empty(t, shortcut)
}
