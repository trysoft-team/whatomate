package calling

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/zerodha/logf"
)

func testLogger() logf.Logger {
	return logf.New(logf.Opts{Writer: io.Discard, Level: logf.InfoLevel})
}

func TestResolveSendMessagePhone(t *testing.T) {
	t.Parallel()
	session := &CallSession{CallerPhone: "+15550001"}

	// Empty config → session caller
	assert.Equal(t, "+15550001", resolveSendMessagePhone(
		&IVRNode{Config: map[string]any{"body": "hi"}},
		session,
		map[string]string{},
	))

	// caller_phone var preferred over empty to
	assert.Equal(t, "+15559999", resolveSendMessagePhone(
		&IVRNode{Config: map[string]any{"to": ""}},
		session,
		map[string]string{"caller_phone": "+15559999"},
	))

	// Explicit to with template
	assert.Equal(t, "+441234", resolveSendMessagePhone(
		&IVRNode{Config: map[string]any{"to": "{{guest_phone}}"}},
		session,
		map[string]string{"guest_phone": "+441234", "caller_phone": "+15559999"},
	))

	// phone key alias
	assert.Equal(t, "263771234567", resolveSendMessagePhone(
		&IVRNode{Config: map[string]any{"phone": "263771234567"}},
		session,
		nil,
	))

	// Tool arg phone overrides caller when config to is empty / unresolved
	assert.Equal(t, "15550001234", resolveSendMessagePhone(
		&IVRNode{Config: map[string]any{"to": "{{phone}}"}},
		session,
		map[string]string{"phone": "15550001234", "caller_phone": "+15550001"},
	))
	assert.Equal(t, "+15550001", resolveSendMessagePhone(
		&IVRNode{Config: map[string]any{"to": "{{phone}}"}},
		session,
		map[string]string{"caller_phone": "+15550001"},
	))
}

func TestFallbackSendMessageBodyAndStripTemplates(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "hello", stripUnresolvedTemplates("hello {{missing}}"))
	assert.Equal(t, "", stripUnresolvedTemplates("{{message}}"))
	assert.Equal(t, "hi there", fallbackSendMessageBody(map[string]string{"message": "hi there"}))
	assert.Equal(t, "Confirmation\nName: Ada\nDate: 2026-09-25\nTime: 09:00",
		fallbackSendMessageBody(map[string]string{
			"guest_name": "Ada",
			"date":       "2026-09-25",
			"time":       "09:00",
		}))
	assert.Equal(t, "Your request is confirmed. Thank you!", fallbackSendMessageBody(map[string]string{}))
}

func TestStringSliceFromIVRConfig(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"a", "b"}, stringSliceFromIVRConfig(map[string]any{
		"add": []any{"a", "b", ""},
	}, "add"))
	assert.Equal(t, []string{"x", "y"}, stringSliceFromIVRConfig(map[string]any{
		"add": "x, y",
	}, "add"))
}

func TestExecuteLanguageSetsVariable(t *testing.T) {
	t.Parallel()
	m := &Manager{log: testLogger()}
	session := &CallSession{}
	ctx := &IVRContext{Variables: map[string]string{}}
	node := &IVRNode{Config: map[string]any{"locale": "fr-FR"}}
	assert.Equal(t, "default", m.executeLanguage(session, node, ctx))
	assert.Equal(t, "fr-FR", session.Locale)
	assert.Equal(t, "fr-FR", ctx.Variables["locale"])
}

func TestTranscribeWhisper_ParsesResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/audio/transcriptions", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer test-key")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": " hello world "})
	}))
	t.Cleanup(srv.Close)

	// Point Whisper at test server by temporarily replacing URL via rewriting
	// the function's hardcoded host is production — write a small OGG and call
	// through a local helper that posts to srv.URL.
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("OggSfake"), 0o600))

	text, err := transcribeWhisperAt(srv.URL+"/v1/audio/transcriptions", "test-key", path, "en")
	require.NoError(t, err)
	assert.Equal(t, "hello world", text)
}

func TestIVROpenAIChat_ParsesReply(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "Hi there"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	reply, err := ivrOpenAIChatAt(srv.URL+"/v1/chat/completions", "k", "gpt-4o-mini", "sys", "user", 50)
	require.NoError(t, err)
	assert.Equal(t, "Hi there", reply)
}

func TestUploadIVRRecording_RequiresStorage(t *testing.T) {
	t.Parallel()
	m := &Manager{log: testLogger()}
	_, err := m.uploadIVRRecording(&CallSession{
		OrganizationID: uuid.New(),
		CallLogID:      uuid.New(),
	}, "/tmp/nope.ogg", "voicemail")
	require.Error(t, err)
}

func TestMatchIVRConditionStillWorks(t *testing.T) {
	t.Parallel()
	assert.True(t, matchIVRCondition(map[string]any{"operator": "equals", "value": "a"}, "a"))
}

func TestExecuteSetLabels_NoContact(t *testing.T) {
	t.Parallel()
	m := &Manager{log: testLogger()}
	out := m.executeSetLabels(&CallSession{ID: "c1"}, &IVRNode{Config: map[string]any{"add": []any{"vip"}}}, &IVRContext{})
	assert.Equal(t, "error", out)
}

// Ensure models import used for compile of related helpers.
var _ = models.CallStatusAnswered
