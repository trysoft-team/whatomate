package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	flowEndpointModeBuiltin = "builtin"
	flowEndpointModeProxy   = "proxy"

	// flowEndpointNextScreenKey in a data_exchange payload names the screen the
	// built-in endpoint should return; without it the flow completes.
	flowEndpointNextScreenKey = "next_screen"

	flowEndpointStateTTL = 24 * time.Hour
	flowEndpointProxyTTL = 10 * time.Second
	// Meta gives the endpoint roughly 10s before showing the user an error.
	flowEndpointRequestTTL = 12 * time.Second

	// Meta business endpoint error codes.
	statusFlowDecryptFailed   = 421
	statusFlowSignatureFailed = 432
)

// flowEndpointState is the built-in endpoint's per-flow_token session.
type flowEndpointState struct {
	Data    map[string]any `json:"data"`
	History []string       `json:"history"`
	// Media caches stored uploads by media_id so re-sent files are not fetched again.
	Media map[string]map[string]any `json:"media,omitempty"`
}

// flowEndpointMemState backs state when Redis is not configured (tests, dev).
var flowEndpointMemState sync.Map

func flowEndpointStateKey(flowID uuid.UUID, token string) string {
	return "flow_endpoint:" + flowID.String() + ":" + token
}

func (a *App) loadFlowEndpointState(ctx context.Context, key string) *flowEndpointState {
	st := &flowEndpointState{Data: map[string]any{}}
	if a.Redis != nil {
		if raw, err := a.Redis.Get(ctx, key).Bytes(); err == nil {
			_ = json.Unmarshal(raw, st)
		}
	} else if v, ok := flowEndpointMemState.Load(key); ok {
		_ = json.Unmarshal(v.([]byte), st)
	}
	if st.Data == nil {
		st.Data = map[string]any{}
	}
	return st
}

func (a *App) saveFlowEndpointState(ctx context.Context, key string, st *flowEndpointState) {
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	if a.Redis != nil {
		if err := a.Redis.Set(ctx, key, raw, flowEndpointStateTTL).Err(); err != nil {
			a.Log.Warn("flow endpoint: failed to save state", "error", err)
		}
		return
	}
	flowEndpointMemState.Store(key, raw)
}

func (a *App) deleteFlowEndpointState(ctx context.Context, key string) {
	if a.Redis != nil {
		a.Redis.Del(ctx, key)
		return
	}
	flowEndpointMemState.Delete(key)
}

// FlowEndpoint is the public WhatsApp Flow data endpoint (POST /api/flow-endpoint/{id}).
func (a *App) FlowEndpoint(r *fastglue.Request) error {
	id, err := uuid.Parse(fmt.Sprint(r.RequestCtx.UserValue("id")))
	if err != nil {
		r.RequestCtx.SetStatusCode(fasthttp.StatusNotFound)
		return nil
	}

	var flow models.WhatsAppFlow
	if err := a.DB.Where("id = ?", id).First(&flow).Error; err != nil || !flow.EndpointEnabled {
		r.RequestCtx.SetStatusCode(fasthttp.StatusNotFound)
		return nil
	}

	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", flow.OrganizationID, flow.WhatsAppAccount).
		First(&account).Error; err != nil {
		r.RequestCtx.SetStatusCode(fasthttp.StatusNotFound)
		return nil
	}
	a.decryptAccountSecrets(&account)

	body := r.RequestCtx.PostBody()
	if account.AppSecret != "" {
		sig := r.RequestCtx.Request.Header.Peek("X-Hub-Signature-256")
		if !verifyWebhookSignature(body, sig, []byte(account.AppSecret)) {
			a.Log.Warn("flow endpoint: invalid signature", "flow_id", flow.ID)
			r.RequestCtx.SetStatusCode(statusFlowSignatureFailed)
			return nil
		}
	}

	privateKey, err := crypto.Decrypt(account.FlowPrivateKey, a.Config.App.EncryptionKey)
	if err != nil || privateKey == "" {
		a.Log.Error("flow endpoint: no usable private key", "flow_id", flow.ID, "account", account.Name)
		r.RequestCtx.SetStatusCode(statusFlowDecryptFailed)
		return nil
	}

	req, session, err := whatsapp.DecryptFlowRequest(body, privateKey)
	if err != nil {
		a.Log.Warn("flow endpoint: decrypt failed", "flow_id", flow.ID, "error", err)
		r.RequestCtx.SetStatusCode(statusFlowDecryptFailed)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), flowEndpointRequestTTL)
	defer cancel()
	resp, err := a.respondFlowEndpoint(ctx, &flow, req)
	if err != nil {
		a.Log.Error("flow endpoint: request failed", "flow_id", flow.ID, "action", req.Action, "error", err)
		r.RequestCtx.SetStatusCode(fasthttp.StatusInternalServerError)
		return nil
	}

	encrypted, err := whatsapp.EncryptFlowResponse(resp, session)
	if err != nil {
		r.RequestCtx.SetStatusCode(fasthttp.StatusInternalServerError)
		return nil
	}
	r.RequestCtx.SetStatusCode(fasthttp.StatusOK)
	r.RequestCtx.SetContentType("text/plain")
	r.RequestCtx.SetBodyString(encrypted)
	return nil
}

