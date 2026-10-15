package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

const testFlowPrivateKey = "-----BEGIN PRIVATE KEY-----\nTEST\n-----END PRIVATE KEY-----\n"

func setFlowKeys(t *testing.T, app *handlers.App, account *models.WhatsAppAccount) {
	t.Helper()
	enc, err := crypto.Encrypt(testFlowPrivateKey, app.Config.App.EncryptionKey)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(&models.WhatsAppAccount{}).Where("id = ?", account.ID).Updates(map[string]any{
		"flow_private_key": enc,
		"flow_public_key":  "-----BEGIN PUBLIC KEY-----\nTEST\n-----END PUBLIC KEY-----\n",
	}).Error)
}

func TestApp_RevealFlowPrivateKey_ReturnsKeyAndAudits(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	setFlowKeys(t, app, account)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", account.ID.String())

	require.NoError(t, app.RevealFlowPrivateKey(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, "no-store", string(req.RequestCtx.Response.Header.Peek("Cache-Control")))

	var resp struct {
		Data struct {
			PrivateKey string `json:"private_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, testFlowPrivateKey, resp.Data.PrivateKey)

	require.Eventually(t, func() bool {
		var n int64
		app.DB.Model(&models.AuditLog{}).
			Where("organization_id = ? AND resource_id = ? AND action = ?", org.ID, account.ID, models.AuditActionViewed).
			Count(&n)
		return n == 1
	}, 2*time.Second, 20*time.Millisecond, "revealing the private key is recorded in the audit log")
}

func TestApp_RevealFlowPrivateKey_NotConfigured(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", account.ID.String())

	require.NoError(t, app.RevealFlowPrivateKey(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestApp_RevealFlowPrivateKey_RequiresAccountWrite(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	setFlowKeys(t, app, account)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetPathParam(req, "id", account.ID.String())

	require.NoError(t, app.RevealFlowPrivateKey(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	assert.NotContains(t, string(testutil.GetResponseBody(req)), "PRIVATE KEY")
}
