package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/crypto"
	wmgoogle "github.com/trysoft/whatomate/internal/google"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

const (
	googleOAuthStatePrefix   = "google_oauth:state:"
	googleOAuthStateTTL      = 10 * time.Minute
	googleOAuthCallbackPath  = "/api/google-credentials/oauth/callback"
	googleCredentialsUIPath  = "/settings/google-credentials"
	googleOAuthSourceConfig  = "config"
	googleOAuthSourceSSO     = "sso"
	googleOAuthLabelMaxRunes = 255
)

// googleUserInfoURL returns the connected account's email; tests may override it.
var googleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

type googleOAuthState struct {
	OrgID       uuid.UUID `json:"org_id"`
	UserID      uuid.UUID `json:"user_id"`
	Label       string    `json:"label"`
	RedirectURI string    `json:"redirect_uri"`
}

// googleOAuthApp returns the OAuth client for Connect Google account: the
// server-wide [google] config, else the org's enabled Google SSO client.
func (a *App) googleOAuthApp(orgID uuid.UUID) (clientID, clientSecret, source string, ok bool) {
	if a.Config != nil && a.Config.Google.OAuthClientID != "" && a.Config.Google.OAuthClientSecret != "" {
		return a.Config.Google.OAuthClientID, a.Config.Google.OAuthClientSecret, googleOAuthSourceConfig, true
	}
	var sso models.SSOProvider
	if err := a.DB.Where("organization_id = ? AND provider = ? AND is_enabled = ?", orgID, "google", true).
		First(&sso).Error; err != nil {
		return "", "", "", false
	}
	secret, err := crypto.Decrypt(sso.ClientSecret, a.Config.App.EncryptionKey)
	if err != nil || sso.ClientID == "" || secret == "" {
		return "", "", "", false
	}
	return sso.ClientID, secret, googleOAuthSourceSSO, true
}

func (a *App) googleOAuthRedirectURI(r *fastglue.Request) string {
	prefix := strings.TrimRight(sanitizeRedirectPath(a.Config.Server.BasePath), "/")
	return a.publicAbsoluteURL(r, prefix+googleOAuthCallbackPath)
}

func googleOAuthConfig(clientID, clientSecret, redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     wmgoogle.OAuthEndpoint,
		Scopes:       wmgoogle.OAuthScopes,
		RedirectURL:  redirectURI,
	}
}

// GetGoogleOAuthStatus reports whether Connect Google account is available and
// which redirect URI must be registered on the OAuth client.
func (a *App) GetGoogleOAuthStatus(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	_, _, source, ok := a.googleOAuthApp(orgID)
	return r.SendEnvelope(map[string]any{
		"enabled":      ok,
		"source":       source,
		"redirect_uri": a.googleOAuthRedirectURI(r),
	})
}

// StartGoogleOAuth returns the Google consent URL for connecting an account.
func (a *App) StartGoogleOAuth(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	var req struct {
		Label string `json:"label"`
	}
	if body := r.RequestCtx.PostBody(); len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid body", nil, "")
		}
	}
	label := strings.TrimSpace(req.Label)
	if len([]rune(label)) > googleOAuthLabelMaxRunes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Label is too long", nil, "")
	}

	clientID, clientSecret, _, ok := a.googleOAuthApp(orgID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"Connect Google account isn't set up. Add [google] oauth_client_id and oauth_client_secret to the server config, or enable Google SSO.", nil, "")
	}
	redirectURI := a.googleOAuthRedirectURI(r)
	if redirectURI == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Couldn't determine the callback URL; set server.public_url", nil, "")
	}

	nonce := generateRandomString(32)
	stateJSON, _ := json.Marshal(googleOAuthState{OrgID: orgID, UserID: userID, Label: label, RedirectURI: redirectURI})
	redisCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Redis.Set(redisCtx, googleOAuthStatePrefix+nonce, stateJSON, googleOAuthStateTTL).Err(); err != nil {
		a.Log.Error("Failed to store Google OAuth state", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to start Google sign-in", nil, "")
	}

	authURL := googleOAuthConfig(clientID, clientSecret, redirectURI).AuthCodeURL(nonce,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.SetAuthURLParam("include_granted_scopes", "true"),
	)
	return r.SendEnvelope(map[string]any{"auth_url": authURL})
}

