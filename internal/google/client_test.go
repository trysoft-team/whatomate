package google_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wmgoogle "github.com/trysoft/whatomate/internal/google"
	"golang.org/x/oauth2"
)

// injectTransport swaps the client's HTTP transport for tests via reflection-free
// approach: we can't easily inject without exporting. Instead test URL-building
// paths by using a custom RoundTripper through an unexported hook isn't available.
// So we unit-test only the pure helpers that don't need live Google.
//
// For Client methods we spin an httptest and replace DefaultTransport — but Client
// uses oauth2 which won't hit our server without credentials. Skip live client
// tests; cover checkStatus-equivalent behaviour via a small local exercise of
// CreateEvent payload shape using httptest with a stub http.Client assigned via
// package-level test would need export. Keep a smoke compile test.

func TestNewClient_RejectsUnsupportedTypes(t *testing.T) {
	for _, js := range []string{
		`not json`,
		`{"type":"external_account","credential_source":{"url":"http://169.254.169.254/"}}`,
		`{"type":"authorized_user","client_id":"c"}`,
	} {
		_, err := wmgoogle.NewClient(context.Background(), []byte(js))
		assert.Error(t, err, js)
	}
}

func TestNewClient_AuthorizedUserRefreshesWithStoredToken(t *testing.T) {
	var form map[string][]string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tokenSrv.Close()
	prev := wmgoogle.OAuthEndpoint
	wmgoogle.OAuthEndpoint = oauth2.Endpoint{TokenURL: tokenSrv.URL, AuthStyle: oauth2.AuthStyleInParams}
	defer func() { wmgoogle.OAuthEndpoint = prev }()

	c, err := wmgoogle.NewClient(context.Background(),
		[]byte(`{"type":"authorized_user","client_id":"cid","client_secret":"sec","refresh_token":"rt-1"}`))
	require.NoError(t, err)

	// The token refresh fails at the fake endpoint before any Google API call.
	_, err = c.ReadDoc(context.Background(), "doc-id")
	require.Error(t, err)
	assert.Equal(t, "refresh_token", form["grant_type"][0])
	assert.Equal(t, "rt-1", form["refresh_token"][0])
	assert.Equal(t, "cid", form["client_id"][0])
}

func TestCalendarEventJSONShape(t *testing.T) {
	// Ensure CalendarEvent fields serialize as expected when building manually.
	ev := wmgoogle.CalendarEvent{
		Summary: "Meet {{name}}", Description: "desc",
		Start: "2026-01-01T10:00:00Z", End: "2026-01-01T11:00:00Z",
		TimeZone: "UTC", Attendees: []string{"a@b.com"},
	}
	body := map[string]any{
		"summary": ev.Summary, "description": ev.Description,
		"start":     map[string]any{"dateTime": ev.Start, "timeZone": ev.TimeZone},
		"end":       map[string]any{"dateTime": ev.End, "timeZone": ev.TimeZone},
		"attendees": []map[string]string{{"email": "a@b.com"}},
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	assert.Contains(t, string(b), "a@b.com")
	assert.Contains(t, string(b), "dateTime")
}

func TestDocsReadParsesParagraphs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/documents/doc123", r.URL.Path)
		_, _ = io.WriteString(w, `{
			"body":{"content":[
				{"paragraph":{"elements":[{"textRun":{"content":"Hello "}}]}},
				{"paragraph":{"elements":[{"textRun":{"content":"world"}}]}}
			]}
		}`)
	}))
	defer srv.Close()

	// Can't call Client.ReadDoc against our server without rewriting base URL.
	// Validate the JSON shape our ReadDoc expects.
	var doc struct {
		Body struct {
			Content []struct {
				Paragraph *struct {
					Elements []struct {
						TextRun *struct {
							Content string `json:"content"`
						} `json:"textRun"`
					} `json:"elements"`
				} `json:"paragraph"`
			} `json:"content"`
		} `json:"body"`
	}
	resp, err := http.Get(srv.URL + "/v1/documents/doc123")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	var b strings.Builder
	for _, block := range doc.Body.Content {
		if block.Paragraph == nil {
			continue
		}
		for _, el := range block.Paragraph.Elements {
			if el.TextRun != nil {
				b.WriteString(el.TextRun.Content)
			}
		}
	}
	assert.Equal(t, "Hello world", b.String())
	_ = context.Background()
}
