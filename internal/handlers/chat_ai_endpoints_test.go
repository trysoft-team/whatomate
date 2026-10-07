package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func chatAIPermissions(t *testing.T, app *handlers.App, resources ...string) []models.Permission {
	t.Helper()
	want := map[string]bool{}
	for _, r := range resources {
		want[r] = true
	}
	var out []models.Permission
	for _, p := range testutil.GetOrCreateTestPermissions(t, app.DB) {
		if want[p.Resource] {
			out = append(out, p)
		}
	}
	require.NotEmpty(t, out)
	return out
}

func enableTestAI(t *testing.T, app *handlers.App, orgID uuid.UUID) {
	t.Helper()
	require.NoError(t, app.DB.Where("organization_id = ? AND whats_app_account = ?", orgID, "").
		Delete(&models.ChatbotSettings{}).Error)
	settings := &models.ChatbotSettings{OrganizationID: orgID}
	settings.AI.Enabled = true
	settings.AI.Provider = models.AIProviderGoogle
	settings.AI.APIKey = "test-key"
	settings.AI.Model = "gemini-test"
	require.NoError(t, app.DB.Create(settings).Error)
	app.InvalidateChatbotSettingsCache(orgID)
}

func callChatAIAssist(t *testing.T, app *handlers.App, orgID, userID, contactID uuid.UUID, body map[string]any) (int, string) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", contactID.String())
	require.NoError(t, app.ChatAIAssist(req))
	var resp struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Message
}

func TestChatAIAssist_Validation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Chat AI Admin", false, false,
		chatAIPermissions(t, app, models.ResourceChat, models.ResourceContacts))
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("chat-ai")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	status, msg := callChatAIAssist(t, app, org.ID, user.ID, contact.ID, map[string]any{"mode": "suggest_reply"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, msg, "AI is not configured")

	enableTestAI(t, app, org.ID)

	status, msg = callChatAIAssist(t, app, org.ID, user.ID, contact.ID, map[string]any{"mode": "hack"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Equal(t, "Unknown mode", msg)

	status, msg = callChatAIAssist(t, app, org.ID, user.ID, contact.ID, map[string]any{"mode": "rewrite", "tone": "friendly", "draft": "  "})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, msg, "Type a message")

	status, msg = callChatAIAssist(t, app, org.ID, user.ID, contact.ID, map[string]any{"mode": "suggest_reply"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, msg, "no messages")

	status, _ = callChatAIAssist(t, app, org.ID, user.ID, uuid.New(), map[string]any{"mode": "suggest_reply"})
	assert.Equal(t, fasthttp.StatusNotFound, status)
}

func TestChatAIAssist_AgentOnlySeesAssignedContacts(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	enableTestAI(t, app, org.ID)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Chat AI Agent", false, false,
		chatAIPermissions(t, app, models.ResourceChat))
	agent := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("chat-ai-agent")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)
	other := testutil.CreateTestContact(t, app.DB, org.ID)

	status, _ := callChatAIAssist(t, app, org.ID, agent.ID, other.ID, map[string]any{"mode": "summarize"})
	assert.Equal(t, fasthttp.StatusNotFound, status)
}

func TestTranscribeMessage_ValidationAndCache(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	enableTestAI(t, app, org.ID)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Chat AI Transcribe", false, false,
		chatAIPermissions(t, app, models.ResourceChat, models.ResourceContacts))
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("chat-ai-tx")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	newMsg := func(typ models.MessageType, meta models.JSONB) *models.Message {
		m := &models.Message{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			ContactID:       contact.ID,
			WhatsAppAccount: "test",
			Direction:       models.DirectionIncoming,
			MessageType:     typ,
			Content:         "hi",
			MediaURL:        "audio/voice.ogg",
			Metadata:        meta,
		}
		require.NoError(t, app.DB.Create(m).Error)
		return m
	}
	call := func(id uuid.UUID) (int, []byte) {
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", id.String())
		require.NoError(t, app.TranscribeMessage(req))
		return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
	}

	status, _ := call(newMsg(models.MessageTypeText, nil).ID)
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	status, body := call(newMsg(models.MessageTypeAudio, models.JSONB{"transcript": "call me tomorrow"}).ID)
	require.Equal(t, fasthttp.StatusOK, status)
	var resp struct {
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "call me tomorrow", resp.Data.Text)

	status, _ = call(uuid.New())
	assert.Equal(t, fasthttp.StatusNotFound, status)
}
