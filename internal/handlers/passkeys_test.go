package handlers_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const testPasskeyOrigin = "http://localhost:3000"

var b64url = base64.RawURLEncoding

// softAuthenticator is a minimal ES256 platform authenticator for tests.
type softAuthenticator struct {
	key       *ecdsa.PrivateKey
	credID    []byte
	userID    []byte
	signCount uint32
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &softAuthenticator{key: key, credID: id}
}

func rpIDHash(rpID string) []byte {
	h := sha256.Sum256([]byte(rpID))
	return h[:]
}

func clientData(t *testing.T, typ, challenge, origin string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	require.NoError(t, err)
	return b
}

// register builds a "none" attestation response for creation options.
func (s *softAuthenticator) register(t *testing.T, options map[string]any, origin string) json.RawMessage {
	t.Helper()
	pk := options["publicKey"].(map[string]any)
	challenge := pk["challenge"].(string)
	rpID := pk["rp"].(map[string]any)["id"].(string)
	userID, err := b64url.DecodeString(pk["user"].(map[string]any)["id"].(string))
	require.NoError(t, err)
	s.userID = userID

	x := s.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := s.key.PublicKey.Y.FillBytes(make([]byte, 32))
	coseKey, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	require.NoError(t, err)

	authData := append([]byte{}, rpIDHash(rpID)...)
	authData = append(authData, 0x01|0x04|0x40) // UP | UV | AT
	authData = binary.BigEndian.AppendUint32(authData, s.signCount)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(s.credID)))
	authData = append(authData, s.credID...)
	authData = append(authData, coseKey...)

	attObj, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	require.NoError(t, err)

	resp, err := json.Marshal(map[string]any{
		"id": b64url.EncodeToString(s.credID), "rawId": b64url.EncodeToString(s.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64url.EncodeToString(clientData(t, "webauthn.create", challenge, origin)),
			"attestationObject": b64url.EncodeToString(attObj),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	})
	require.NoError(t, err)
	return resp
}

// assert builds an assertion response for request options.
func (s *softAuthenticator) assert(t *testing.T, options map[string]any, origin, rpID string) json.RawMessage {
	t.Helper()
	pk := options["publicKey"].(map[string]any)
	challenge := pk["challenge"].(string)
	s.signCount++

	authData := append([]byte{}, rpIDHash(rpID)...)
	authData = append(authData, 0x01|0x04) // UP | UV
	authData = binary.BigEndian.AppendUint32(authData, s.signCount)

	cd := clientData(t, "webauthn.get", challenge, origin)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, digest[:])
	require.NoError(t, err)

	resp, err := json.Marshal(map[string]any{
		"id": b64url.EncodeToString(s.credID), "rawId": b64url.EncodeToString(s.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64url.EncodeToString(cd),
			"authenticatorData": b64url.EncodeToString(authData),
			"signature":         b64url.EncodeToString(sig),
			"userHandle":        b64url.EncodeToString(s.userID),
		},
		"clientExtensionResults": map[string]any{},
	})
	require.NoError(t, err)
	return resp
}

func passkeyReq(t *testing.T, body any, orgID, userID uuid.UUID) *fastglue.Request {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	req.RequestCtx.Request.Header.Set("Origin", testPasskeyOrigin)
	if userID != uuid.Nil {
		testutil.SetAuthContext(req, orgID, userID)
	}
	return req
}

func envelopeData(t *testing.T, req *fastglue.Request) map[string]any {
	t.Helper()
	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp), string(testutil.GetResponseBody(req)))
	return resp.Data
}

func registerPasskey(t *testing.T, app *handlers.App, auth *softAuthenticator, orgID, userID uuid.UUID, name string) {
	t.Helper()
	begin := passkeyReq(t, map[string]any{}, orgID, userID)
	require.NoError(t, app.BeginPasskeyRegistration(begin))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(begin), string(testutil.GetResponseBody(begin)))
	options := envelopeData(t, begin)

	pk := options["publicKey"].(map[string]any)
	assert.Equal(t, "localhost", pk["rp"].(map[string]any)["id"])
	sel := pk["authenticatorSelection"].(map[string]any)
	assert.Equal(t, "required", sel["residentKey"])
	assert.Equal(t, "required", sel["userVerification"])

	finish := passkeyReq(t, map[string]any{"name": name, "credential": auth.register(t, options, testPasskeyOrigin)}, orgID, userID)
	require.NoError(t, app.FinishPasskeyRegistration(finish))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(finish), string(testutil.GetResponseBody(finish)))
}

