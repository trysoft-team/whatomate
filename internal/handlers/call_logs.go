package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/storage"
	"github.com/trysoft/whatomate/internal/utils"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ListCallLogs returns call logs for the organization.
// Users with call_logs:read permission see all logs; others see only their own.
func (a *App) ListCallLogs(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	pg := parsePagination(r)
	status := string(r.RequestCtx.QueryArgs().Peek("status"))
	account := string(r.RequestCtx.QueryArgs().Peek("account"))
	contactIDStr := string(r.RequestCtx.QueryArgs().Peek("contact_id"))
	direction := string(r.RequestCtx.QueryArgs().Peek("direction"))
	ivrFlowID := string(r.RequestCtx.QueryArgs().Peek("ivr_flow_id"))
	phone := string(r.RequestCtx.QueryArgs().Peek("phone"))

	query := a.DB.Where("call_logs.organization_id = ?", orgID).
		Preload("Contact").
		Preload("Agent").
		Preload("IVRFlow").
		Order("call_logs.created_at DESC")

	countQuery := a.DB.Model(&models.CallLog{}).Where("organization_id = ?", orgID)

	// Users without call_logs:read permission only see their own call logs
	if !a.HasPermission(userID, models.ResourceCallLogs, models.ActionRead, orgID) {
		query = query.Where("call_logs.agent_id = ?", userID)
		countQuery = countQuery.Where("agent_id = ?", userID)
	}

	if status != "" {
		query = query.Where("call_logs.status = ?", status)
		countQuery = countQuery.Where("status = ?", status)
	}
	if account != "" {
		query = query.Where("call_logs.whatsapp_account = ?", account)
		countQuery = countQuery.Where("whatsapp_account = ?", account)
	}
	if contactIDStr != "" {
		query = query.Where("call_logs.contact_id = ?", contactIDStr)
		countQuery = countQuery.Where("contact_id = ?", contactIDStr)
	}
	if direction != "" {
		query = query.Where("call_logs.direction = ?", direction)
		countQuery = countQuery.Where("direction = ?", direction)
	}
	if ivrFlowID != "" {
		query = query.Where("call_logs.ivr_flow_id = ?", ivrFlowID)
		countQuery = countQuery.Where("ivr_flow_id = ?", ivrFlowID)
	}
	if phone != "" {
		phoneLike := "%" + phone + "%"
		query = query.Where("call_logs.caller_phone LIKE ?", phoneLike)
		countQuery = countQuery.Where("caller_phone LIKE ?", phoneLike)
	}

	// Date range filter
	if start, ok := parseDateParam(r, "start_date"); ok {
		query = query.Where("call_logs.created_at >= ?", start)
		countQuery = countQuery.Where("created_at >= ?", start)
	}
	if end, ok := parseDateParam(r, "end_date"); ok {
		query = query.Where("call_logs.created_at <= ?", endOfDay(end))
		countQuery = countQuery.Where("created_at <= ?", endOfDay(end))
	}

	var total int64
	countQuery.Count(&total)

	var callLogs []models.CallLog
	if err := pg.Apply(query).Find(&callLogs).Error; err != nil {
		a.Log.Error("Failed to fetch call logs", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to fetch call logs", nil, "")
	}

	// Mask phone numbers if enabled for this organization
	if a.ShouldMaskPhoneNumbers(orgID) {
		for i := range callLogs {
			callLogs[i].CallerPhone = utils.MaskPhoneNumber(callLogs[i].CallerPhone)
			if callLogs[i].Contact != nil {
				callLogs[i].Contact.PhoneNumber = utils.MaskPhoneNumber(callLogs[i].Contact.PhoneNumber)
				callLogs[i].Contact.ProfileName = utils.MaskIfPhoneNumber(callLogs[i].Contact.ProfileName)
			}
		}
	}

	return r.SendEnvelope(listEnvelope("call_logs", callLogs, total, pg))
}

// GetCallLog returns a single call log by ID.
// Users without call_logs:read permission can only access their own call logs.
func (a *App) GetCallLog(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	logID, err := parsePathUUID(r, "id", "call log")
	if err != nil {
		return nil
	}

	query := a.DB.Where("id = ? AND organization_id = ?", logID, orgID)
	if !a.HasPermission(userID, models.ResourceCallLogs, models.ActionRead, orgID) {
		query = query.Where("agent_id = ?", userID)
	}

	var callLog models.CallLog
	if err := query.Preload("Contact").Preload("Agent").Preload("IVRFlow").First(&callLog).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Call log not found", nil, "")
	}

	if a.ShouldMaskPhoneNumbers(orgID) {
		callLog.CallerPhone = utils.MaskPhoneNumber(callLog.CallerPhone)
		if callLog.Contact != nil {
			callLog.Contact.PhoneNumber = utils.MaskPhoneNumber(callLog.Contact.PhoneNumber)
			callLog.Contact.ProfileName = utils.MaskIfPhoneNumber(callLog.Contact.ProfileName)
		}
	}

	// Fetch associated call transfers for this call log
	var transfers []models.CallTransfer
	a.DB.Where("call_log_id = ? AND organization_id = ?", callLog.ID, orgID).
		Preload("Agent").
		Preload("InitiatingAgent").
		Preload("Team").
		Order("created_at ASC").
		Find(&transfers)

	return r.SendEnvelope(map[string]any{
		"call_log":  callLog,
		"transfers": transfers,
	})
}

