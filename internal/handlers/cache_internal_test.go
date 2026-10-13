package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
)

// cacheTestApp builds a minimal App configured for cache tests, with a test
// database and an in-memory cache.
func cacheTestApp(t *testing.T) *App {
	t.Helper()
	db := testutil.SetupTestDB(t)
	mem := store.NewMemory()
	t.Cleanup(mem.Close)
	return &App{
		DB:    db,
		Log:   testutil.NopLogger(),
		Cache: mem,
		Config: &config.Config{
			JWT: config.JWTConfig{Secret: testutil.TestJWTSecret, AccessExpiryMins: 15, RefreshExpiryDays: 7},
		},
	}
}

// makeAccount inserts a WhatsApp account with the given phone ID and tokens.
func makeAccount(t *testing.T, app *App, orgID uuid.UUID, phoneID, accessToken, appSecret string) *models.WhatsAppAccount {
	t.Helper()
	acc := &models.WhatsAppAccount{
		BaseModel:          models.BaseModel{ID: uuid.New()},
		OrganizationID:     orgID,
		Name:               "cache-acc-" + uuid.New().String()[:8],
		PhoneID:            phoneID,
		BusinessID:         "biz-" + uuid.New().String()[:8],
		AccessToken:        accessToken,
		AppSecret:          appSecret,
		WebhookVerifyToken: "vt-" + uuid.New().String()[:8],
		APIVersion:         "v18.0",
		Status:             "active",
	}
	require.NoError(t, app.DB.Create(acc).Error)
	return acc
}

// --- getWhatsAppAccountCached ---

func TestGetWhatsAppAccountCached_CacheMissPopulatesCache(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	phoneID := "phone-" + uuid.New().String()[:8]
	acc := makeAccount(t, app, org.ID, phoneID, "tok", "secret")

	// Ensure cache is clean.
	cacheKey := fmt.Sprintf("%s%s", whatsappAccountCachePrefix, phoneID)
	_ = app.Cache.Del(context.Background(), cacheKey)

	got, err := app.getWhatsAppAccountCached(phoneID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, acc.ID, got.ID)
	assert.Equal(t, "tok", got.AccessToken, "DB → cache path must return decrypted access token")
	assert.Equal(t, "secret", got.AppSecret)

	// Cache should now be populated.
	cached, err := app.Cache.Get(context.Background(), cacheKey)
	require.NoError(t, err)
	require.NotEmpty(t, cached)
}

func TestGetWhatsAppAccountCached_CacheHitSkipsDB(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	phoneID := "phone-" + uuid.New().String()[:8]
	acc := makeAccount(t, app, org.ID, phoneID, "real-tok", "real-secret")

	// Plant a fake cache entry with a different name → if the function reads from
	// cache, we'll see "from-cache"; if it falls through to DB we'll see acc.Name.
	cacheData := whatsAppAccountCache{
		WhatsAppAccount: models.WhatsAppAccount{
			BaseModel:      models.BaseModel{ID: acc.ID},
			OrganizationID: org.ID,
			Name:           "from-cache",
			PhoneID:        phoneID,
		},
		AccessToken: "cached-tok",
		AppSecret:   "cached-secret",
	}
	data, err := json.Marshal(cacheData)
	require.NoError(t, err)
	cacheKey := fmt.Sprintf("%s%s", whatsappAccountCachePrefix, phoneID)
	require.NoError(t, app.Cache.Set(context.Background(), cacheKey, data, time.Hour))

	got, err := app.getWhatsAppAccountCached(phoneID)
	require.NoError(t, err)
	assert.Equal(t, "from-cache", got.Name, "cache hit must short-circuit the DB read")
	assert.Equal(t, "cached-tok", got.AccessToken,
		"cached AccessToken must be restored on the WhatsAppAccount even though it has json:\"-\"")
	assert.Equal(t, "cached-secret", got.AppSecret,
		"cached AppSecret must be restored")
}

func TestGetWhatsAppAccountCached_NotFoundReturnsError(t *testing.T) {
	app := cacheTestApp(t)
	_, err := app.getWhatsAppAccountCached("phone-does-not-exist-" + uuid.New().String()[:8])
	require.Error(t, err)
}

func TestInvalidateWhatsAppAccountCache_DeletesKey(t *testing.T) {
	app := cacheTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	phoneID := "phone-" + uuid.New().String()[:8]
	makeAccount(t, app, org.ID, phoneID, "tok", "secret")

	// Populate the cache.
	_, err := app.getWhatsAppAccountCached(phoneID)
	require.NoError(t, err)

	cacheKey := fmt.Sprintf("%s%s", whatsappAccountCachePrefix, phoneID)
	_, err = app.Cache.Get(context.Background(), cacheKey)
	require.NoError(t, err)

	app.InvalidateWhatsAppAccountCache(phoneID)

	_, err = app.Cache.Get(context.Background(), cacheKey)
	assert.ErrorIs(t, err, store.ErrNotFound, "invalidation must remove the cache key")
}

