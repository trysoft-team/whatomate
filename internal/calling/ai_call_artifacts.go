package calling

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
)

// SaveAICallTranscript merges transcript lines into call_logs.ai_call_artifacts.
func (m *Manager) SaveAICallTranscript(callID string, lines []map[string]string) error {
	session, callLogID, err := m.lookupAICallTarget(callID)
	if err != nil {
		return err
	}
	_ = session

	artifacts := m.loadAICallArtifacts(callLogID)
	existing, _ := artifacts["transcript"].([]any)
	for _, line := range lines {
		role := strings.TrimSpace(line["role"])
		text := strings.TrimSpace(line["text"])
		if role == "" || text == "" {
			continue
		}
		existing = append(existing, map[string]any{"role": role, "text": text})
	}
	artifacts["transcript"] = existing
	return m.persistAICallArtifacts(callLogID, artifacts)
}

// SaveAICallRecording uploads one AI Call audio track and stores its key.
// kind is user | assistant | stereo.
func (m *Manager) SaveAICallRecording(callID, kind string, body io.Reader, contentType string) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "user", "assistant", "stereo":
	default:
		return "", fmt.Errorf("kind must be user, assistant, or stereo")
	}
	session, callLogID, err := m.lookupAICallTarget(callID)
	if err != nil {
		return "", err
	}
	if m.recordings == nil {
		return "", fmt.Errorf("recording storage not configured")
	}
	if contentType == "" {
		contentType = "audio/wav"
	}
	ext := ".wav"
	if strings.Contains(contentType, "ogg") {
		ext = ".ogg"
	}
	key := path.Join(
		"recordings",
		session.OrganizationID.String(),
		callLogID.String(),
		fmt.Sprintf("ai_%s_%s%s", kind, uuid.New().String()[:8], ext),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.recordings.Upload(ctx, key, body, contentType); err != nil {
		return "", err
	}

	artifacts := m.loadAICallArtifacts(callLogID)
	recs, _ := artifacts["recordings"].(map[string]any)
	if recs == nil {
		recs = map[string]any{}
	}
	recs[kind] = key
	artifacts["recordings"] = recs
	if err := m.persistAICallArtifacts(callLogID, artifacts); err != nil {
		return key, err
	}
	m.log.Info("AI Call recording saved", "call_id", callID, "kind", kind, "key", key)
	return key, nil
}

func (m *Manager) lookupAICallTarget(callID string) (*CallSession, uuid.UUID, error) {
	if m == nil || strings.TrimSpace(callID) == "" {
		return nil, uuid.Nil, fmt.Errorf("call not found")
	}
	m.mu.RLock()
	session := m.sessions[callID]
	m.mu.RUnlock()
	if session == nil {
		// Fall back to DB lookup by WhatsApp call id (session may have ended).
		var log models.CallLog
		if err := m.db.Select("id", "organization_id").
			Where("whatsapp_call_id = ?", callID).
			Order("created_at DESC").
			First(&log).Error; err != nil {
			return nil, uuid.Nil, fmt.Errorf("call not found")
		}
		return &CallSession{
			ID:             callID,
			OrganizationID: log.OrganizationID,
			CallLogID:      log.ID,
		}, log.ID, nil
	}
	if session.CallLogID == uuid.Nil {
		return nil, uuid.Nil, fmt.Errorf("call log missing")
	}
	return session, session.CallLogID, nil
}

func (m *Manager) loadAICallArtifacts(callLogID uuid.UUID) map[string]any {
	var log models.CallLog
	if err := m.db.Select("ai_call_artifacts").Where("id = ?", callLogID).First(&log).Error; err != nil {
		return map[string]any{}
	}
	if log.AICallArtifacts == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(log.AICallArtifacts)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func (m *Manager) persistAICallArtifacts(callLogID uuid.UUID, artifacts map[string]any) error {
	if artifacts == nil {
		artifacts = map[string]any{}
	}
	return m.db.Model(&models.CallLog{}).
		Where("id = ?", callLogID).
		Update("ai_call_artifacts", models.JSONB(artifacts)).Error
}

// AICallRecordingKey returns a stored AI recording object key for kind, if any.
func (m *Manager) AICallRecordingKey(callLogID uuid.UUID, kind string) string {
	artifacts := m.loadAICallArtifacts(callLogID)
	recs, _ := artifacts["recordings"].(map[string]any)
	if recs == nil {
		return ""
	}
	v, _ := recs[kind].(string)
	return strings.TrimSpace(v)
}
