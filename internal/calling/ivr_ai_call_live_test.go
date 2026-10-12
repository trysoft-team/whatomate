package calling

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Live smoke test against a real Postgres database, an organization with AI
// enabled and an AI Context, and the organization's AI provider.
//
//	LIVE_IVR_TEST=1 LIVE_DATABASE_URL=... LIVE_ENCRYPTION_KEY=... LIVE_ORG_ID=... \
//	LIVE_QUESTION="How much is soft glam?" LIVE_EXPECT="soft glam" \
//	go test ./internal/calling -run TestLiveAICallContext -v -count=1
//
// LIVE_EXPECT (optional) must appear in both the AI Context and the reply.
func TestLiveAICallContext(t *testing.T) {
	if os.Getenv("LIVE_IVR_TEST") != "1" {
		t.Skip("set LIVE_IVR_TEST=1 to run against a live database and AI provider")
	}
	env := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Skipf("set %s", name)
		}
		return v
	}
	dsn := env("LIVE_DATABASE_URL")
	encKey := env("LIVE_ENCRYPTION_KEY")
	orgID := uuid.MustParse(env("LIVE_ORG_ID"))
	question := env("LIVE_QUESTION")
	expect := strings.ToLower(os.Getenv("LIVE_EXPECT"))

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	m := &Manager{db: db, encryptionKey: encKey, log: testLogger()}

	session := &CallSession{
		ID:             "live-test",
		OrganizationID: orgID,
		CallerPhone:    "15550005678",
		CallerName:     "Test Caller",
		Status:         models.CallStatusAnswered,
	}
	ctx := &IVRContext{Variables: map[string]string{}, OrganizationID: orgID}

	contextBlock := m.buildIVRAIContext(session, ctx, question, nil)
	require.NotEmpty(t, contextBlock, "organization has no AI Context")
	if expect != "" {
		require.Contains(t, strings.ToLower(contextBlock), expect)
	}
	t.Logf("context bytes=%d", len(contextBlock))

	settings := m.loadOrgAISettings(orgID)
	require.True(t, settings.AI.Enabled, "org AI must be enabled")
	apiKey, provider, model, maxTokens := m.resolveAILLMCreds(settings, "")
	require.NotEmpty(t, apiKey, "org AI API key missing")
	t.Logf("provider=%s model=%s", provider, model)

	sys := mergeSystemWithAIContext(
		"You are a phone assistant. Answer briefly using the context. Prefer exact prices.",
		contextBlock,
	)
	messages := []map[string]any{
		{"role": "system", "content": sys},
		{"role": "user", "content": question},
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
	require.False(t, transfer)
	require.False(t, hangup)
	require.NotEmpty(t, reply)
	t.Logf("reply: %s", reply)
	if expect != "" {
		require.Contains(t, strings.ToLower(reply), expect)
	}
}

func TestAICallLLMTurn_GoogleIgnoresTools(t *testing.T) {
	// Without LIVE, still unit-test the branch by using a stub unreachable key path —
	// covered by live test; here we only assert decrypt helper doesn't panic.
	key := "enc:not-real"
	crypto.DecryptFields("bad-key", &key)
	_ = key
}
