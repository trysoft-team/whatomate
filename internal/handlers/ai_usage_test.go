package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestParseProviderUsage(t *testing.T) {
	p, c, tot := handlers.ParseOpenAIUsageForTest([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	assert.Equal(t, 10, p)
	assert.Equal(t, 5, c)
	assert.Equal(t, 15, tot)

	p, c, tot = handlers.ParseAnthropicUsageForTest([]byte(`{"usage":{"input_tokens":8,"output_tokens":4}}`))
	assert.Equal(t, 8, p)
	assert.Equal(t, 4, c)
	assert.Equal(t, 12, tot)

	p, c, tot = handlers.ParseGoogleUsageForTest([]byte(`{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`))
	assert.Equal(t, 7, p)
	assert.Equal(t, 3, c)
	assert.Equal(t, 10, tot)
}

func TestRecordAIUsageAndWidgetQuery(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	settings := &models.ChatbotSettings{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		WhatsAppAccount: "test-account",
		AI: models.AIConfig{
			Enabled:  true,
			Provider: models.AIProviderOpenAI,
			Model:    "gpt-4o-mini",
		},
	}
	require.NoError(t, app.DB.Create(settings).Error)

	handlers.RecordAIUsageForTest(app, settings, nil, models.AIUsagePurposeChatbotReply,
		string(models.AIProviderOpenAI), "gpt-4o-mini", 100, 50, 150)

	var logs []models.AIUsageLog
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).Find(&logs).Error)
	require.Len(t, logs, 1)
	assert.Equal(t, 100, logs[0].PromptTokens)
	assert.Equal(t, 50, logs[0].CompletionTokens)
	assert.Equal(t, 150, logs[0].TotalTokens)

	now := time.Now().UTC()
	widget := models.Widget{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		Name:           "AI Tokens",
		DataSource:     "ai",
		Metric:         "sum",
		Field:          "total_tokens",
		DisplayType:    "number",
	}
	val, prev, stub := handlers.QueryWidgetMetricForTest(app, org.ID, widget, nil,
		now.Add(-24*time.Hour), now.Add(time.Hour),
		now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	assert.False(t, stub)
	assert.Equal(t, float64(150), val)
	assert.Equal(t, float64(0), prev)
}
