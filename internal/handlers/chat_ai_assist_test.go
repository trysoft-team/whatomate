package handlers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func TestChatAIMessageText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		msg  models.Message
		want string
	}{
		{models.Message{MessageType: models.MessageTypeText, Content: "  hello\n\nthere  "}, "hello there"},
		{models.Message{MessageType: models.MessageTypeTemplate, TemplateName: "order_update", Content: "Your order shipped"}, "[Template order_update] Your order shipped"},
		{models.Message{MessageType: models.MessageTypeAudio}, "[Voice note]"},
		{models.Message{MessageType: models.MessageTypeAudio, Metadata: models.JSONB{"transcript": "call me back"}}, "[Voice note, transcribed] call me back"},
		{models.Message{MessageType: models.MessageTypeImage, Content: "receipt"}, "[image] receipt"},
		{models.Message{MessageType: models.MessageTypeLocation}, "[location]"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, chatAIMessageText(c.msg))
	}
	long := models.Message{MessageType: models.MessageTypeText, Content: string(make([]rune, 2000))}
	assert.LessOrEqual(t, len([]rune(chatAIMessageText(long))), chatAIMessageMaxRunes)
}

func TestParseChatAISummary(t *testing.T) {
	t.Parallel()
	s, err := parseChatAISummary("```json\n" + `{"summary":"Customer wants a refund for order 123.","key_points":["Order 123","Order 123"," ","Paid by card"],"open_questions":["Refund timeline"],"sentiment":"FRUSTRATED"}` + "\n```")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order 123", "Paid by card"}, s.KeyPoints)
	assert.Equal(t, "frustrated", s.Sentiment)

	text := formatHandoffSummary(s)
	assert.Contains(t, text, "Customer wants a refund")
	assert.Contains(t, text, "Key points:\n• Order 123\n• Paid by card")
	assert.Contains(t, text, "Open:\n• Refund timeline")
	assert.Contains(t, text, "Mood: frustrated")

	s, err = parseChatAISummary(`{"summary":"ok","sentiment":"meh"}`)
	require.NoError(t, err)
	assert.Equal(t, "neutral", s.Sentiment)
	assert.NotContains(t, formatHandoffSummary(s), "Mood")

	_, err = parseChatAISummary(`{"summary":""}`)
	assert.Error(t, err)
	_, err = parseChatAISummary("no json")
	assert.Error(t, err)
}

func TestParseChatAITagsFields(t *testing.T) {
	t.Parallel()
	raw := `{"tags":["VIP","vip","billing","refund request","extra one","another new","already"],
		"fields":{"email":"not-an-email","company":"Acme Ltd","profile_name":"Jane Doe","city":"Paris","favourite_color":"blue"}}`
	res, err := parseChatAITagsFields(raw,
		[]string{"VIP", "Billing"},
		[]string{"already"},
		map[string]string{"profile_name": "jane doe", "city": ""})
	require.NoError(t, err)
	assert.Equal(t, []string{"VIP", "Billing", "refund request", "extra one"}, res.Tags,
		"existing tags keep their spelling, at most 2 new tags, current tags skipped")

	keys := map[string]string{}
	for _, f := range res.Fields {
		keys[f.Key] = f.Value
	}
	assert.Equal(t, map[string]string{"company": "Acme Ltd", "city": "Paris"}, keys,
		"invalid email, unchanged name and unknown fields are dropped")
}

func TestParseChatAITemplateSuggestion(t *testing.T) {
	t.Parallel()
	tpl := models.Template{
		BaseModel:   models.BaseModel{ID: uuid.New()},
		Name:        "order_followup",
		DisplayName: "Order follow-up",
		BodyContent: "Hi {{name}}, about order {{ order_id }} — {{name}}",
	}
	res, err := parseChatAITemplateSuggestion(`{"template_name":"order_followup","params":{"name":"Jane","order_id":"","hack":"x"},"reason":"Continues the order thread"}`,
		[]models.Template{tpl})
	require.NoError(t, err)
	assert.Equal(t, tpl.ID.String(), res.TemplateID)
	assert.Equal(t, "Order follow-up", res.DisplayName)
	assert.Equal(t, map[string]string{"name": "Jane"}, res.Params, "unknown and empty params dropped")

	_, err = parseChatAITemplateSuggestion(`{"template_name":"made_up"}`, []models.Template{tpl})
	assert.Error(t, err)
	assert.Equal(t, []string{"name", "order_id"}, templateVariableNames(tpl.BodyContent))
}

func TestCleanAIMessageText(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Hello there", cleanAIMessageText(`"Hello there"`))
	assert.Equal(t, "Hello there", cleanAIMessageText("“Hello there”"))
	assert.Equal(t, "Sure!", cleanAIMessageText("```\nSure!\n```"))
	assert.Equal(t, `"A" and "B"`, cleanAIMessageText(`"A" and "B"`), "inner quotes keep the outer ones")
}

func TestAudioMimeType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "audio/ogg", audioMimeType("audio/ogg; codecs=opus", "x.ogg"))
	assert.Equal(t, "audio/mpeg", audioMimeType("", "voice.mp3"))
	assert.Equal(t, "audio/ogg", audioMimeType("application/octet-stream", "voice"))
}

func TestCompleteGoogleChat_QuotaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"You exceeded your current quota","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer srv.Close()
	prev := googleGenerateURL
	googleGenerateURL = srv.URL + "/models/%s:generateContent"
	defer func() { googleGenerateURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderGoogle, Model: "gemini-test", APIKey: "k"}}
	_, err := app.completeAIChat(settings, "sys", "hi", 100, models.AIUsagePurposeChatAssist)
	require.Error(t, err)
	assert.ErrorIs(t, err, errAIQuotaExceeded)
	assert.Contains(t, aiQuotaMessage(settings), "gemini-test")

	_, err = app.transcribeGoogle(settings, []byte("a"), "audio/ogg", transcribePrompt)
	assert.ErrorIs(t, err, errAIQuotaExceeded)
}

func TestTranscribeGoogle_SendsAudioAndKeyInHeader(t *testing.T) {
	var gotKey, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		gotPath = r.URL.String()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hello "},{"text":"world"}]}}]}`))
	}))
	defer srv.Close()
	prev := googleGenerateURL
	googleGenerateURL = srv.URL + "/models/%s:generateContent"
	defer func() { googleGenerateURL = prev }()

	app := &App{HTTPClient: srv.Client()}
	settings := &models.ChatbotSettings{AI: models.AIConfig{Provider: models.AIProviderGoogle, Model: "gemini-test", APIKey: "secret-key"}}
	text, err := app.transcribeGoogle(settings, []byte("OggS-audio"), "audio/ogg", transcribePrompt)
	require.NoError(t, err)
	assert.Equal(t, "hello world", text)
	assert.Equal(t, "secret-key", gotKey)
	assert.NotContains(t, gotPath, "secret-key", "API key must not be in the URL")

	parts := gotBody["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	inline := parts[0].(map[string]any)["inlineData"].(map[string]any)
	assert.Equal(t, "audio/ogg", inline["mimeType"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("OggS-audio")), inline["data"])
}