// GetCallRecording returns the same-origin playback path for a call recording.
// The bytes are served by ServeCallRecordingFile. A storage URL is never returned,
// so the browser has no file it can save from the network panel.
// Users without call_logs:read permission can only access recordings for their own calls.
func (a *App) GetCallRecording(r *fastglue.Request) error {
	callLog, _, err := a.loadCallLogForRecording(r)
	if err != nil {
		return nil
	}

	base := strings.TrimSuffix(a.Config.Server.BasePath, "/")
	kind := recordingKindParam(r)
	fileURL := base + "/api/call-logs/" + callLog.ID.String() + "/recording/file"
	if kind != "" && kind != "main" {
		fileURL += "?kind=" + kind
	}
	return r.SendEnvelope(map[string]any{
		"url":      fileURL,
		"duration": callLog.RecordingDuration,
		"kind":     kind,
	})
}

// playbackMaskHeader carries the per-response key. Saving the body from the
// network panel does not include this header, so the saved bytes are not audio.
const playbackMaskHeader = "X-Playback-Mask"

// ServeCallRecordingFile sends a masked recording for the in-app player.
// Same permission rules as GetCallRecording. The body is not a playable file.
func (a *App) ServeCallRecordingFile(r *fastglue.Request) error {
	_, key, err := a.loadCallLogForRecording(r)
	if err != nil {
		return nil
	}

	data, err := a.readCallRecording(r.RequestCtx, key)
	if errors.Is(err, os.ErrInvalid) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid file path", nil, "")
	}
	if err != nil {
		a.Log.Error("Failed to read recording file", "error", err, "key", key)
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Recording file not found", nil, "")
	}

	mask, err := newPlaybackMask()
	if err != nil {
		a.Log.Error("Failed to mask recording", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read recording", nil, "")
	}

	r.RequestCtx.Response.Header.Set(playbackMaskHeader, base64.StdEncoding.EncodeToString(mask))
	r.RequestCtx.Response.Header.Set("Cache-Control", "no-store")
	r.RequestCtx.Response.Header.Set("Content-Disposition", "inline")
	r.RequestCtx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	r.RequestCtx.SetContentType("application/octet-stream")
	r.RequestCtx.SetBody(maskPlayback(data, mask))
	r.RequestCtx.SetStatusCode(fasthttp.StatusOK)
	return nil
}

func (a *App) readCallRecording(ctx context.Context, key string) ([]byte, error) {
	if a.S3Client != nil {
		return a.S3Client.Download(ctx, key)
	}

	fs := storage.NewLocalFS(a.Config.Storage.LocalPath)
	fullPath, err := fs.Resolve(key)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrInvalid
	}
	return os.ReadFile(fullPath)
}

func newPlaybackMask() ([]byte, error) {
	mask := make([]byte, 16)
	if _, err := rand.Read(mask); err != nil {
		return nil, err
	}
	return mask, nil
}

// maskPlayback XORs data with a repeating mask. Applying it twice restores the original.
func maskPlayback(data, mask []byte) []byte {
	out := make([]byte, len(data))
	if len(mask) == 0 {
		copy(out, data)
		return out
	}
	for i := range data {
		out[i] = data[i] ^ mask[i%len(mask)]
	}
	return out
}

func recordingKindParam(r *fastglue.Request) string {
	kind := strings.ToLower(strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("kind"))))
	switch kind {
	case "user", "assistant", "stereo", "ai_user", "ai_assistant", "ai_stereo":
		return strings.TrimPrefix(kind, "ai_")
	case "main", "":
		return "main"
	default:
		return "main"
	}
}

func (a *App) loadCallLogForRecording(r *fastglue.Request) (*models.CallLog, string, error) {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
		return nil, "", errEnvelopeSent
	}

	logID, err := parsePathUUID(r, "id", "call log")
	if err != nil {
		return nil, "", err
	}

	query := a.DB.Where("id = ? AND organization_id = ?", logID, orgID)
	if !a.HasPermission(userID, models.ResourceCallLogs, models.ActionRead, orgID) {
		query = query.Where("agent_id = ?", userID)
	}

	var callLog models.CallLog
	if err := query.First(&callLog).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Call log not found", nil, "")
		return nil, "", errEnvelopeSent
	}

	kind := recordingKindParam(r)
	key := callLog.RecordingS3Key
	if kind != "main" {
		key = ""
		if callLog.AICallArtifacts != nil {
			if raw, err := json.Marshal(callLog.AICallArtifacts); err == nil {
				var arts map[string]any
				if json.Unmarshal(raw, &arts) == nil {
					if recs, ok := arts["recordings"].(map[string]any); ok {
						if v, ok := recs[kind].(string); ok {
							key = strings.TrimSpace(v)
						}
					}
				}
			}
		}
	}
	if key == "" {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "No recording for this call", nil, "")
		return nil, "", errEnvelopeSent
	}

	return &callLog, key, nil
}
