package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
)

// Test exports for AI usage helpers (kept out of production call sites).

func ParseOpenAIUsageForTest(body []byte) (int, int, int)    { return parseOpenAIUsage(body) }
func ParseAnthropicUsageForTest(body []byte) (int, int, int) { return parseAnthropicUsage(body) }
func ParseGoogleUsageForTest(body []byte) (int, int, int)    { return parseGoogleUsage(body) }

func RecordAIUsageForTest(a *App, settings *models.ChatbotSettings, session *models.ChatbotSession, purpose, provider, model string, prompt, completion, total int) {
	a.recordAIUsage(aiUsageContext{Settings: settings, Session: session, Purpose: purpose}, provider, model, prompt, completion, total)
}

// SetGoogleGenerateURLForTest points Gemini calls at a fake server and returns a restore func.
func SetGoogleGenerateURLForTest(u string) func() {
	prev := googleGenerateURL
	googleGenerateURL = u
	return func() { googleGenerateURL = prev }
}

func QueryWidgetMetricForTest(a *App, orgID uuid.UUID, widget models.Widget, filters []FilterInput, start, end, prevStart, prevEnd time.Time) (float64, float64, bool) {
	return a.queryWidgetMetric(orgID, widget, filters, start, end, prevStart, prevEnd)
}
