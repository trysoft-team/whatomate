package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/crypto"
	wmgoogle "github.com/trysoft/whatomate/internal/google"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"golang.org/x/oauth2"
)

// fakeGoogle serves Google's token and userinfo endpoints.
type fakeGoogle struct {
	server *httptest.Server

	mu           sync.Mutex
	email        string
	refreshToken string
	tokenForm    url.Values
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{email: "owner@example.com", refreshToken: "refresh-1"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			f.tokenForm = r.PostForm
			if r.PostForm.Get("code") != "good-code" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			resp := map[string]any{
				"access_token": "access-1",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"scope":        "openid email https://www.googleapis.com/auth/spreadsheets",
			}
			if f.refreshToken != "" {
				resp["refresh_token"] = f.refreshToken
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"email": f.email})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)

	prevEndpoint := wmgoogle.OAuthEndpoint
	wmgoogle.OAuthEndpoint = oauth2.Endpoint{
		AuthURL:   f.server.URL + "/auth",
		TokenURL:  f.server.URL + "/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	restoreUserInfo := handlers.SetGoogleUserInfoURLForTest(f.server.URL + "/userinfo")
	t.Cleanup(func() {
		wmgoogle.OAuthEndpoint = prevEndpoint
		restoreUserInfo()
	})
	return f
}

func newGoogleOAuthApp(t *testing.T) (*handlers.App, uuid.UUID, uuid.UUID) {
	t.Helper()
	app := newTestApp(t)
	app.Config.Google.OAuthClientID = "client-123"
	app.Config.Google.OAuthClientSecret = "secret-xyz"
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	return app, org.ID, user.ID
}

