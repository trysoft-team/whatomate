package handlers

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestGenerateHandoffSummary(t *testing.T) {
	var calls atomic.Int32
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotKey = r.Header.Get("x-goog-api-key")
		gotPath = r.URL.String()
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"Customer wants to rebook.\",\"key_points\":[\"Order 42\"],\"open_questions\":[\"New date?\"],\"sentiment\":\"frustrated\"}"}]}}]}`))
	}))
	defer srv.Close()
	prev := googleGenerateURL
	googleGenerateURL = srv.URL + "/models/%s:generateContent"
	defer func() { googleGenerateURL = prev }()

	db := testutil.SetupTestDB(t)
	app := &App{
		DB:         db,
		Log:        testutil.NopLogger(),
		HTTPClient: srv.Client(),
		Config:     &config.Config{JWT: config.JWTConfig{Secret: testutil.TestJWTSecret}},
	}
	org := testutil.CreateTestOrganization(t, db)
	contact := testutil.CreateTestContact(t, db, org.ID)

	settings := &models.ChatbotSettings{OrganizationID: org.ID}
	settings.AI.Enabled = true
	settings.AI.Provider = models.AIProviderGoogle
	settings.AI.APIKey = "secret-key"
	settings.AI.Model = "gemini-test"
	require.NoError(t, db.Create(settings).Error)

	newTransfer := func() *models.AgentTransfer {
		tr := &models.AgentTransfer{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			ContactID:       contact.ID,
			WhatsAppAccount: "test",
			PhoneNumber:     contact.PhoneNumber,
		}
		require.NoError(t, db.Create(tr).Error)
		return tr
	}
	summaryOf := func(id uuid.UUID) string {
		var tr models.AgentTransfer
		require.NoError(t, db.First(&tr, "id = ?", id).Error)
		return tr.AISummary
	}
	addMessage := func(dir models.Direction, text string, at time.Time) {
		require.NoError(t, db.Create(&models.Message{
			BaseModel:       models.BaseModel{ID: uuid.New(), CreatedAt: at},
			OrganizationID:  org.ID,
			ContactID:       contact.ID,
			WhatsAppAccount: "test",
			Direction:       dir,
			MessageType:     models.MessageTypeText,
			Content:         text,
		}).Error)
	}

	t.Run("skips chats with too few messages", func(t *testing.T) {
		addMessage(models.DirectionIncoming, "hi", time.Now().Add(-2*time.Minute))
		tr := newTransfer()
		app.generateHandoffSummary(tr)
		assert.Equal(t, int32(0), calls.Load())
		assert.Empty(t, summaryOf(tr.ID))
	})

	addMessage(models.DirectionIncoming, "I need to move my appointment, order 42", time.Now().Add(-time.Minute))

	t.Run("stores the formatted summary", func(t *testing.T) {
		tr := newTransfer()
		app.generateHandoffSummary(tr)
		assert.Equal(t, int32(1), calls.Load())
		assert.Equal(t, "secret-key", gotKey)
		assert.NotContains(t, gotPath, "secret-key")
		got := summaryOf(tr.ID)
		assert.Contains(t, got, "Customer wants to rebook.")
		assert.Contains(t, got, "• Order 42")
		assert.Contains(t, got, "• New date?")
		assert.Contains(t, got, "Mood: frustrated")
	})

	t.Run("respects the setting", func(t *testing.T) {
		require.NoError(t, db.Model(&models.ChatbotSettings{}).Where("id = ?", settings.ID).
			Update("ai_handoff_summary", false).Error)
		tr := newTransfer()
		app.generateHandoffSummary(tr)
		assert.Equal(t, int32(1), calls.Load(), "no AI call when handoff summaries are off")
		assert.Empty(t, summaryOf(tr.ID))
	})
}
