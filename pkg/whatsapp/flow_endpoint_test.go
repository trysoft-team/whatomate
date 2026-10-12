package whatsapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/pkg/whatsapp"
)

func TestFlowEndpoint_RoundTrip(t *testing.T) {
	t.Parallel()

	priv, pub, err := whatsapp.GenerateFlowKeyPair()
	require.NoError(t, err)

	env, clientSession, err := whatsapp.EncryptFlowRequest(map[string]any{
		"version":    "3.0",
		"action":     "data_exchange",
		"screen":     "DETAILS",
		"data":       map[string]any{"name": "Ada"},
		"flow_token": "tok-1",
	}, pub)
	require.NoError(t, err)

	body, _ := json.Marshal(env)
	req, serverSession, err := whatsapp.DecryptFlowRequest(body, priv)
	require.NoError(t, err)
	assert.Equal(t, "data_exchange", req.Action)
	assert.Equal(t, "DETAILS", req.Screen)
	assert.Equal(t, "tok-1", req.FlowToken)
	assert.Equal(t, "Ada", req.Data["name"])

	encResp, err := whatsapp.EncryptFlowResponse(map[string]any{"screen": "NEXT", "data": map[string]any{}}, serverSession)
	require.NoError(t, err)

	resp, err := whatsapp.DecryptFlowResponse(encResp, clientSession)
	require.NoError(t, err)
	assert.Equal(t, "NEXT", resp["screen"])
}

func TestFlowEndpoint_WrongKeyIsDecryptError(t *testing.T) {
	t.Parallel()

	_, pub, err := whatsapp.GenerateFlowKeyPair()
	require.NoError(t, err)
	otherPriv, _, err := whatsapp.GenerateFlowKeyPair()
	require.NoError(t, err)

	env, _, err := whatsapp.EncryptFlowRequest(map[string]any{"version": "3.0", "action": "ping"}, pub)
	require.NoError(t, err)
	body, _ := json.Marshal(env)

	_, _, err = whatsapp.DecryptFlowRequest(body, otherPriv)
	require.Error(t, err)
	assert.True(t, errors.Is(err, whatsapp.ErrFlowDecrypt))
}

func TestClient_SetBusinessPublicKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/whatsapp_business_encryption")
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "PEM", body["business_public_key"])
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	require.NoError(t, client.SetBusinessPublicKey(context.Background(), testAccount(server.URL), "PEM"))
}

func TestClient_GetBusinessPublicKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{
			"business_public_key":                  "PEM",
			"business_public_key_signature_status": "VALID",
		}}})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	info, err := client.GetBusinessPublicKey(context.Background(), testAccount(server.URL))
	require.NoError(t, err)
	assert.Equal(t, "VALID", info.BusinessPublicKeySignatureStatus)
}

func TestClient_SetFlowEndpointURI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/flow-123")
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "https://example.com/api/flow-endpoint/x", body["endpoint_uri"])
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	require.NoError(t, client.SetFlowEndpointURI(context.Background(), testAccount(server.URL), "flow-123", "https://example.com/api/flow-endpoint/x"))
}