// startGoogleOAuth runs the start endpoint and returns the state nonce.
func startGoogleOAuth(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, label string) (string, *url.URL) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"label": label})
	req.RequestCtx.Request.Header.Set("Origin", "http://localhost:3000")
	testutil.SetAuthContext(req, orgID, userID)

	require.NoError(t, app.StartGoogleOAuth(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	var resp struct {
		Data struct {
			AuthURL string `json:"auth_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	u, err := url.Parse(resp.Data.AuthURL)
	require.NoError(t, err)
	return u.Query().Get("state"), u
}

func runGoogleCallback(t *testing.T, app *handlers.App, query string) *url.URL {
	t.Helper()
	req := testutil.NewGETRequest(t)
	req.RequestCtx.Request.SetRequestURI("/api/google-credentials/oauth/callback?" + query)
	require.NoError(t, app.GoogleOAuthCallback(req))
	require.Equal(t, fasthttp.StatusFound, testutil.GetResponseStatusCode(req))
	loc, err := url.Parse(string(req.RequestCtx.Response.Header.Peek("Location")))
	require.NoError(t, err)
	return loc
}

func TestGoogleOAuthStatus(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)

	get := func() map[string]any {
		req := testutil.NewGETRequest(t)
		req.RequestCtx.Request.Header.Set("Origin", "http://localhost:3000")
		testutil.SetAuthContext(req, org.ID, user.ID)
		require.NoError(t, app.GetGoogleOAuthStatus(req))
		var resp struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		return resp.Data
	}

	data := get()
	assert.Equal(t, false, data["enabled"])
	assert.Equal(t, "http://localhost:3000/api/google-credentials/oauth/callback", data["redirect_uri"])

	app.Config.Google.OAuthClientID = "client-123"
	app.Config.Google.OAuthClientSecret = "secret-xyz"
	data = get()
	assert.Equal(t, true, data["enabled"])
	assert.Equal(t, "config", data["source"])
}

func TestGoogleOAuthStart_NotConfigured(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.StartGoogleOAuth(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestGoogleOAuthStart_BuildsConsentURL(t *testing.T) {
	app, orgID, userID := newGoogleOAuthApp(t)
	newFakeGoogle(t)

	state, authURL := startGoogleOAuth(t, app, orgID, userID, "Sales sheets")
	require.NotEmpty(t, state)
	q := authURL.Query()
	assert.Equal(t, "client-123", q.Get("client_id"))
	assert.Equal(t, "http://localhost:3000/api/google-credentials/oauth/callback", q.Get("redirect_uri"))
	assert.Equal(t, "offline", q.Get("access_type"))
	assert.Equal(t, "consent", q.Get("prompt"))
	assert.Contains(t, q.Get("scope"), "https://www.googleapis.com/auth/drive.file")
	assert.NotContains(t, strings.Fields(q.Get("scope")), "https://www.googleapis.com/auth/drive")

	exists, err := app.Redis.Exists(context.Background(), "google_oauth:state:"+state).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
}

func TestGoogleOAuthCallback_CreatesCredential(t *testing.T) {
	app, orgID, userID := newGoogleOAuthApp(t)
	fake := newFakeGoogle(t)
	state, _ := startGoogleOAuth(t, app, orgID, userID, "")

	loc := runGoogleCallback(t, app, "code=good-code&state="+url.QueryEscape(state))
	assert.Equal(t, "localhost:3000", loc.Host, "returns to the origin the flow started from")
	assert.Equal(t, "/settings/google-credentials", loc.Path)
	assert.Equal(t, "owner@example.com", loc.Query().Get("google_connected"))
	assert.Equal(t, "http://localhost:3000/api/google-credentials/oauth/callback", fake.tokenForm.Get("redirect_uri"))

	var cred models.GoogleCredential
	require.NoError(t, app.DB.Where("organization_id = ?", orgID).First(&cred).Error)
	assert.Equal(t, models.GoogleAuthOAuth, cred.AuthType)
	assert.Equal(t, "owner@example.com", cred.ClientEmail)
	assert.Equal(t, "owner@example.com", cred.Label, "label defaults to the account email")
	assert.Contains(t, cred.Scopes, "spreadsheets")

	plain, err := crypto.Decrypt(cred.EncryptedJSON, app.Config.App.EncryptionKey)
	require.NoError(t, err)
	var au wmgoogle.AuthorizedUser
	require.NoError(t, json.Unmarshal([]byte(plain), &au))
	assert.Equal(t, wmgoogle.CredTypeAuthorizedUser, au.Type)
	assert.Equal(t, "client-123", au.ClientID)
	assert.Equal(t, "refresh-1", au.RefreshToken)

	// The stored credential is usable by the Google client.
	_, err = wmgoogle.NewClient(context.Background(), []byte(plain))
	require.NoError(t, err)

	// State is single-use.
	loc = runGoogleCallback(t, app, "code=good-code&state="+url.QueryEscape(state))
	assert.NotEmpty(t, loc.Query().Get("google_error"))
}

func TestGoogleOAuthCallback_ReconnectUpdatesExisting(t *testing.T) {
	app, orgID, userID := newGoogleOAuthApp(t)
	fake := newFakeGoogle(t)

	state, _ := startGoogleOAuth(t, app, orgID, userID, "First")
	runGoogleCallback(t, app, "code=good-code&state="+url.QueryEscape(state))

	fake.mu.Lock()
	fake.refreshToken = "refresh-2"
	fake.mu.Unlock()
	state, _ = startGoogleOAuth(t, app, orgID, userID, "Renamed")
	loc := runGoogleCallback(t, app, "code=good-code&state="+url.QueryEscape(state))
	assert.Equal(t, "Renamed", loc.Query().Get("google_connected"))

	var creds []models.GoogleCredential
	require.NoError(t, app.DB.Where("organization_id = ?", orgID).Find(&creds).Error)
	require.Len(t, creds, 1)
	assert.Equal(t, "Renamed", creds[0].Label)
	plain, err := crypto.Decrypt(creds[0].EncryptedJSON, app.Config.App.EncryptionKey)
	require.NoError(t, err)
	assert.Contains(t, plain, "refresh-2")
}

func TestGoogleOAuthCallback_Errors(t *testing.T) {
	app, orgID, userID := newGoogleOAuthApp(t)
	fake := newFakeGoogle(t)

	t.Run("cancelled", func(t *testing.T) {
		state, _ := startGoogleOAuth(t, app, orgID, userID, "")
		loc := runGoogleCallback(t, app, "error=access_denied&state="+url.QueryEscape(state))
		assert.Equal(t, "Google sign-in was cancelled", loc.Query().Get("google_error"))
		assert.Equal(t, "localhost:3000", loc.Host)
	})
	t.Run("unknown state", func(t *testing.T) {
		loc := runGoogleCallback(t, app, "code=good-code&state=nope")
		assert.Contains(t, loc.Query().Get("google_error"), "expired")
		assert.Empty(t, loc.Host, "no trusted origin without state; path-only redirect")
	})
	t.Run("bad code", func(t *testing.T) {
		state, _ := startGoogleOAuth(t, app, orgID, userID, "")
		loc := runGoogleCallback(t, app, "code=bad&state="+url.QueryEscape(state))
		assert.NotEmpty(t, loc.Query().Get("google_error"))
	})
	t.Run("no refresh token", func(t *testing.T) {
		fake.mu.Lock()
		fake.refreshToken = ""
		fake.mu.Unlock()
		state, _ := startGoogleOAuth(t, app, orgID, userID, "")
		loc := runGoogleCallback(t, app, "code=good-code&state="+url.QueryEscape(state))
		assert.Contains(t, loc.Query().Get("google_error"), "offline access")
	})

	var count int64
	require.NoError(t, app.DB.Model(&models.GoogleCredential{}).Where("organization_id = ?", orgID).Count(&count).Error)
	assert.Zero(t, count)
}

func TestListGoogleCredentials_IncludesAuthType(t *testing.T) {
	app, orgID, userID := newGoogleOAuthApp(t)
	require.NoError(t, app.DB.Create(&models.GoogleCredential{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, Label: "SA",
		EncryptedJSON: "x", ClientEmail: "sa@proj.iam.gserviceaccount.com",
	}).Error)
	require.NoError(t, app.DB.Create(&models.GoogleCredential{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, Label: "User",
		EncryptedJSON: "x", ClientEmail: "u@example.com", AuthType: models.GoogleAuthOAuth,
		Scopes: "openid https://www.googleapis.com/auth/spreadsheets",
	}).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.ListGoogleCredentials(req))
	var resp struct {
		Data []struct {
			Label    string   `json:"label"`
			AuthType string   `json:"auth_type"`
			Scopes   []string `json:"scopes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "service_account", resp.Data[0].AuthType)
	assert.Equal(t, "oauth", resp.Data[1].AuthType)
	assert.Len(t, resp.Data[1].Scopes, 2)
}
