package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

// fakeMetaFlows serves ListFlows per business ID; unknown businesses fail.
func fakeMetaFlows(t *testing.T, flowsByBusiness map[string][]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 3 && parts[2] == "assets" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		if len(parts) == 3 && parts[2] == "flows" {
			if flows, ok := flowsByBusiness[parts[1]]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": flows})
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"unknown business","code":100}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type syncResponse struct {
	Data struct {
		Synced         int      `json:"synced"`
		Created        int      `json:"created"`
		Updated        int      `json:"updated"`
		Accounts       int      `json:"accounts"`
		FailedAccounts []string `json:"failed_accounts"`
	} `json:"data"`
}

func TestApp_SyncFlows_AllAccounts(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	good := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	broken := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	existing := createTestFlow(t, app, org.ID, good.Name, "Old name")
	require.NoError(t, app.DB.Model(existing).Update("meta_flow_id", "meta-existing").Error)

	srv := fakeMetaFlows(t, map[string][]map[string]any{
		good.BusinessID: {
			{"id": "meta-existing", "name": "Renamed on Meta", "status": "PUBLISHED", "categories": []string{"SURVEY"}},
			{"id": "meta-new", "name": "Made on Meta", "status": "DRAFT", "categories": []string{"OTHER"}},
		},
	})
	app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)

	for _, body := range []map[string]any{{}, {"whatsapp_account": "all"}} {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, org.ID, user.ID)
		require.NoError(t, app.SyncFlows(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))

		var resp syncResponse
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, 2, resp.Data.Accounts)
		assert.Equal(t, 2, resp.Data.Synced)
		assert.Equal(t, []string{broken.Name}, resp.Data.FailedAccounts, "one failing account must not stop the others")
	}

	var renamed models.WhatsAppFlow
	require.NoError(t, app.DB.First(&renamed, "id = ?", existing.ID).Error)
	assert.Equal(t, "Renamed on Meta", renamed.Name)
	assert.Equal(t, "PUBLISHED", renamed.Status)

	var imported models.WhatsAppFlow
	require.NoError(t, app.DB.Where("organization_id = ? AND meta_flow_id = ?", org.ID, "meta-new").First(&imported).Error)
	assert.Equal(t, good.Name, imported.WhatsAppAccount, "imported flows belong to the account they came from")
}

func TestApp_SyncFlows_AllAccountsFailing(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), fakeMetaFlows(t, nil).URL)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SyncFlows(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusInternalServerError, "Failed to fetch flows from Meta")
}

func TestApp_SyncFlows_NoAccounts(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SyncFlows(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusBadRequest, "No WhatsApp account configured")
}

func TestApp_SyncFlows_SingleAccount(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	other := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	srv := fakeMetaFlows(t, map[string][]map[string]any{
		account.BusinessID: {{"id": "meta-one", "name": "One", "status": "DRAFT"}},
		other.BusinessID:   {{"id": "meta-other", "name": "Other", "status": "DRAFT"}},
	})
	app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)

	req := testutil.NewJSONRequest(t, map[string]any{"whatsapp_account": account.Name})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SyncFlows(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp syncResponse
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, 1, resp.Data.Synced)
	assert.Equal(t, 1, resp.Data.Created)

	var count int64
	app.DB.Model(&models.WhatsAppFlow{}).Where("organization_id = ? AND meta_flow_id = ?", org.ID, "meta-other").Count(&count)
	assert.Zero(t, count, "a named account only syncs that account")

	req = testutil.NewJSONRequest(t, map[string]any{"whatsapp_account": "missing"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SyncFlows(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusBadRequest, "WhatsApp account not found")
}