func (a *App) respondFlowEndpoint(ctx context.Context, flow *models.WhatsAppFlow, req *whatsapp.FlowEndpointRequest) (any, error) {
	if req.Action == "ping" {
		return map[string]any{"data": map[string]any{"status": "active"}}, nil
	}
	if errKey, ok := req.Data["error"]; ok && errKey != nil {
		a.Log.Warn("flow endpoint: client error notification", "flow_id", flow.ID,
			"error", errKey, "error_message", req.Data["error_message"])
		return map[string]any{"data": map[string]any{"acknowledged": true}}, nil
	}
	if flow.EndpointMode == flowEndpointModeProxy {
		return a.proxyFlowEndpoint(ctx, flow, req)
	}
	return a.builtinFlowEndpoint(ctx, flow, req)
}

// builtinFlowEndpoint routes screens using the next_screen payload key and
// accumulates answers; completion returns everything as SUCCESS params so the
// nfm_reply webhook carries the full submission.
func (a *App) builtinFlowEndpoint(ctx context.Context, flow *models.WhatsAppFlow, req *whatsapp.FlowEndpointRequest) (any, error) {
	screens := sanitizeScreensForMetaOpts([]any(flow.Screens), true)
	byID := map[string]map[string]any{}
	var order []string
	for _, s := range screens {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		sid, _ := sm["id"].(string)
		if sid == "" {
			continue
		}
		byID[sid] = sm
		order = append(order, sid)
	}
	if len(order) == 0 {
		return nil, errors.New("flow has no screens")
	}

	key := flowEndpointStateKey(flow.ID, req.FlowToken)
	st := a.loadFlowEndpointState(ctx, key)

	var target string
	switch req.Action {
	case "INIT":
		st = &flowEndpointState{Data: map[string]any{}}
		target = order[0]
	case "BACK":
		target = order[0]
		if n := len(st.History); n > 0 {
			target = st.History[n-1]
			st.History = st.History[:n-1]
		}
	case "data_exchange":
		if st.Media == nil {
			st.Media = map[string]map[string]any{}
		}
		a.materializeFlowMedia(ctx, flow.OrganizationID, req.Data, st.Media)
		next := ""
		for k, v := range req.Data {
			if k == flowEndpointNextScreenKey {
				next, _ = v.(string)
				continue
			}
			st.Data[k] = v
		}
		if next == "" || next == "SUCCESS" || byID[next] == nil {
			params := make(map[string]any, len(st.Data)+1)
			for k, v := range st.Data {
				params[k] = v
			}
			params["flow_token"] = req.FlowToken
			a.deleteFlowEndpointState(ctx, key)
			return map[string]any{
				"screen": "SUCCESS",
				"data": map[string]any{
					"extension_message_response": map[string]any{"params": params},
				},
			}, nil
		}
		if req.Screen != "" {
			st.History = append(st.History, req.Screen)
		}
		target = next
	default:
		return nil, fmt.Errorf("unsupported action %q", req.Action)
	}

	a.saveFlowEndpointState(ctx, key, st)
	return map[string]any{
		"screen": target,
		"data":   buildFlowScreenData(byID[target], st.Data),
	}, nil
}

// buildFlowScreenData fills a screen's declared data model from collected
// answers (falling back to __example__), coerced to the declared type.
func buildFlowScreenData(screen map[string]any, collected map[string]any) map[string]any {
	out := map[string]any{}
	model, _ := screen["data"].(map[string]any)
	for k, raw := range model {
		schema, _ := raw.(map[string]any)
		typ, _ := schema["type"].(string)
		v, ok := collected[k]
		if !ok {
			v = schema["__example__"]
		}
		out[k] = coerceFlowDataValue(v, typ)
	}
	return out
}

func coerceFlowDataValue(v any, typ string) any {
	switch typ {
	case "string":
		switch t := v.(type) {
		case nil:
			return ""
		case string:
			return t
		case []any:
			parts := make([]string, 0, len(t))
			for _, p := range t {
				parts = append(parts, fmt.Sprint(p))
			}
			return strings.Join(parts, ", ")
		default:
			return fmt.Sprint(t)
		}
	case "number":
		switch t := v.(type) {
		case float64, int, int64:
			return t
		case string:
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return f
			}
		}
		return 0
	case "boolean":
		if b, ok := v.(bool); ok {
			return b
		}
		return false
	case "array":
		if arr, ok := v.([]any); ok {
			return arr
		}
		return []any{}
	case "object":
		if m, ok := v.(map[string]any); ok {
			return m
		}
		return map[string]any{}
	}
	return v
}

