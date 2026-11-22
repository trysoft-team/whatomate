package calling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/calling/defaultaudio"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
)

// recordCallerAudio captures Opus from the caller track into an OGG file until
// maxDuration elapses or a terminator DTMF digit arrives.
// terminated is true only when the caller pressed the terminator digit.
func (m *Manager) recordCallerAudio(session *CallSession, player *AudioPlayer, maxDuration time.Duration, terminator string, beep bool) (path string, terminated bool, packets int, err error) {
	rec, err := NewCallRecorder()
	if err != nil {
		return "", false, 0, err
	}

	if beep && player != nil {
		m.playRecordBeep(player)
	}

	session.mu.Lock()
	session.IVRRecorder = rec
	session.mu.Unlock()

	m.drainDTMF(session)
	deadline := time.After(maxDuration)
	term := strings.TrimSpace(terminator)

loop:
	for {
		select {
		case <-deadline:
			break loop
		case d, ok := <-session.DTMFBuffer:
			if !ok {
				break loop
			}
			if term != "" && string(d) == term {
				terminated = true
				break loop
			}
			// Ignore other digits while recording
		case <-time.After(200 * time.Millisecond):
			session.mu.Lock()
			status := session.Status
			session.mu.Unlock()
			if status != models.CallStatusAnswered {
				break loop
			}
		}
	}

	session.mu.Lock()
	session.IVRRecorder = nil
	session.mu.Unlock()

	path, count, stopErr := rec.Stop()
	if stopErr != nil {
		_ = os.Remove(path)
		return "", terminated, 0, stopErr
	}
	if count == 0 {
		_ = os.Remove(path)
		return "", terminated, 0, fmt.Errorf("no audio captured")
	}
	return path, terminated, count, nil
}

// recordedSeconds converts 20ms Opus packets into a duration the call log can store.
func recordedSeconds(packets int) int {
	if packets <= 0 {
		return 0
	}
	secs := packets * 20 / 1000
	if secs < 1 {
		secs = 1
	}
	return secs
}

// attachVoicemailRecording points the call log at the uploaded voicemail when
// the log has no call recording yet. The call-log player reads recording_s3_key.
func (m *Manager) attachVoicemailRecording(callLogID uuid.UUID, key string, packets int) {
	if m == nil || m.db == nil || callLogID == uuid.Nil || strings.TrimSpace(key) == "" {
		return
	}
	updates := map[string]any{"recording_s3_key": key}
	if secs := recordedSeconds(packets); secs > 0 {
		updates["recording_duration"] = secs
	}
	if err := m.db.Model(&models.CallLog{}).
		Where("id = ? AND (recording_s3_key = '' OR recording_s3_key IS NULL)", callLogID).
		Updates(updates).Error; err != nil {
		m.log.Warn("Failed to attach voicemail to call log", "error", err, "call_log_id", callLogID)
	}
}

// playRecordBeep plays the bundled beep into the call. A missing file falls
// back to a short silence so recording still starts.
func (m *Manager) playRecordBeep(player *AudioPlayer) {
	if player == nil {
		return
	}
	if m != nil && m.config != nil && m.config.AudioDir != "" {
		path := filepath.Join(m.config.AudioDir, defaultaudio.BeepFile)
		_, err := player.PlayFile(path)
		if err == nil {
			return
		}
		m.log.Warn("Voicemail beep failed", "error", err, "path", path)
	}
	player.PlaySilence(400 * time.Millisecond)
}

// voicemailBeepEnabled is true unless the node turns the beep off.
// A missing key stays on: the editor treats that as on, and prompts say
// "after the beep".
func voicemailBeepEnabled(config map[string]any) bool {
	if config == nil {
		return true
	}
	v, ok := config["beep"]
	if !ok || v == nil {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "false", "0", "no", "off":
			return false
		default:
			return true
		}
	default:
		return true
	}
}

// voicemailRecordSettings reads the editor keys (max_seconds, terminator)
// and the aliases saved by older flows (max_duration_seconds, finish_on_key).
func voicemailRecordSettings(config map[string]any) (seconds int, terminator string) {
	seconds = getConfigInt(config, "max_seconds", 0)
	if seconds == 0 {
		seconds = getConfigInt(config, "max_duration_seconds", 60)
	}
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 300 {
		seconds = 300
	}
	terminator, _ = config["terminator"].(string)
	if strings.TrimSpace(terminator) == "" {
		terminator, _ = config["finish_on_key"].(string)
	}
	terminator = strings.TrimSpace(terminator)
	if terminator == "" {
		terminator = "#"
	}
	return seconds, terminator
}

