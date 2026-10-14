package handlers_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func serverSettingsApp(t *testing.T) (*handlers.App, string, uuid.UUID, uuid.UUID) {
	t.Helper()
	app := newTestApp(t)
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte("[server]\n# the port\nport = 4747\nallowed_origins = \"http://localhost:4747,https://app.example.com\"\n\n[database]\nport = 5432\n"), 0o600))
	app.ConfigPath = p
	app.Config.Server.Port = 4747
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("srv-admin")), testutil.WithSuperAdmin())
	return app, p, org.ID, admin.ID
}

func settingsData(t *testing.T, req *fastglue.Request) handlers.ServerSettingsResponse {
	t.Helper()
	var resp struct {
		Data handlers.ServerSettingsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func TestServerSettings_RequiresSuperAdmin(t *testing.T) {
	app, _, orgID, _ := serverSettingsApp(t)
	user := testutil.CreateTestUser(t, app.DB, orgID, testutil.WithEmail(testutil.UniqueEmail("srv-user")))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, user.ID)
	require.NoError(t, app.GetServerSettings(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))

	put := testutil.NewJSONRequest(t, map[string]any{"port": 9000})
	testutil.SetAuthContext(put, orgID, user.ID)
	require.NoError(t, app.UpdateServerSettings(put))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(put))
}

func TestServerSettings_GetAndUpdate(t *testing.T) {
	app, path, orgID, adminID := serverSettingsApp(t)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, adminID)
	require.NoError(t, app.GetServerSettings(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	got := settingsData(t, req)
	assert.Equal(t, handlers.ServerSettingsResponse{Port: 4747, ConfigPort: 4747, ConfigPath: path, Writable: true}, got)

	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	free := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	put := testutil.NewJSONRequest(t, map[string]any{"port": free})
	testutil.SetAuthContext(put, orgID, adminID)
	require.NoError(t, app.UpdateServerSettings(put))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(put))
	updated := settingsData(t, put)
	assert.Equal(t, 4747, updated.Port, "the running port is unchanged until restart")
	assert.Equal(t, free, updated.ConfigPort)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "# the port\n")
	assert.Contains(t, string(raw), fmt.Sprintf("allowed_origins = \"http://localhost:%d,https://app.example.com\"\n", free),
		"browser origins on the old port move to the new one")
	assert.Contains(t, string(raw), "[database]\nport = 5432\n")
}

func TestServerSettings_RejectsBadPorts(t *testing.T) {
	app, _, orgID, adminID := serverSettingsApp(t)
	busy, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer func() { _ = busy.Close() }()

	for _, port := range []int{0, 70000, busy.Addr().(*net.TCPAddr).Port} {
		put := testutil.NewJSONRequest(t, map[string]any{"port": port})
		testutil.SetAuthContext(put, orgID, adminID)
		require.NoError(t, app.UpdateServerSettings(put))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(put), port)
	}
}

func TestServerSettings_FileItCannotEdit(t *testing.T) {
	app, path, orgID, adminID := serverSettingsApp(t)
	content := "server = { port = 4747 }\n\n[database]\nport = 5432\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	free := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	put := testutil.NewJSONRequest(t, map[string]any{"port": free})
	testutil.SetAuthContext(put, orgID, adminID)
	require.NoError(t, app.UpdateServerSettings(put))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(put))
	assert.Contains(t, string(testutil.GetResponseBody(put)), "set [server] port by hand")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(raw))
}

func TestServerSettings_NoConfigFile(t *testing.T) {
	app, _, orgID, adminID := serverSettingsApp(t)
	app.ConfigPath = ""
	put := testutil.NewJSONRequest(t, map[string]any{"port": 4747})
	testutil.SetAuthContext(put, orgID, adminID)
	require.NoError(t, app.UpdateServerSettings(put))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(put))
}
