package handlers_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func screen(id string, children ...any) map[string]any {
	return map[string]any{
		"id":     id,
		"title":  id,
		"layout": map[string]any{"type": "SingleColumnLayout", "children": children},
	}
}

func footer(action map[string]any) map[string]any {
	return map[string]any{"type": "Footer", "label": "Go", "on-click-action": action}
}

func dataExchange(next string) map[string]any {
	payload := map[string]any{}
	if next != "" {
		payload["next_screen"] = next
	}
	return map[string]any{"name": "data_exchange", "payload": payload}
}

// --- pure helpers ---

func TestBuildRoutingModel_ForwardEdgesOnly(t *testing.T) {
	t.Parallel()

	screens := handlers.SanitizeScreensForMetaOptsForTest([]any{
		screen("START", footer(map[string]any{"name": "navigate", "next": map[string]any{"type": "screen", "name": "MIDDLE"}})),
		screen("MIDDLE", footer(dataExchange("START"))),
		screen("MIDDLE_TWO"),
		screen("DONE", footer(map[string]any{"name": "complete", "payload": map[string]any{}})),
	}, true)
	// MIDDLE -> START would be a reverse edge; MIDDLE_TWO has no routes.
	model := handlers.BuildRoutingModelForTest(screens)
	assert.Equal(t, []any{"MIDDLE"}, model["START"])
	assert.Equal(t, []any{}, model["MIDDLE"])
	assert.Equal(t, []any{}, model["DONE"])
}

func TestSanitizeScreens_EndpointFinishIsTerminal(t *testing.T) {
	t.Parallel()

	screens := handlers.SanitizeScreensForMetaOptsForTest([]any{
		screen("ASK",
			map[string]any{"type": "TextInput", "name": "email", "label": "Email"},
			footer(dataExchange("CONFIRM"))),
		screen("CONFIRM", footer(dataExchange(""))),
	}, true)

	ask := screens[0].(map[string]any)
	confirm := screens[1].(map[string]any)
	assert.Nil(t, ask["terminal"])
	assert.Equal(t, true, confirm["terminal"])

	children := ask["layout"].(map[string]any)["children"].([]any)
	payload := children[1].(map[string]any)["on-click-action"].(map[string]any)["payload"].(map[string]any)
	assert.Equal(t, "CONFIRM", payload["next_screen"])
	assert.Equal(t, "${form.email}", payload["email"])
}

func TestValidateFlowStructure_Endpoint(t *testing.T) {
	t.Parallel()

	err := handlers.ValidateFlowStructureForTest([]any{screen("A", footer(dataExchange("NOPE")))}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown screen")

	err = handlers.ValidateFlowStructureForTest([]any{
		screen("A", footer(dataExchange("B"))),
		screen("B", footer(dataExchange("A"))),
	}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finish")

	require.NoError(t, handlers.ValidateFlowStructureForTest([]any{
		screen("A", footer(dataExchange("B"))),
		screen("B", footer(dataExchange(""))),
	}, true))

	// Without an endpoint the classic rules still require a complete action.
	require.Error(t, handlers.ValidateFlowStructureForTest([]any{screen("A", footer(dataExchange("")))}, false))
}

// --- endpoint handler ---

type endpointFixture struct {
	app       *handlers.App
	flow      *models.WhatsAppFlow
	publicKey string
	appSecret string
}

func newEndpointFixture(t *testing.T, screens []any, appSecret string) *endpointFixture {
	t.Helper()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	priv, pub, err := whatsapp.GenerateFlowKeyPair()
	require.NoError(t, err)
	encPriv, err := crypto.Encrypt(priv, app.Config.App.EncryptionKey)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(account).Updates(map[string]any{
		"flow_private_key": encPriv,
		"flow_public_key":  pub,
		"app_secret":       appSecret,
	}).Error)

	flow := &models.WhatsAppFlow{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		WhatsAppAccount: account.Name,
		Name:            "Endpoint flow",
		Status:          "DRAFT",
		JSONVersion:     "6.1",
		Screens:         models.JSONBArray(screens),
		EndpointEnabled: true,
		EndpointMode:    "builtin",
	}
	require.NoError(t, app.DB.Create(flow).Error)
	return &endpointFixture{app: app, flow: flow, publicKey: pub, appSecret: appSecret}
}

// call encrypts payload as Meta would, invokes the endpoint, and returns the
// status plus the decrypted response (nil unless 200).
func (f *endpointFixture) call(t *testing.T, payload map[string]any, publicKey string) (int, map[string]any) {
	t.Helper()

	env, session, err := whatsapp.EncryptFlowRequest(payload, publicKey)
	require.NoError(t, err)
	req := testutil.NewJSONRequest(t, env)
	testutil.SetPathParam(req, "id", f.flow.ID.String())
	if f.appSecret != "" {
		mac := hmac.New(sha256.New, []byte(f.appSecret))
		mac.Write(req.RequestCtx.PostBody())
		req.RequestCtx.Request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	require.NoError(t, f.app.FlowEndpoint(req))
	status := testutil.GetResponseStatusCode(req)
	if status != fasthttp.StatusOK {
		return status, nil
	}
	resp, err := whatsapp.DecryptFlowResponse(string(testutil.GetResponseBody(req)), session)
	require.NoError(t, err)
	return status, resp
}

func twoStepScreens() []any {
	return []any{
		screen("DETAILS",
			map[string]any{"type": "TextInput", "name": "name", "label": "Name"},
			footer(dataExchange("CONFIRM"))),
		screen("CONFIRM",
			map[string]any{"type": "TextBody", "text": "${data.name}"},
			footer(dataExchange(""))),
	}
}

func TestFlowEndpoint_Ping(t *testing.T) {
	t.Parallel()

	f := newEndpointFixture(t, twoStepScreens(), "")
	status, resp := f.call(t, map[string]any{"version": "3.0", "action": "ping"}, f.publicKey)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "active", resp["data"].(map[string]any)["status"])
}

func TestFlowEndpoint_BuiltinJourney(t *testing.T) {
	t.Parallel()

	f := newEndpointFixture(t, twoStepScreens(), "secret")
	token := "tok-" + uuid.New().String()

	status, resp := f.call(t, map[string]any{"version": "3.0", "action": "INIT", "flow_token": token}, f.publicKey)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "DETAILS", resp["screen"])

	status, resp = f.call(t, map[string]any{
		"version": "3.0", "action": "data_exchange", "screen": "DETAILS", "flow_token": token,
		"data": map[string]any{"name": "Ada", "next_screen": "CONFIRM"},
	}, f.publicKey)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "CONFIRM", resp["screen"])
	assert.Equal(t, "Ada", resp["data"].(map[string]any)["name"])

	status, resp = f.call(t, map[string]any{
		"version": "3.0", "action": "data_exchange", "screen": "CONFIRM", "flow_token": token,
		"data": map[string]any{"name": "Ada"},
	}, f.publicKey)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "SUCCESS", resp["screen"])
	params := resp["data"].(map[string]any)["extension_message_response"].(map[string]any)["params"].(map[string]any)
	assert.Equal(t, token, params["flow_token"])
	assert.Equal(t, "Ada", params["name"])
}

