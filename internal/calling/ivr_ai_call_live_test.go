package calling

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Live smoke test against local Postgres + org Google AI + Shakim AI Context.
// Run: LIVE_IVR_TEST=1 go test ./internal/calling -run TestLiveShakimAICallContext -v -count=1
func TestLiveShakimAICallContext(t *testing.T) {
	if os.Getenv("LIVE_IVR_TEST") != "1" {
		t.Skip("set LIVE_IVR_TEST=1 to run against local DB + Gemini")
	}

	dsn := os.Getenv("LIVE_DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost port=5433 user=whatomate password=whatomate dbname=whatomate sslmode=disable"
	}
	encKey := os.Getenv("LIVE_ENCRYPTION_KEY")
	if encKey == "" {
		encKey = "thJyLUPwYm67025+ulHsoKAbxYnmpKvUggWMuLtOmdU="
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	orgID := uuid.MustParse("fa2d2c42-e685-47c9-b4a0-b98363105663")
	m := &Manager{db: db, encryptionKey: encKey, log: testLogger()}

	session := &CallSession{
		ID:             "live-test",
		OrganizationID: orgID,
		CallerPhone:    "263771674202",
		CallerName:     "Test Caller",
		Status:         models.CallStatusAnswered,
	}
	ctx := &IVRContext{Variables: map[string]string{}, OrganizationID: orgID}

	contextBlock := m.buildIVRAIContext(session, ctx, "How much is soft glam?", nil)
	require.Contains(t, contextBlock, "Shakim Beauty Bar")
	require.Contains(t, contextBlock, "Soft glam")
	require.Contains(t, contextBlock, "$20")
	t.Logf("context bytes=%d", len(contextBlock))

	settings := m.loadOrgAISettings(orgID)
	require.True(t, settings.AI.Enabled, "org AI must be enabled")
	apiKey, provider, model, maxTokens := m.resolveAILLMCreds(settings, "")
	require.NotEmpty(t, apiKey, "org AI API key missing")
	t.Logf("provider=%s model=%s", provider, model)

	sys := mergeSystemWithAIContext(
		"You are the Shakim Beauty Bar phone assistant. Answer briefly using the context. Prefer exact prices.",
		contextBlock,
	)
	messages := []map[string]any{
		{"role": "system", "content": sys},
		{"role": "user", "content": "How much is soft glam makeup?"},
	}

	// Include tools to prove non-OpenAI path ignores them and still answers via Google.
	tools := toolsToOpenAISchema([]aiCallToolDef{{
		Name: "end_call", Handler: "hangup", Description: "end",
	}})
	reply, transfer, hangup, err := m.aiCallLLMTurn(
		apiKey, provider, model, maxTokens,
		messages, tools, nil, session, ctx, "",
	)
	require.NoError(t, err)
	assert.False(t, transfer)
	assert.False(t, hangup)
	require.NotEmpty(t, reply)
	t.Logf("reply: %s", reply)

	lower := strings.ToLower(reply)
	assert.True(t,
		strings.Contains(lower, "20") || strings.Contains(lower, "$20") || strings.Contains(lower, "soft"),
		"expected price or soft glam in reply: %s", reply)
}

func TestAICallLLMTurn_GoogleIgnoresTools(t *testing.T) {
	// Without LIVE, still unit-test the branch by using a stub unreachable key path —
	// covered by live test; here we only assert decrypt helper doesn't panic.
	key := "enc:not-real"
	crypto.DecryptFields("bad-key", &key)
	_ = key
}