func (m *Manager) uploadIVRRecording(session *CallSession, localPath, purpose string) (string, error) {
	if m.recordings == nil {
		return "", fmt.Errorf("recordings storage not configured")
	}
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck

	key := fmt.Sprintf("ivr/%s/%s/%s-%s.ogg",
		session.OrganizationID.String(),
		session.CallLogID.String(),
		purpose,
		uuid.New().String()[:8],
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.recordings.Upload(ctx, key, f, "audio/ogg"); err != nil {
		return "", err
	}
	return key, nil
}

// executeVoicemail records caller audio, uploads it, stores key in a variable.
// Outcomes: default, timeout (no audio), error.
func (m *Manager) executeVoicemail(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	secs, terminator := voicemailRecordSettings(node.Config)
	storeAs, _ := node.Config["store_as"].(string)
	if storeAs == "" {
		storeAs = "voicemail_key"
	}
	beep := voicemailBeepEnabled(node.Config)
	prompt := m.resolvePromptAudio(node, ctx)
	if prompt != "" && m.config.AudioDir != "" && player != nil {
		_, _ = player.PlayFile(filepath.Join(m.config.AudioDir, prompt))
	}

	path, _, packets, err := m.recordCallerAudio(session, player, time.Duration(secs)*time.Second, terminator, beep)
	if err != nil {
		m.log.Warn("IVR voicemail capture failed", "error", err, "call_id", session.ID)
		return "timeout"
	}
	defer os.Remove(path) //nolint:errcheck

	key, err := m.uploadIVRRecording(session, path, "voicemail")
	if err != nil {
		m.log.Error("IVR voicemail upload failed", "error", err, "call_id", session.ID)
		return "error"
	}
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	ctx.Variables[storeAs] = key
	m.attachVoicemailRecording(session.CallLogID, key, packets)
	m.log.Info("IVR voicemail saved", "call_id", session.ID, "key", key)
	return "default"
}

// executeSpeechInput records audio then transcribes via OpenAI Whisper.
// Outcomes: default, timeout, error.
func (m *Manager) executeSpeechInput(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {
	secs := getConfigInt(node.Config, "max_seconds", 10)
	if secs < 1 {
		secs = 1
	}
	if secs > 60 {
		secs = 60
	}
	terminator, _ := node.Config["terminator"].(string)
	storeAs, _ := node.Config["store_as"].(string)
	if storeAs == "" {
		storeAs = "speech_text"
	}
	prompt := m.resolvePromptAudio(node, ctx)
	if prompt != "" && m.config.AudioDir != "" && player != nil {
		_, _ = player.PlayFile(filepath.Join(m.config.AudioDir, prompt))
	}

	path, _, _, err := m.recordCallerAudio(session, player, time.Duration(secs)*time.Second, terminator, false)
	if err != nil {
		m.log.Warn("IVR speech_input capture failed", "error", err, "call_id", session.ID)
		return "timeout"
	}
	defer os.Remove(path) //nolint:errcheck

	apiKey := m.resolveOpenAIKey(session.OrganizationID)
	if apiKey == "" {
		m.log.Error("IVR speech_input: no OpenAI API key configured", "call_id", session.ID)
		return "error"
	}
	lang, _ := node.Config["language"].(string)
	if lang == "" && session.Locale != "" {
		lang = session.Locale
	}
	text, err := transcribeWhisper(apiKey, path, lang)
	if err != nil {
		m.log.Error("IVR speech_input transcription failed", "error", err, "call_id", session.ID)
		return "error"
	}
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	ctx.Variables[storeAs] = text
	m.log.Info("IVR speech_input transcribed", "call_id", session.ID, "store_as", storeAs, "len", len(text))
	return "default"
}

func (m *Manager) resolveOpenAIKey(orgID uuid.UUID) string {
	if m.db != nil {
		var settings models.ChatbotSettings
		if err := m.db.Where("organization_id = ?", orgID).First(&settings).Error; err == nil {
			key := settings.AI.APIKey
			if key != "" && m.encryptionKey != "" {
				crypto.DecryptFields(m.encryptionKey, &key)
			}
			if key != "" && (settings.AI.Provider == models.AIProviderOpenAI || settings.AI.Provider == "") {
				return key
			}
		}
	}
	if m.aiConfig != nil && m.aiConfig.OpenAIKey != "" {
		return m.aiConfig.OpenAIKey
	}
	return ""
}

// transcribeWhisper calls OpenAI Whisper with an OGG/Opus file.
func transcribeWhisper(apiKey, filePath, language string) (string, error) {
	return transcribeWhisperAt("https://api.openai.com/v1/audio/transcriptions", apiKey, filePath, language)
}

func transcribeWhisperAt(endpoint, apiKey, filePath, language string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	_ = w.WriteField("model", "whisper-1")
	if language != "" {
		lang := language
		if i := strings.IndexByte(lang, '-'); i > 0 {
			lang = lang[:i]
		}
		_ = w.WriteField("language", lang)
	}
	_ = w.WriteField("response_format", "json")
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("whisper status %d: %s", resp.StatusCode, truncateStr(string(raw), 200))
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	return strings.TrimSpace(parsed.Text), nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