func beginPasskeyLogin(t *testing.T, app *handlers.App) (string, map[string]any) {
	t.Helper()
	req := passkeyReq(t, map[string]any{}, uuid.Nil, uuid.Nil)
	require.NoError(t, app.BeginPasskeyLogin(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	data := envelopeData(t, req)
	return data["session_id"].(string), data["options"].(map[string]any)
}

func TestPasskey_RegisterAndSignIn(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	auth := newSoftAuthenticator(t)

	registerPasskey(t, app, auth, org.ID, user.ID, "MacBook Touch ID")

	list := passkeyReq(t, nil, org.ID, user.ID)
	require.NoError(t, app.ListPasskeys(list))
	var listed struct {
		Data []struct {
			Name         string `json:"name"`
			CredentialID string `json:"credential_id"`
			RPID         string `json:"rp_id"`
			CurrentSite  bool   `json:"current_site"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(list), &listed))
	require.Len(t, listed.Data, 1)
	assert.Equal(t, "MacBook Touch ID", listed.Data[0].Name)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(auth.credID), listed.Data[0].CredentialID)
	assert.Equal(t, "localhost", listed.Data[0].RPID)
	assert.True(t, listed.Data[0].CurrentSite)

	sessionID, options := beginPasskeyLogin(t, app)
	finish := passkeyReq(t, map[string]any{"session_id": sessionID, "credential": auth.assert(t, options, testPasskeyOrigin, "localhost")}, uuid.Nil, uuid.Nil)
	require.NoError(t, app.FinishPasskeyLogin(finish))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(finish), string(testutil.GetResponseBody(finish)))

	data := envelopeData(t, finish)
	assert.Equal(t, user.Email, data["user"].(map[string]any)["email"])
	cookies := string(finish.RequestCtx.Response.Header.Peek("Set-Cookie"))
	assert.NotEmpty(t, cookies, "sign-in sets auth cookies")

	var row models.UserPasskey
	require.NoError(t, app.DB.Where("user_id = ?", user.ID).First(&row).Error)
	assert.NotNil(t, row.LastUsedAt)

	// The login session is single-use.
	replay := passkeyReq(t, map[string]any{"session_id": sessionID, "credential": auth.assert(t, options, testPasskeyOrigin, "localhost")}, uuid.Nil, uuid.Nil)
	require.NoError(t, app.FinishPasskeyLogin(replay))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(replay))
}

func TestPasskey_SignInRejectsWrongKeyAndDisabledUser(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	auth := newSoftAuthenticator(t)
	registerPasskey(t, app, auth, org.ID, user.ID, "Key")

	t.Run("signature from a different key", func(t *testing.T) {
		imposter := newSoftAuthenticator(t)
		imposter.credID, imposter.userID = auth.credID, auth.userID
		sessionID, options := beginPasskeyLogin(t, app)
		req := passkeyReq(t, map[string]any{"session_id": sessionID, "credential": imposter.assert(t, options, testPasskeyOrigin, "localhost")}, uuid.Nil, uuid.Nil)
		require.NoError(t, app.FinishPasskeyLogin(req))
		assert.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(req))
	})

	t.Run("assertion for another relying party", func(t *testing.T) {
		sessionID, options := beginPasskeyLogin(t, app)
		req := passkeyReq(t, map[string]any{"session_id": sessionID, "credential": auth.assert(t, options, testPasskeyOrigin, "evil.example")}, uuid.Nil, uuid.Nil)
		require.NoError(t, app.FinishPasskeyLogin(req))
		assert.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(req))
	})

	t.Run("signature from a different key is not reported as removed", func(t *testing.T) {
		imposter := newSoftAuthenticator(t)
		imposter.credID, imposter.userID = auth.credID, auth.userID
		sessionID, options := beginPasskeyLogin(t, app)
		req := passkeyReq(t, map[string]any{"session_id": sessionID, "credential": imposter.assert(t, options, testPasskeyOrigin, "localhost")}, uuid.Nil, uuid.Nil)
		require.NoError(t, app.FinishPasskeyLogin(req))
		assert.NotContains(t, string(testutil.GetResponseBody(req)), "passkey_unknown")
	})

	t.Run("deleted passkey is reported as unknown", func(t *testing.T) {
		stale := newSoftAuthenticator(t)
		stale.userID = auth.userID
		sessionID, options := beginPasskeyLogin(t, app)
		req := passkeyReq(t, map[string]any{"session_id": sessionID, "credential": stale.assert(t, options, testPasskeyOrigin, "localhost")}, uuid.Nil, uuid.Nil)
		require.NoError(t, app.FinishPasskeyLogin(req))
		assert.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(req))
		assert.Contains(t, string(testutil.GetResponseBody(req)), `"reason":"passkey_unknown"`)
	})

	t.Run("disabled account", func(t *testing.T) {
		require.NoError(t, app.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("is_active", false).Error)
		sessionID, options := beginPasskeyLogin(t, app)
		req := passkeyReq(t, map[string]any{"session_id": sessionID, "credential": auth.assert(t, options, testPasskeyOrigin, "localhost")}, uuid.Nil, uuid.Nil)
		require.NoError(t, app.FinishPasskeyLogin(req))
		assert.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(req))
		assert.Contains(t, string(testutil.GetResponseBody(req)), "disabled")
	})
}

func TestPasskey_OriginPolicy(t *testing.T) {
	app := newTestApp(t)

	begin := func(origin string) int {
		req := testutil.NewJSONRequest(t, map[string]any{})
		if origin != "" {
			req.RequestCtx.Request.Header.Set("Origin", origin)
		}
		require.NoError(t, app.BeginPasskeyLogin(req))
		return testutil.GetResponseStatusCode(req)
	}

	assert.Equal(t, fasthttp.StatusBadRequest, begin(""), "no origin")
	assert.Equal(t, fasthttp.StatusBadRequest, begin("https://evil.example"), "unknown origin")
	assert.Equal(t, fasthttp.StatusBadRequest, begin("http://app.example.com"), "non-HTTPS remote origin")
	assert.Equal(t, fasthttp.StatusOK, begin("http://localhost:3000"))

	app.Config.Server.PublicURL = "https://app.example.com/"
	assert.Equal(t, fasthttp.StatusOK, begin("https://app.example.com"), "server.public_url")
	app.Config.Server.AllowedOrigins = "https://other.example.com"
	assert.Equal(t, fasthttp.StatusOK, begin("https://other.example.com"), "allowed_origins")
}

func TestPasskey_DeleteOnlyOwn(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	owner := createAdminUser(t, app, org.ID)
	other := createAdminUser(t, app, org.ID)
	registerPasskey(t, app, newSoftAuthenticator(t), org.ID, owner.ID, "Owner key")

	var row models.UserPasskey
	require.NoError(t, app.DB.Where("user_id = ?", owner.ID).First(&row).Error)

	del := func(userID uuid.UUID) int {
		req := testutil.NewRequest(t)
		req.RequestCtx.Request.Header.SetMethod("DELETE")
		testutil.SetAuthContext(req, org.ID, userID)
		testutil.SetPathParam(req, "id", row.ID.String())
		require.NoError(t, app.DeletePasskey(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusNotFound, del(other.ID))
	assert.Equal(t, fasthttp.StatusOK, del(owner.ID))

	var count int64
	require.NoError(t, app.DB.Model(&models.UserPasskey{}).Where("user_id = ?", owner.ID).Count(&count).Error)
	assert.Zero(t, count)
}

func TestPasskey_RegistrationExcludesExisting(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	auth := newSoftAuthenticator(t)
	registerPasskey(t, app, auth, org.ID, user.ID, "First")

	begin := passkeyReq(t, map[string]any{}, org.ID, user.ID)
	require.NoError(t, app.BeginPasskeyRegistration(begin))
	pk := envelopeData(t, begin)["publicKey"].(map[string]any)
	exclude := pk["excludeCredentials"].([]any)
	require.Len(t, exclude, 1)
	assert.Equal(t, b64url.EncodeToString(auth.credID), exclude[0].(map[string]any)["id"])
}