func TestFlowEndpoint_ErrorNotificationAcknowledged(t *testing.T) {
	t.Parallel()

	f := newEndpointFixture(t, twoStepScreens(), "")
	status, resp := f.call(t, map[string]any{
		"version": "3.0", "action": "data_exchange", "flow_token": "t",
		"data": map[string]any{"error": "invalid-screen-transition", "error_message": "bad"},
	}, f.publicKey)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, true, resp["data"].(map[string]any)["acknowledged"])
}

func TestFlowEndpoint_BadSignature(t *testing.T) {
	t.Parallel()

	f := newEndpointFixture(t, twoStepScreens(), "secret")
	f.appSecret = "wrong-secret"
	status, _ := f.call(t, map[string]any{"version": "3.0", "action": "ping"}, f.publicKey)
	assert.Equal(t, 432, status)
}

func TestFlowEndpoint_WrongKeyReturns421(t *testing.T) {
	t.Parallel()

	f := newEndpointFixture(t, twoStepScreens(), "")
	_, otherPub, err := whatsapp.GenerateFlowKeyPair()
	require.NoError(t, err)
	status, _ := f.call(t, map[string]any{"version": "3.0", "action": "ping"}, otherPub)
	assert.Equal(t, 421, status)
}

func TestFlowEndpoint_DisabledIsNotFound(t *testing.T) {
	t.Parallel()

	f := newEndpointFixture(t, twoStepScreens(), "")
	require.NoError(t, f.app.DB.Model(f.flow).Update("endpoint_enabled", false).Error)
	status, _ := f.call(t, map[string]any{"version": "3.0", "action": "ping"}, f.publicKey)
	assert.Equal(t, fasthttp.StatusNotFound, status)
}

func TestUpdateFlow_ProxyModeNeedsURL(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	flow := createTestFlow(t, app, org.ID, account.Name, "Proxy flow")

	req := testutil.NewJSONRequest(t, map[string]any{"endpoint_enabled": true, "endpoint_mode": "proxy"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", flow.ID.String())
	require.NoError(t, app.UpdateFlow(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	req = testutil.NewJSONRequest(t, map[string]any{
		"endpoint_enabled": true, "endpoint_mode": "builtin",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", flow.ID.String())
	req.RequestCtx.Request.Header.SetHost("wa.example.com")
	require.NoError(t, app.UpdateFlow(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data struct {
			Flow handlers.FlowResponse `json:"flow"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.True(t, resp.Data.Flow.EndpointEnabled)
	assert.Equal(t, "https://wa.example.com/api/flow-endpoint/"+flow.ID.String(), resp.Data.Flow.EndpointURL)
}

func TestGetFlow_EndpointURLFollowsBrowserLocation(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	flow := createTestFlow(t, app, org.ID, account.Name, "Location flow")

	endpointURL := func(headers map[string]string) string {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", flow.ID.String())
		req.RequestCtx.Request.Header.SetHost("localhost:8080")
		for k, v := range headers {
			req.RequestCtx.Request.Header.Set(k, v)
		}
		require.NoError(t, app.GetFlow(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		var resp struct {
			Data struct {
				Flow handlers.FlowResponse `json:"flow"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		return resp.Data.Flow.EndpointURL
	}

	suffix := "/api/flow-endpoint/" + flow.ID.String()
	assert.Equal(t, "https://abc.ngrok-free.dev"+suffix,
		endpointURL(map[string]string{"Origin": "https://abc.ngrok-free.dev"}))
	assert.Equal(t, "https://abc.ngrok-free.dev"+suffix,
		endpointURL(map[string]string{"Referer": "https://abc.ngrok-free.dev/flows/x/edit"}))
	assert.Equal(t, "https://wa.example.com"+suffix,
		endpointURL(map[string]string{"X-Forwarded-Host": "wa.example.com", "X-Forwarded-Proto": "https"}))

	app.Config.Server.PublicURL = "https://override.example.com"
	assert.Equal(t, "https://override.example.com"+suffix,
		endpointURL(map[string]string{"Origin": "https://abc.ngrok-free.dev"}))
}