// --- decryptAccountSecrets handles encrypted + legacy plaintext ---

func TestDecryptAccountSecrets_DecryptsEncryptedValues(t *testing.T) {
	app := cacheTestApp(t)
	app.Config.App.EncryptionKey = "this-is-a-32-character-test-key-XX"

	encTok, err := crypto.Encrypt("plain-token", app.Config.App.EncryptionKey)
	require.NoError(t, err)
	encSecret, err := crypto.Encrypt("plain-secret", app.Config.App.EncryptionKey)
	require.NoError(t, err)

	acc := &models.WhatsAppAccount{AccessToken: encTok, AppSecret: encSecret}
	app.decryptAccountSecrets(acc)
	assert.Equal(t, "plain-token", acc.AccessToken)
	assert.Equal(t, "plain-secret", acc.AppSecret)
}

func TestDecryptAccountSecrets_LeavesLegacyPlaintextUnchanged(t *testing.T) {
	app := cacheTestApp(t)
	app.Config.App.EncryptionKey = "this-is-a-32-character-test-key-XX"

	// No "enc:" prefix → legacy value, returned as-is.
	acc := &models.WhatsAppAccount{AccessToken: "legacy-plain", AppSecret: "legacy-secret"}
	app.decryptAccountSecrets(acc)
	assert.Equal(t, "legacy-plain", acc.AccessToken)
	assert.Equal(t, "legacy-secret", acc.AppSecret)
}

// --- getChatbotSettingsCached returns usable AI keys ---

func TestGetChatbotSettingsCached_DecryptsAIKeyOnDBAndCachePaths(t *testing.T) {
	app := cacheTestApp(t)
	app.Config.App.EncryptionKey = "this-is-a-32-character-test-key-XX"
	org := testutil.CreateTestOrganization(t, app.DB)

	encKey, err := crypto.Encrypt("sk-plain-ai-key", app.Config.App.EncryptionKey)
	require.NoError(t, err)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		AI:             models.AIConfig{Enabled: true, Provider: models.AIProviderGoogle, APIKey: encKey, Model: "m"},
	}).Error)

	cacheKey := fmt.Sprintf("%s%s:%s", settingsCachePrefix, org.ID.String(), "")
	_ = app.Cache.Del(context.Background(), cacheKey)

	fromDB, err := app.getChatbotSettingsCached(org.ID, "")
	require.NoError(t, err)
	assert.Equal(t, "sk-plain-ai-key", fromDB.AI.APIKey)

	cached, err := app.Cache.Get(context.Background(), cacheKey)
	require.NoError(t, err)
	assert.NotContains(t, string(cached), "sk-plain-ai-key", "The cache must keep the encrypted key")

	fromCache, err := app.getChatbotSettingsCached(org.ID, "")
	require.NoError(t, err)
	assert.Equal(t, "sk-plain-ai-key", fromCache.AI.APIKey)
}

// --- getWebhooksCached ---

func TestGetWebhooksCached_OnlyActiveAndOrgScoped(t *testing.T) {
	app := cacheTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)

	// Active webhook in target org.
	require.NoError(t, app.DB.Create(&models.Webhook{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgA.ID,
		Name: "active-A", URL: "https://a.example.com", IsActive: true,
	}).Error)
	// Inactive webhook in target org — must be excluded. GORM's default:true tag
	// makes Create ignore an explicit `false`; insert active first, then flip.
	inactive := &models.Webhook{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgA.ID,
		Name: "inactive-A", URL: "https://a2.example.com", IsActive: true,
	}
	require.NoError(t, app.DB.Create(inactive).Error)
	require.NoError(t, app.DB.Model(inactive).Update("is_active", false).Error)
	// Active webhook in other org — must be excluded.
	require.NoError(t, app.DB.Create(&models.Webhook{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgB.ID,
		Name: "active-B", URL: "https://b.example.com", IsActive: true,
	}).Error)

	// Clear cache to force DB read.
	app.InvalidateWebhooksCache(orgA.ID)

	got, err := app.getWebhooksCached(orgA.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "active-A", got[0].Name)
}

func TestGetWebhooksCached_CacheHitSkipsDB(t *testing.T) {
	app := cacheTestApp(t)
	orgID := uuid.New()

	// Plant a synthetic cache entry — there is no matching row in the DB.
	cacheKey := fmt.Sprintf("%s%s", webhooksCachePrefix, orgID.String())
	cached := []models.Webhook{{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID,
		Name: "from-cache", IsActive: true,
	}}
	data, err := json.Marshal(cached)
	require.NoError(t, err)
	require.NoError(t, app.Cache.Set(context.Background(), cacheKey, data, time.Hour))

	got, err := app.getWebhooksCached(orgID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "from-cache", got[0].Name, "cache hit must skip the DB read")
}