// GoogleOAuthCallback finishes Connect Google account and stores the credential.
// Public route: the one-time state stored in Redis identifies the org and user.
func (a *App) GoogleOAuthCallback(r *fastglue.Request) error {
	args := r.RequestCtx.QueryArgs()
	code := string(args.Peek("code"))
	nonce := string(args.Peek("state"))
	errParam := string(args.Peek("error"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Consume the state first (even on error) so the link can't be replayed and
	// the redirect goes back to the origin the flow started from.
	var state googleOAuthState
	haveState := false
	if nonce != "" {
		if stateJSON, err := a.Redis.GetDel(ctx, googleOAuthStatePrefix+nonce).Bytes(); err == nil {
			haveState = json.Unmarshal(stateJSON, &state) == nil
		}
	}
	uiBase := ""
	if haveState {
		uiBase = googleUIBase(state.RedirectURI)
	}
	fail := func(msg string) error {
		a.redirectGoogleCredentials(r, uiBase, "google_error", msg)
		return nil
	}

	if errParam != "" {
		if errParam == "access_denied" {
			return fail("Google sign-in was cancelled")
		}
		return fail("Google sign-in failed: " + errParam)
	}
	if code == "" || nonce == "" {
		return fail("Invalid callback parameters")
	}
	if !haveState {
		return fail("Sign-in link expired. Please try again.")
	}

	clientID, clientSecret, _, ok := a.googleOAuthApp(state.OrgID)
	if !ok {
		return fail("Connect Google account is no longer configured")
	}
	cfg := googleOAuthConfig(clientID, clientSecret, state.RedirectURI)
	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		a.Log.Error("Google OAuth code exchange failed", "error", err)
		return fail("Couldn't complete Google sign-in")
	}
	if token.RefreshToken == "" {
		return fail("Google didn't return offline access. Remove Whatomate from your Google account's third-party access and try again.")
	}

	email, err := fetchGoogleEmail(ctx, cfg, token)
	if err != nil {
		a.Log.Error("Failed to fetch Google account email", "error", err)
		return fail("Couldn't read the Google account's email")
	}
	scopes, _ := token.Extra("scope").(string)

	cred, err := a.saveGoogleOAuthCredential(state, email, scopes, wmgoogle.AuthorizedUser{
		Type:         wmgoogle.CredTypeAuthorizedUser,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RefreshToken: token.RefreshToken,
	})
	if err != nil {
		a.Log.Error("Failed to save Google OAuth credential", "error", err)
		return fail("Failed to save the credential")
	}
	a.redirectGoogleCredentials(r, uiBase, "google_connected", cred.Label)
	return nil
}

// saveGoogleOAuthCredential creates the credential, or refreshes the token of
// an existing one for the same Google account so reconnecting doesn't duplicate.
func (a *App) saveGoogleOAuthCredential(state googleOAuthState, email, scopes string, au wmgoogle.AuthorizedUser) (*models.GoogleCredential, error) {
	plain, err := json.Marshal(au)
	if err != nil {
		return nil, err
	}
	enc, err := crypto.Encrypt(string(plain), a.Config.App.EncryptionKey)
	if err != nil {
		return nil, err
	}

	var existing models.GoogleCredential
	err = a.DB.Where("organization_id = ? AND auth_type = ? AND client_email = ?", state.OrgID, models.GoogleAuthOAuth, email).
		First(&existing).Error
	if err == nil {
		updates := map[string]any{"encrypted_json": enc, "scopes": scopes}
		if state.Label != "" {
			updates["label"] = state.Label
		}
		if err := a.DB.Model(&existing).Updates(updates).Error; err != nil {
			return nil, err
		}
		a.logAudit(state.OrgID, state.UserID, "google_credential", existing.ID, models.AuditActionUpdated,
			nil, map[string]any{"reconnected": email})
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	label := state.Label
	if label == "" {
		label = email
	}
	cred := models.GoogleCredential{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: state.OrgID,
		Label:          label,
		EncryptedJSON:  enc,
		ClientEmail:    email,
		AuthType:       models.GoogleAuthOAuth,
		Scopes:         scopes,
	}
	if err := a.DB.Create(&cred).Error; err != nil {
		return nil, err
	}
	a.logAudit(state.OrgID, state.UserID, "google_credential", cred.ID, models.AuditActionCreated,
		nil, map[string]any{"label": cred.Label, "account": email})
	return &cred, nil
}

func fetchGoogleEmail(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := cfg.Client(ctx, token).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo returned %d", resp.StatusCode)
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.Email == "" {
		return "", fmt.Errorf("userinfo has no email")
	}
	return info.Email, nil
}

// redirectGoogleCredentials sends the browser back to the credentials page.
// uiBase is the origin+base path the flow started from (derived from the
// stored redirect URI); when unknown, a path-only Location is used. The header
// is set directly because fasthttp's Redirect would absolutize a relative path
// against the API host, which differs from the UI origin behind a dev proxy.
func (a *App) redirectGoogleCredentials(r *fastglue.Request, uiBase, key, value string) {
	if uiBase == "" {
		uiBase = strings.TrimRight(sanitizeRedirectPath(a.Config.Server.BasePath), "/")
	}
	r.RequestCtx.Response.Header.Set("Location",
		fmt.Sprintf("%s%s?%s=%s", uiBase, googleCredentialsUIPath, key, url.QueryEscape(value)))
	r.RequestCtx.SetStatusCode(fasthttp.StatusFound)
}

func googleUIBase(redirectURI string) string {
	return strings.TrimSuffix(redirectURI, googleOAuthCallbackPath)
}
