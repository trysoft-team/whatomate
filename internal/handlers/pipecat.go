package handlers

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/trysoft/whatomate/internal/calling"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// PipecatEvent handles POST /api/internal/pipecat/events from the sidecar.
// Auth: Authorization Bearer matching [pipecat].auth_token.
func (a *App) PipecatEvent(r *fastglue.Request) error {
	if !a.pipecatAuthorized(r) {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if a.CallManager == nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Calling not configured", nil, "")
	}
	var req struct {
		CallID string `json:"call_id"`
		Type   string `json:"type"`
		TeamID string `json:"team_id"`
		Reason string `json:"reason"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil || strings.TrimSpace(req.CallID) == "" || strings.TrimSpace(req.Type) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "call_id and type required", nil, "")
	}
	ok := a.CallManager.SignalPipecatEvent(req.CallID, calling.PipecatControlEvent{
		Type:   req.Type,
		TeamID: req.TeamID,
		Reason: req.Reason,
		Error:  req.Error,
	})
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "No active Pipecat session for call", nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}

// PipecatTool handles POST /api/internal/pipecat/tools/{call_id} for HTTP/set_variable tools.
func (a *App) PipecatTool(r *fastglue.Request) error {
	if !a.pipecatAuthorized(r) {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if a.CallManager == nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Calling not configured", nil, "")
	}
	callID := r.RequestCtx.UserValue("call_id").(string)
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil || strings.TrimSpace(req.Name) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name required", nil, "")
	}
	args := string(req.Arguments)
	if args == "" {
		args = "{}"
	}
	result, err := a.CallManager.RunPipecatTool(callID, req.Name, args)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{"result": result})
}

// PipecatStatus returns whether the realtime sidecar is configured.
func (a *App) PipecatStatus(r *fastglue.Request) error {
	enabled := a.CallManager != nil && a.CallManager.PipecatEnabled()
	return r.SendEnvelope(map[string]any{"enabled": enabled})
}

// PipecatTranscript handles POST /api/internal/pipecat/transcript/{call_id}.
func (a *App) PipecatTranscript(r *fastglue.Request) error {
	if !a.pipecatAuthorized(r) {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if a.CallManager == nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Calling not configured", nil, "")
	}
	callID := r.RequestCtx.UserValue("call_id").(string)
	var req struct {
		Lines []map[string]string `json:"lines"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid JSON", nil, "")
	}
	if err := a.CallManager.SaveAICallTranscript(callID, req.Lines); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}

// PipecatRecording handles POST /api/internal/pipecat/recordings/{call_id}?kind=user|assistant|stereo
// Body is raw audio bytes (audio/wav preferred).
func (a *App) PipecatRecording(r *fastglue.Request) error {
	if !a.pipecatAuthorized(r) {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if a.CallManager == nil {
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Calling not configured", nil, "")
	}
	callID := r.RequestCtx.UserValue("call_id").(string)
	kind := string(r.RequestCtx.QueryArgs().Peek("kind"))
	ct := string(r.RequestCtx.Request.Header.ContentType())
	body := r.RequestCtx.PostBody()
	if len(body) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "empty body", nil, "")
	}
	key, err := a.CallManager.SaveAICallRecording(callID, kind, bytes.NewReader(body), ct)
	if err != nil {
		a.Log.Warn("Pipecat recording upload failed", "error", err, "call_id", callID, "kind", kind)
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true, "key": key})
}

func (a *App) pipecatAuthorized(r *fastglue.Request) bool {
	if a.Config == nil || strings.TrimSpace(a.Config.Pipecat.AuthToken) == "" {
		return false
	}
	auth := string(r.RequestCtx.Request.Header.Peek("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	return strings.TrimSpace(auth[len(prefix):]) == a.Config.Pipecat.AuthToken
}
