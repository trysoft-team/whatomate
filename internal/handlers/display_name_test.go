package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

// fakeDisplayNameServer simulates Meta's phone number node (display name
// status + change request) and the /register endpoint.
type fakeDisplayNameServer struct {
	server *httptest.Server

	mu            sync.Mutex
	requestedName string
	registerPin   string
	registerCalls int
	rejectChange  bool
}

func newFakeDisplayNameServer(t *testing.T) *fakeDisplayNameServer {
	t.Helper()
	f := &fakeDisplayNameServer{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/register"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.registerPin = body["pin"]
			f.registerCalls++
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodPost:
			if f.rejectChange {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"name limit reached","code":100}}`))
				return
			}
			f.requestedName = r.URL.Query().Get("new_display_name")
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodGet:
			resp := map[string]string{"verified_name": "Old Name", "name_status": "APPROVED", "id": "x"}
			if f.requestedName != "" {
				resp["new_display_name"] = f.requestedName
				resp["new_name_status"] = "PENDING_REVIEW"
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func setupDisplayNameTest(t *testing.T, isSMB bool) (*handlers.App, *fakeDisplayNameServer, *models.WhatsAppAccount, uuid.UUID) {
	t.Helper()
	app := newTestApp(t)
	meta := newFakeDisplayNameServer(t)
	app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, meta.server.URL)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	acc := mkAccountForProfile(t, app.DB, org.ID)
	if isSMB {
		require.NoError(t, app.DB.Model(acc).Update("is_smb", true).Error)
	}
	return app, meta, acc, user.ID
}

type displayNameEnvelope struct {
	Data struct {
		VerifiedName   string `json:"verified_name"`
		NameStatus     string `json:"name_status"`
		NewDisplayName string `json:"new_display_name"`
		NewNameStatus  string `json:"new_name_status"`
		CanReregister  bool   `json:"can_reregister"`
		HasStoredPin   bool   `json:"has_stored_pin"`
	} `json:"data"`
}

func TestApp_GetDisplayName_Success(t *testing.T) {
	app, _, acc, userID := setupDisplayNameTest(t, false)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, acc.OrganizationID, userID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.GetDisplayName(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp displayNameEnvelope
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "Old Name", resp.Data.VerifiedName)
	assert.Equal(t, "APPROVED", resp.Data.NameStatus)
	assert.True(t, resp.Data.CanReregister)
	assert.False(t, resp.Data.HasStoredPin)
}

func TestApp_GetDisplayName_SMBCannotReregister(t *testing.T) {
	app, _, acc, userID := setupDisplayNameTest(t, true)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, acc.OrganizationID, userID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.GetDisplayName(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp displayNameEnvelope
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.False(t, resp.Data.CanReregister)
}

func TestApp_GetDisplayName_CrossOrgIsolation(t *testing.T) {
	app, _, acc, _ := setupDisplayNameTest(t, false)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	otherUser := createAdminUser(t, app, otherOrg.ID)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, otherOrg.ID, otherUser.ID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.GetDisplayName(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestApp_RequestDisplayNameChange_Success(t *testing.T) {
	app, meta, acc, userID := setupDisplayNameTest(t, false)

	req := testutil.NewJSONRequest(t, map[string]any{"display_name": "  Astro & Co  "})
	testutil.SetAuthContext(req, acc.OrganizationID, userID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.RequestDisplayNameChange(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, "Astro & Co", meta.requestedName, "name is trimmed and survives query escaping")

	var resp displayNameEnvelope
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "Astro & Co", resp.Data.NewDisplayName)
	assert.Equal(t, "PENDING_REVIEW", resp.Data.NewNameStatus)
}

func TestApp_RequestDisplayNameChange_Validation(t *testing.T) {
	app, meta, acc, userID := setupDisplayNameTest(t, false)

	for _, name := range []string{"", "   ", strings.Repeat("a", 101)} {
		req := testutil.NewJSONRequest(t, map[string]any{"display_name": name})
		testutil.SetAuthContext(req, acc.OrganizationID, userID)
		testutil.SetPathParam(req, "id", acc.ID.String())

		require.NoError(t, app.RequestDisplayNameChange(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	}
	assert.Empty(t, meta.requestedName, "Meta must not be called for invalid names")
}

func TestApp_RequestDisplayNameChange_MetaRejects(t *testing.T) {
	app, meta, acc, userID := setupDisplayNameTest(t, false)
	meta.rejectChange = true

	req := testutil.NewJSONRequest(t, map[string]any{"display_name": "New Name"})
	testutil.SetAuthContext(req, acc.OrganizationID, userID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.RequestDisplayNameChange(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), "name limit reached")
}

func TestApp_ApplyDisplayName_WithNewPin(t *testing.T) {
	app, meta, acc, userID := setupDisplayNameTest(t, false)

	req := testutil.NewJSONRequest(t, map[string]any{"pin": "123456"})
	testutil.SetAuthContext(req, acc.OrganizationID, userID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.ApplyDisplayName(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, 1, meta.registerCalls)
	assert.Equal(t, "123456", meta.registerPin)

	var updated models.WhatsAppAccount
	require.NoError(t, app.DB.First(&updated, "id = ?", acc.ID).Error)
	assert.True(t, crypto.IsEncrypted(updated.Pin))
	updated.DecryptSecrets(app.Config.App.EncryptionKey)
	assert.Equal(t, "123456", updated.Pin)
}

func TestApp_ApplyDisplayName_UsesStoredPin(t *testing.T) {
	app, meta, acc, userID := setupDisplayNameTest(t, false)
	encPin, err := crypto.Encrypt("654321", app.Config.App.EncryptionKey)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(acc).Update("pin", encPin).Error)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, acc.OrganizationID, userID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.ApplyDisplayName(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, "654321", meta.registerPin)
}

func TestApp_ApplyDisplayName_RequiresPin(t *testing.T) {
	app, meta, acc, userID := setupDisplayNameTest(t, false)

	for _, pin := range []string{"", "12345", "abcdef"} {
		req := testutil.NewJSONRequest(t, map[string]any{"pin": pin})
		testutil.SetAuthContext(req, acc.OrganizationID, userID)
		testutil.SetPathParam(req, "id", acc.ID.String())

		require.NoError(t, app.ApplyDisplayName(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	}
	assert.Zero(t, meta.registerCalls)
}

func TestApp_ApplyDisplayName_SMBRejected(t *testing.T) {
	app, meta, acc, userID := setupDisplayNameTest(t, true)

	req := testutil.NewJSONRequest(t, map[string]any{"pin": "123456"})
	testutil.SetAuthContext(req, acc.OrganizationID, userID)
	testutil.SetPathParam(req, "id", acc.ID.String())

	require.NoError(t, app.ApplyDisplayName(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	assert.Zero(t, meta.registerCalls)
}
