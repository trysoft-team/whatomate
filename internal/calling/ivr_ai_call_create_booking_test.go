package calling

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createBookingDef(url, method string) *aiCallToolDef {
	return &aiCallToolDef{
		Name:         "create_booking",
		Handler:      "http",
		ReturnToCall: true, // same as UI checkbox — must still run inline HTTP
		HTTP: struct {
			URL          string
			Method       string
			Headers      map[string]string
			BodyTemplate string
		}{
			URL:          url,
			Method:       method,
			BodyTemplate: `{"date":"{{date}}","time":"{{time}}","guest_name":"{{guest_name}}","phone":"{{caller_phone}}"}`,
		},
	}
}

func TestCreateBooking_HTTPSuccess2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		b, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(b), `"guest_name":"Ada"`)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"booking_id":"B99","ok":true}`)
	}))
	t.Cleanup(srv.Close)

	m := &Manager{log: testLogger()}
	ctx := &IVRContext{Variables: map[string]string{"caller_phone": "15550001234"}}
	res, xfer, hang := m.runAICallTool(
		createBookingDef(srv.URL, "POST"),
		`{"date":"2026-09-25","time":"14:00","guest_name":"Ada"}`,
		&CallSession{ID: "c1"},
		ctx,
		"",
	)
	assert.False(t, xfer)
	assert.False(t, hang)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(res), &payload))
	assert.Equal(t, true, payload["ok"])
	assert.Equal(t, true, payload["success"])
	assert.EqualValues(t, 201, payload["status"])
	assert.Contains(t, payload["body"], "B99")
	assert.Contains(t, payload["message"], "succeeded")
}

func TestCreateBooking_HTTPFailNon2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, `{"status":"error","message":"Request method not allowed"}`)
	}))
	t.Cleanup(srv.Close)

	m := &Manager{log: testLogger()}
	ctx := &IVRContext{Variables: map[string]string{"caller_phone": "15550001234"}}
	res, _, _ := m.runAICallTool(
		createBookingDef(srv.URL, "POST"),
		`{"date":"2026-09-25","time":"14:00","guest_name":"Ada"}`,
		&CallSession{ID: "c1"},
		ctx,
		"",
	)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(res), &payload))
	assert.Equal(t, false, payload["ok"])
	assert.Equal(t, false, payload["success"])
	assert.EqualValues(t, 405, payload["status"])
	assert.Contains(t, payload["message"], "failed")
}

// LIVE_BOOKING_URL is a public URL of a running Whatomate, e.g. an ngrok tunnel.
func TestCreateBooking_LiveNgrokReportsFail(t *testing.T) {
	base := os.Getenv("LIVE_BOOKING_URL")
	if base == "" {
		t.Skip("set LIVE_BOOKING_URL to run against a live endpoint")
	}
	url := strings.TrimRight(base, "/") + "/api/bookings"
	m := &Manager{log: testLogger()}
	ctx := &IVRContext{Variables: map[string]string{"caller_phone": "15550001234"}}
	def := createBookingDef(url, "POST")
	if def.HTTP.Headers == nil {
		def.HTTP.Headers = map[string]string{}
	}
	def.HTTP.Headers["ngrok-skip-browser-warning"] = "1"
	def.HTTP.Headers["Content-Type"] = "application/json"

	res, _, _ := m.runAICallTool(
		def,
		`{"date":"2026-09-25","time":"14:00","guest_name":"Ada"}`,
		&CallSession{ID: "live"},
		ctx,
		"",
	)
	t.Logf("live create_booking result: %s", res)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(res), &payload))
	// Live Whatomate /api/bookings rejects POST (Allow: GET, OPTIONS) → tool must report fail
	assert.Equal(t, false, payload["success"], "expected fail against live 405 endpoint")
	assert.Equal(t, false, payload["ok"])
	st, _ := payload["status"].(float64)
	assert.True(t, st == 405 || st >= 400, "status=%v", payload["status"])
}