// proxyFlowEndpoint forwards the decrypted request as JSON to the flow's proxy
// URL and returns its JSON response for encryption.
func (a *App) proxyFlowEndpoint(ctx context.Context, flow *models.WhatsAppFlow, req *whatsapp.FlowEndpointRequest) (any, error) {
	if err := validateWebhookURL(flow.EndpointProxyURL); err != nil {
		return nil, fmt.Errorf("proxy url: %w", err)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, flowEndpointProxyTTL)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, flow.EndpointProxyURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Whatomate-Flow-ID", flow.ID.String())
	httpReq.Header.Set("X-Whatomate-Meta-Flow-ID", flow.MetaFlowID)

	client := &http.Client{Transport: &http.Transport{DialContext: SSRFSafeDialer()}}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("proxy request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("proxy read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("proxy returned status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("proxy response is not a JSON object: %w", err)
	}
	return out, nil
}

// flowEndpointURL is the public URL Meta calls for this flow.
func (a *App) flowEndpointURL(r *fastglue.Request, flowID uuid.UUID) string {
	prefix := strings.TrimRight(a.Config.Server.BasePath, "/")
	return a.publicAbsoluteURL(r, fmt.Sprintf("%s/api/flow-endpoint/%s", prefix, flowID))
}

// FlowEncryptionStatus describes an account's Flow endpoint key setup.
type FlowEncryptionStatus struct {
	Configured      bool   `json:"configured"`
	PublicKey       string `json:"public_key,omitempty"`
	MetaStatus      string `json:"meta_status,omitempty"` // VALID, MISMATCH
	MetaKeyMatches  bool   `json:"meta_key_matches"`
	MetaStatusError string `json:"meta_status_error,omitempty"`
}

func (a *App) flowEncryptionStatus(ctx context.Context, account *models.WhatsAppAccount) FlowEncryptionStatus {
	st := FlowEncryptionStatus{Configured: account.FlowPublicKey != "", PublicKey: account.FlowPublicKey}
	if !st.Configured {
		return st
	}
	info, err := whatsapp.New(a.Log).GetBusinessPublicKey(ctx, a.toWhatsAppAccount(account))
	if err != nil {
		st.MetaStatusError = err.Error()
		return st
	}
	st.MetaStatus = info.BusinessPublicKeySignatureStatus
	st.MetaKeyMatches = strings.TrimSpace(info.BusinessPublicKey) == strings.TrimSpace(account.FlowPublicKey)
	return st
}

// GetFlowEncryption returns the Flow endpoint key status (GET /api/accounts/{id}/flow-encryption).
func (a *App) GetFlowEncryption(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionRead)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}
	return r.SendEnvelope(a.flowEncryptionStatus(context.Background(), account))
}

// SetupFlowEncryption creates (or rotates) the account key pair and uploads the
// public key to Meta (POST /api/accounts/{id}/flow-encryption, body {"rotate": bool}).
func (a *App) SetupFlowEncryption(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}

	var body struct {
		Rotate bool `json:"rotate"`
	}
	if len(r.RequestCtx.PostBody()) > 0 {
		_ = json.Unmarshal(r.RequestCtx.PostBody(), &body)
	}

	if account.FlowPrivateKey == "" || account.FlowPublicKey == "" || body.Rotate {
		priv, pub, err := whatsapp.GenerateFlowKeyPair()
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to generate key pair", nil, "")
		}
		encPriv, err := crypto.Encrypt(priv, a.Config.App.EncryptionKey)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to encrypt private key", nil, "")
		}
		if err := a.DB.Model(&models.WhatsAppAccount{}).Where("id = ?", account.ID).Updates(map[string]any{
			"flow_private_key": encPriv,
			"flow_public_key":  pub,
		}).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save key pair", nil, "")
		}
		account.FlowPrivateKey = encPriv
		account.FlowPublicKey = pub
	}

	ctx := context.Background()
	if err := whatsapp.New(a.Log).SetBusinessPublicKey(ctx, a.toWhatsAppAccount(account), account.FlowPublicKey); err != nil {
		a.Log.Error("Failed to upload flow public key", "error", err, "account", account.Name)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Key saved, but uploading it to Meta failed: "+err.Error(), nil, "")
	}
	return r.SendEnvelope(a.flowEncryptionStatus(ctx, account))
}
