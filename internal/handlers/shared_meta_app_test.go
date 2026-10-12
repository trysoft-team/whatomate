package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func TestSharedMetaApp_AppliesToOrg(t *testing.T) {
	t.Parallel()
	orgA := uuid.New()
	orgB := uuid.New()

	off := &models.SharedMetaAppCredentials{ShareMode: models.MetaAppShareOff}
	assert.False(t, off.AppliesToOrg(orgA))

	global := &models.SharedMetaAppCredentials{ShareMode: models.MetaAppShareGlobal}
	assert.True(t, global.AppliesToOrg(orgA))

	selected := &models.SharedMetaAppCredentials{
		ShareMode:    models.MetaAppShareSelected,
		SharedOrgIDs: models.JSONBArray{orgA.String()},
	}
	assert.True(t, selected.AppliesToOrg(orgA))
	assert.False(t, selected.AppliesToOrg(orgB))
}

// resetSharedMetaApp empties the singleton shared_meta_app_credentials table.
// Tests that write it must not use t.Parallel: the resolver reads the oldest row.
func resetSharedMetaApp(t *testing.T, app *handlers.App) {
	t.Helper()
	wipe := func() {
		require.NoError(t, app.DB.Exec("DELETE FROM shared_meta_app_credentials").Error)
	}
	wipe()
	t.Cleanup(wipe)
}

func TestResolveMetaAppCreds_Order(t *testing.T) {
	app := newTestApp(t)
	resetSharedMetaApp(t, app)
	app.Config.WhatsApp.AppID = "toml-app"
	app.Config.WhatsApp.AppSecret = "toml-secret"
	app.Config.WhatsApp.ConfigID = "toml-config"

	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)

	enc, err := crypto.Encrypt("shared-secret", app.Config.App.EncryptionKey)
	require.NoError(t, err)

	shared := &models.SharedMetaAppCredentials{
		AppID:              "shared-app",
		ConfigID:           "shared-config",
		AppSecretEncrypted: enc,
		ShareMode:          models.MetaAppShareGlobal,
		SharedOrgIDs:       models.JSONBArray{},
	}
	require.NoError(t, app.DB.Create(shared).Error)

	// Shared beats toml when org has no override.
	id, secret, cfg, err := app.ResolveMetaAppCredsForTest(org.ID)
	require.NoError(t, err)
	assert.Equal(t, "shared-app", id)
	assert.Equal(t, "shared-secret", secret)
	assert.Equal(t, "shared-config", cfg)

	// Org override beats shared.
	orgEnc, err := crypto.Encrypt("org-secret", app.Config.App.EncryptionKey)
	require.NoError(t, err)
	org.Settings = models.JSONB{
		"meta_app_id":               "org-app",
		"meta_config_id":            "org-config",
		"meta_app_secret_encrypted": orgEnc,
	}
	require.NoError(t, app.DB.Save(org).Error)

	id, secret, cfg, err = app.ResolveMetaAppCredsForTest(org.ID)
	require.NoError(t, err)
	assert.Equal(t, "org-app", id)
	assert.Equal(t, "org-secret", secret)
	assert.Equal(t, "org-config", cfg)

	// Selected mode: only listed orgs inherit shared.
	shared.ShareMode = models.MetaAppShareSelected
	shared.SharedOrgIDs = models.JSONBArray{other.ID.String()}
	require.NoError(t, app.DB.Save(shared).Error)

	// org still has override
	id, _, _, err = app.ResolveMetaAppCredsForTest(org.ID)
	require.NoError(t, err)
	assert.Equal(t, "org-app", id)

	// Clear org override — not in selected list → toml
	org.Settings = models.JSONB{}
	require.NoError(t, app.DB.Save(org).Error)
	id, secret, cfg, err = app.ResolveMetaAppCredsForTest(org.ID)
	require.NoError(t, err)
	assert.Equal(t, "toml-app", id)
	assert.Equal(t, "toml-secret", secret)
	assert.Equal(t, "toml-config", cfg)

	// other is selected → shared
	id, secret, cfg, err = app.ResolveMetaAppCredsForTest(other.ID)
	require.NoError(t, err)
	assert.Equal(t, "shared-app", id)
	assert.Equal(t, "shared-secret", secret)
	assert.Equal(t, "shared-config", cfg)
}

func TestGetSharedMetaApp_RequiresSuperAdmin(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("meta-get-user")))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)

	err := app.GetSharedMetaApp(req)
	require.NoError(t, err)
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestUpdateSharedMetaApp_SuperAdmin(t *testing.T) {
	app := newTestApp(t)
	resetSharedMetaApp(t, app)
	org := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("meta-admin")),
		testutil.WithSuperAdmin(),
	)

	req := testutil.NewJSONRequest(t, map[string]any{
		"app_id":         "admin-app",
		"config_id":      "admin-config",
		"app_secret":     "admin-secret",
		"share_mode":     "selected",
		"shared_org_ids": []string{orgB.ID.String()},
	})
	testutil.SetAuthContext(req, org.ID, admin.ID)

	err := app.UpdateSharedMetaApp(req)
	require.NoError(t, err)
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data struct {
			Settings handlers.SharedMetaAppResponse `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "admin-app", resp.Data.Settings.AppID)
	assert.Equal(t, "admin-config", resp.Data.Settings.ConfigID)
	assert.Equal(t, "selected", resp.Data.Settings.ShareMode)
	assert.True(t, resp.Data.Settings.HasAppSecret)
	assert.Equal(t, []string{orgB.ID.String()}, resp.Data.Settings.SharedOrgIDs)

	getReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(getReq, org.ID, admin.ID)
	require.NoError(t, app.GetSharedMetaApp(getReq))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(getReq))
}

func TestGetOrganizationSettings_MetaSource(t *testing.T) {
	app := newTestApp(t)
	resetSharedMetaApp(t, app)
	app.Config.WhatsApp.AppID = "toml-app"
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("meta-source")))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.GetOrganizationSettings(req))

	var resp struct {
		Data struct {
			Settings handlers.OrganizationSettings `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "config", resp.Data.Settings.MetaSource)

	shared := &models.SharedMetaAppCredentials{
		AppID:     "shared-app",
		ShareMode: models.MetaAppShareGlobal,
	}
	require.NoError(t, app.DB.Create(shared).Error)

	req2 := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req2, org.ID, user.ID)
	require.NoError(t, app.GetOrganizationSettings(req2))
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req2), &resp))
	assert.Equal(t, "shared", resp.Data.Settings.MetaSource)

	org.Settings = models.JSONB{"meta_app_id": "org-app"}
	require.NoError(t, app.DB.Save(org).Error)
	req3 := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req3, org.ID, user.ID)
	require.NoError(t, app.GetOrganizationSettings(req3))
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req3), &resp))
	assert.Equal(t, "org", resp.Data.Settings.MetaSource)
}