func TestInvalidateWebhooksCache_DeletesKey(t *testing.T) {
	app := cacheTestApp(t)
	orgID := uuid.New()
	cacheKey := fmt.Sprintf("%s%s", webhooksCachePrefix, orgID.String())
	require.NoError(t, app.Cache.Set(context.Background(), cacheKey, []byte("[]"), time.Hour))

	app.InvalidateWebhooksCache(orgID)

	_, err := app.Cache.Get(context.Background(), cacheKey)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// --- getSLAEnabledSettingsCached ---

func TestGetSLAEnabledSettingsCached_OnlySLAEnabledRows(t *testing.T) {
	app := cacheTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)

	// Wipe any pre-existing rows so we can assert exact counts.
	require.NoError(t, app.DB.Exec("DELETE FROM chatbot_settings").Error)
	app.InvalidateSLASettingsCache()

	require.NoError(t, app.DB.Create(&models.ChatbotSettings{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgA.ID,
		WhatsAppAccount: "acc-A", IsEnabled: true,
		SLA: models.SLAConfig{Enabled: true},
	}).Error)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgB.ID,
		WhatsAppAccount: "acc-B", IsEnabled: true,
		SLA: models.SLAConfig{Enabled: true},
	}).Error)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgA.ID,
		WhatsAppAccount: "acc-A2", IsEnabled: true,
		SLA: models.SLAConfig{Enabled: false},
	}).Error)

	got, err := app.getSLAEnabledSettingsCached()
	require.NoError(t, err)
	require.Len(t, got, 2, "only SLA-enabled rows must be returned, regardless of org")
	for _, s := range got {
		assert.True(t, s.SLA.Enabled)
	}
}

func TestInvalidateSLASettingsCache_DeletesKey(t *testing.T) {
	app := cacheTestApp(t)
	require.NoError(t, app.Cache.Set(context.Background(), slaSettingsCacheKey, []byte("[]"), time.Hour))

	app.InvalidateSLASettingsCache()

	_, err := app.Cache.Get(context.Background(), slaSettingsCacheKey)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// --- cacheDelPrefix ---

func TestCacheDelPrefix_RemovesAllMatchingKeys(t *testing.T) {
	app := cacheTestApp(t)
	ctx := context.Background()

	prefix := "test:cache:" + uuid.New().String()[:8] + ":"
	for i := range 5 {
		require.NoError(t, app.Cache.Set(ctx, fmt.Sprintf("%s%d", prefix, i), []byte("x"), time.Hour))
	}
	// Sentinel key with a similar but non-matching prefix.
	other := "test:other:" + uuid.New().String()[:8]
	require.NoError(t, app.Cache.Set(ctx, other, []byte("y"), time.Hour))

	app.cacheDelPrefix(ctx, prefix)

	for i := range 5 {
		_, err := app.Cache.Get(ctx, fmt.Sprintf("%s%d", prefix, i))
		assert.ErrorIs(t, err, store.ErrNotFound, "key %d should have been deleted", i)
	}
	_, err := app.Cache.Get(ctx, other)
	assert.NoError(t, err, "non-matching key must be left alone")
}

// --- InvalidateChatbotFlowsCache (uses Del on a single key) ---

func TestInvalidateChatbotFlowsCache_DeletesKey(t *testing.T) {
	app := cacheTestApp(t)
	orgID := uuid.New()
	cacheKey := fmt.Sprintf("%s%s", flowsCachePrefix, orgID.String())
	require.NoError(t, app.Cache.Set(context.Background(), cacheKey, []byte("[]"), time.Hour))

	app.InvalidateChatbotFlowsCache(orgID)

	_, err := app.Cache.Get(context.Background(), cacheKey)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// --- InvalidateChatbotSettingsCache (pattern-based) ---

func TestInvalidateChatbotSettingsCache_DeletesAllAccountVariants(t *testing.T) {
	app := cacheTestApp(t)
	ctx := context.Background()
	orgID := uuid.New()

	// Plant several account-specific cache keys for the same org.
	keyA := fmt.Sprintf("%s%s:%s", settingsCachePrefix, orgID.String(), "acc-A")
	keyB := fmt.Sprintf("%s%s:%s", settingsCachePrefix, orgID.String(), "acc-B")
	keyOther := fmt.Sprintf("%s%s:%s", settingsCachePrefix, uuid.New().String(), "acc-X")
	require.NoError(t, app.Cache.Set(ctx, keyA, []byte("{}"), time.Hour))
	require.NoError(t, app.Cache.Set(ctx, keyB, []byte("{}"), time.Hour))
	require.NoError(t, app.Cache.Set(ctx, keyOther, []byte("{}"), time.Hour))

	app.InvalidateChatbotSettingsCache(orgID)

	// Both org keys gone…
	for _, k := range []string{keyA, keyB} {
		_, err := app.Cache.Get(ctx, k)
		assert.ErrorIs(t, err, store.ErrNotFound, "expected %s to be deleted", k)
	}
	// …other org's key untouched.
	_, err := app.Cache.Get(ctx, keyOther)
	assert.NoError(t, err, "other org's cache must not be invalidated")
}
