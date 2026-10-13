package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/middleware"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	passkeyRegSessionPrefix   = "passkey:reg:"
	passkeyLoginSessionPrefix = "passkey:login:"
	passkeySessionTTL         = 5 * time.Minute
	passkeyNameMaxRunes       = 100
	passkeyMaxPerUser         = 20
	passkeyUnknownReason      = "passkey_unknown"
	passkeyRPDisplayName      = "Whatomate"
)

var errPasskeyOrigin = errors.New("passkeys aren't available from this address")

// passkeyUser adapts a user and their passkeys for one relying party to the
// webauthn.User interface. The user handle is the user's UUID bytes.
type passkeyUser struct {
	user  *models.User
	creds []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { id := u.user.ID; return id[:] }
func (u *passkeyUser) WebAuthnName() string                       { return u.user.Email }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }
func (u *passkeyUser) WebAuthnDisplayName() string {
	if u.user.FullName != "" {
		return u.user.FullName
	}
	return u.user.Email
}

// passkeyCeremony is the server-side state of a registration or login,
// kept in the state store between the begin and finish calls.
type passkeyCeremony struct {
	Session webauthn.SessionData `json:"session"`
	Origin  string               `json:"origin"`
	RPID    string               `json:"rp_id"`
}

type passkeyResponse struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// CredentialID (base64url) lets the browser's password manager be told
	// which passkeys are still valid (WebAuthn Signal API).
	CredentialID string     `json:"credential_id"`
	RPID         string     `json:"rp_id"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	CurrentSite  bool       `json:"current_site"`
}

// passkeyRelyingParty returns the browser origin of the request and the
// relying party ID (its hostname) passkeys are bound to. The origin must be
// localhost, server.public_url, a configured allowed origin, or the host the
// request was sent to; anything but localhost must be HTTPS.
func (a *App) passkeyRelyingParty(r *fastglue.Request) (origin, rpID string, err error) {
	origin = browserOrigin(r)
	if origin == "" {
		return "", "", errPasskeyOrigin
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return "", "", errPasskeyOrigin
	}
	host := u.Hostname()
	isLocal := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if !isLocal && u.Scheme != "https" {
		return "", "", errPasskeyOrigin
	}
	if isLocal || a.passkeyOriginConfigured(origin) || passkeyOriginIsRequestHost(r, u.Host) {
		return origin, host, nil
	}
	return "", "", errPasskeyOrigin
}

func (a *App) passkeyOriginConfigured(origin string) bool {
	if a.Config == nil {
		return false
	}
	if pub, err := url.Parse(strings.TrimRight(a.Config.Server.PublicURL, "/")); err == nil && pub.Host != "" {
		if strings.EqualFold(pub.Scheme+"://"+pub.Host, origin) {
			return true
		}
	}
	for o := range middleware.ParseAllowedOrigins(a.Config.Server.AllowedOrigins) {
		if strings.EqualFold(strings.TrimRight(o, "/"), origin) {
			return true
		}
	}
	return false
}

func passkeyOriginIsRequestHost(r *fastglue.Request, originHost string) bool {
	hosts := []string{string(r.RequestCtx.Host())}
	if xh := string(r.RequestCtx.Request.Header.Peek("X-Forwarded-Host")); xh != "" {
		hosts = append(hosts, strings.TrimSpace(strings.Split(xh, ",")[0]))
	}
	for _, h := range hosts {
		if h != "" && strings.EqualFold(h, originHost) {
			return true
		}
	}
	return false
}

func newPasskeyWebAuthn(origin, rpID string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: passkeyRPDisplayName,
		RPOrigins:     []string{origin},
	})
}

func encodeCredentialID(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}

// loadPasskeyUser returns the user with the passkeys registered for rpID.
func (a *App) loadPasskeyUser(userID uuid.UUID, rpID string, preloadRole bool) (*passkeyUser, error) {
	var user models.User
	q := a.DB
	if preloadRole {
		q = q.Preload("Role")
	}
	if err := q.Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}
	var rows []models.UserPasskey
	if err := a.DB.Where("user_id = ? AND rp_id = ?", userID, rpID).Find(&rows).Error; err != nil {
		return nil, err
	}
	pu := &passkeyUser{user: &user}
	for _, row := range rows {
		var cred webauthn.Credential
		if err := json.Unmarshal([]byte(row.Credential), &cred); err != nil {
			a.Log.Warn("Skipping unreadable passkey", "passkey_id", row.ID, "error", err)
			continue
		}
		pu.creds = append(pu.creds, cred)
	}
	return pu, nil
}

func (a *App) storePasskeyCeremony(key string, c passkeyCeremony) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.State.Set(ctx, key, data, passkeySessionTTL)
}

// takePasskeyCeremony loads and deletes a ceremony so it can't be replayed.
func (a *App) takePasskeyCeremony(key string) (*passkeyCeremony, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := a.State.GetDel(ctx, key)
	if err != nil {
		return nil, err
	}
	var c passkeyCeremony
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListPasskeys returns the current user's passkeys.
func (a *App) ListPasskeys(r *fastglue.Request) error {
	userID, ok := r.RequestCtx.UserValue("user_id").(uuid.UUID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	var rows []models.UserPasskey
	if err := a.DB.Where("user_id = ?", userID).Order("created_at ASC").Find(&rows).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list passkeys", nil, "")
	}
	_, rpID, _ := a.passkeyRelyingParty(r)
	out := make([]passkeyResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, passkeyResponse{
			ID: row.ID, Name: row.Name, CredentialID: row.CredentialID, RPID: row.RPID, CreatedAt: row.CreatedAt,
			LastUsedAt: row.LastUsedAt, CurrentSite: row.RPID == rpID,
		})
	}
	return r.SendEnvelope(out)
}

// BeginPasskeyRegistration returns WebAuthn creation options for the current user.
func (a *App) BeginPasskeyRegistration(r *fastglue.Request) error {
	userID, ok := r.RequestCtx.UserValue("user_id").(uuid.UUID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	origin, rpID, err := a.passkeyRelyingParty(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	var count int64
	a.DB.Model(&models.UserPasskey{}).Where("user_id = ?", userID).Count(&count)
	if count >= passkeyMaxPerUser {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "You've reached the maximum number of passkeys", nil, "")
	}
	pu, err := a.loadPasskeyUser(userID, rpID, false)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "User not found", nil, "")
	}
	wa, err := newPasskeyWebAuthn(origin, rpID)
	if err != nil {
		a.Log.Error("Failed to configure WebAuthn", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errPasskeyOrigin.Error(), nil, "")
	}
	requireResident := true
	creation, session, err := wa.BeginRegistration(pu,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: &requireResident,
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(webauthn.Credentials(pu.creds).CredentialDescriptors()),
	)
	if err != nil {
		a.Log.Error("Failed to begin passkey registration", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to start passkey registration", nil, "")
	}
	if err := a.storePasskeyCeremony(passkeyRegSessionPrefix+userID.String(),
		passkeyCeremony{Session: *session, Origin: origin, RPID: rpID}); err != nil {
		a.Log.Error("Failed to store passkey session", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to start passkey registration", nil, "")
	}
	return r.SendEnvelope(creation)
}

// FinishPasskeyRegistration verifies the authenticator's response and saves the passkey.
func (a *App) FinishPasskeyRegistration(r *fastglue.Request) error {
	userID, ok := r.RequestCtx.UserValue("user_id").(uuid.UUID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	var req struct {
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil || len(req.Credential) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid body", nil, "")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Passkey"
	}
	if len([]rune(name)) > passkeyNameMaxRunes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Name is too long", nil, "")
	}

	ceremony, err := a.takePasskeyCeremony(passkeyRegSessionPrefix + userID.String())
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Passkey setup expired. Please try again.", nil, "")
	}
	if origin, rpID, err := a.passkeyRelyingParty(r); err != nil || origin != ceremony.Origin || rpID != ceremony.RPID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errPasskeyOrigin.Error(), nil, "")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid passkey response", nil, "")
	}
	pu, err := a.loadPasskeyUser(userID, ceremony.RPID, false)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "User not found", nil, "")
	}
	wa, err := newPasskeyWebAuthn(ceremony.Origin, ceremony.RPID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errPasskeyOrigin.Error(), nil, "")
	}
	cred, err := wa.CreateCredential(pu, ceremony.Session, parsed)
	if err != nil {
		a.Log.Warn("Passkey registration failed verification", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Passkey couldn't be verified", nil, "")
	}
	credJSON, err := json.Marshal(cred)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save passkey", nil, "")
	}
	row := models.UserPasskey{
		ID:           uuid.New(),
		UserID:       userID,
		Name:         name,
		CredentialID: encodeCredentialID(cred.ID),
		RPID:         ceremony.RPID,
		Credential:   string(credJSON),
	}
	if err := a.DB.Create(&row).Error; err != nil {
		a.Log.Error("Failed to save passkey", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This passkey is already registered", nil, "")
	}
	if orgID, err := a.getOrgID(r); err == nil {
		a.logAudit(orgID, userID, "user", userID, models.AuditActionUpdated,
			nil, map[string]any{"passkey_added": name})
	}
	return r.SendEnvelope(passkeyResponse{
		ID: row.ID, Name: row.Name, CredentialID: row.CredentialID, RPID: row.RPID, CreatedAt: row.CreatedAt, CurrentSite: true,
	})
}

// DeletePasskey removes one of the current user's passkeys.
func (a *App) DeletePasskey(r *fastglue.Request) error {
	userID, ok := r.RequestCtx.UserValue("user_id").(uuid.UUID)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := parsePathUUID(r, "id", "passkey")
	if err != nil {
		return nil
	}
	var row models.UserPasskey
	if err := a.DB.Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Passkey not found", nil, "")
	}
	if err := a.DB.Delete(&row).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete passkey", nil, "")
	}
	if orgID, err := a.getOrgID(r); err == nil {
		a.logAudit(orgID, userID, "user", userID, models.AuditActionUpdated,
			map[string]any{"passkey_removed": row.Name}, nil)
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}

// BeginPasskeyLogin returns WebAuthn request options for a username-less
// (discoverable credential) sign-in. Public route.
func (a *App) BeginPasskeyLogin(r *fastglue.Request) error {
	origin, rpID, err := a.passkeyRelyingParty(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	wa, err := newPasskeyWebAuthn(origin, rpID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errPasskeyOrigin.Error(), nil, "")
	}
	assertion, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		a.Log.Error("Failed to begin passkey login", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to start passkey sign-in", nil, "")
	}
	sessionID := generateRandomString(32)
	if err := a.storePasskeyCeremony(passkeyLoginSessionPrefix+sessionID,
		passkeyCeremony{Session: *session, Origin: origin, RPID: rpID}); err != nil {
		a.Log.Error("Failed to store passkey session", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to start passkey sign-in", nil, "")
	}
	return r.SendEnvelope(map[string]any{"session_id": sessionID, "options": assertion})
}

// FinishPasskeyLogin verifies the assertion and signs the user in. Public route.
func (a *App) FinishPasskeyLogin(r *fastglue.Request) error {
	var req struct {
		SessionID  string          `json:"session_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil || req.SessionID == "" || len(req.Credential) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid body", nil, "")
	}
	ceremony, err := a.takePasskeyCeremony(passkeyLoginSessionPrefix + req.SessionID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Passkey sign-in expired. Please try again.", nil, "")
	}
	if origin, rpID, err := a.passkeyRelyingParty(r); err != nil || origin != ceremony.Origin || rpID != ceremony.RPID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errPasskeyOrigin.Error(), nil, "")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid passkey response", nil, "")
	}
	wa, err := newPasskeyWebAuthn(ceremony.Origin, ceremony.RPID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errPasskeyOrigin.Error(), nil, "")
	}

	// A passkey the server has no record of (deleted, or its account removed)
	// gets a distinct reason so the browser can be told to stop offering it.
	var known int64
	if err := a.DB.Model(&models.UserPasskey{}).
		Where("credential_id = ? AND rp_id = ?", encodeCredentialID(parsed.RawID), ceremony.RPID).
		Count(&known).Error; err == nil && known == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "This passkey was removed from your account. Sign in with your password or another passkey.",
			map[string]string{"reason": passkeyUnknownReason}, "")
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		uid, err := uuid.FromBytes(userHandle)
		if err != nil {
			return nil, err
		}
		return a.loadPasskeyUser(uid, ceremony.RPID, true)
	}
	user, cred, err := wa.ValidatePasskeyLogin(handler, ceremony.Session, parsed)
	if err != nil {
		a.Log.Warn("Passkey sign-in failed", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Passkey not recognized", nil, "")
	}
	pu, ok := user.(*passkeyUser)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Passkey not recognized", nil, "")
	}
	if cred.Authenticator.CloneWarning {
		a.Log.Warn("Passkey sign counter went backwards; possible cloned authenticator", "user_id", pu.user.ID)
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Passkey not recognized", nil, "")
	}
	if !pu.user.IsActive {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Account is disabled", nil, "")
	}

	if credJSON, err := json.Marshal(cred); err == nil {
		now := time.Now()
		if err := a.DB.Model(&models.UserPasskey{}).
			Where("user_id = ? AND credential_id = ?", pu.user.ID, encodeCredentialID(cred.ID)).
			Updates(map[string]any{"credential": string(credJSON), "last_used_at": now}).Error; err != nil {
			a.Log.Warn("Failed to update passkey after sign-in", "error", err)
		}
	}
	return a.startLoginSession(r, pu.user)
}
