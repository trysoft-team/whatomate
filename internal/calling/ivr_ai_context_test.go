package calling

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func TestMergeSystemWithAIContext(t *testing.T) {
	assert.Equal(t, "sys", mergeSystemWithAIContext("sys", ""))
	assert.Equal(t, "ctx", mergeSystemWithAIContext("", "ctx"))
	assert.Equal(t, "sys\n\nctx", mergeSystemWithAIContext("sys", "ctx"))
}

func TestFetchIVRAIContextAPI_ResponsePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "1555", r.URL.Query().Get("phone"))
		_, _ = io.WriteString(w, `{"data":{"price":"9"}}`)
	}))
	t.Cleanup(srv.Close)

	m := &Manager{log: testLogger()}
	got, err := m.fetchIVRAIContextAPI(models.JSONB{
		"url":           srv.URL + "?phone={{phone_number}}",
		"method":        "GET",
		"response_path": "data.price",
	}, map[string]string{"phone_number": "1555"})
	require.NoError(t, err)
	assert.Equal(t, "9", got)
}

func TestBuildIVRAIContext_NoDB(t *testing.T) {
	m := &Manager{log: testLogger()}
	assert.Empty(t, m.buildIVRAIContext(&CallSession{}, nil, "hi", nil))
}

func TestFilterAIContextsByConfig(t *testing.T) {
	t.Parallel()
	id1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	id2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	id3 := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	all := []models.AIContext{
		{BaseModel: models.BaseModel{ID: id1}, Name: "A"},
		{BaseModel: models.BaseModel{ID: id2}, Name: "B"},
		{BaseModel: models.BaseModel{ID: id3}, Name: "C"},
	}

	// Default / all → keep everything
	got := filterAIContextsByConfig(all, nil)
	require.Len(t, got, 3)
	got = filterAIContextsByConfig(all, map[string]any{"ai_context_mode": "all"})
	require.Len(t, got, 3)

	// Selected with IDs → only those (order preserved from input list)
	got = filterAIContextsByConfig(all, map[string]any{
		"ai_context_mode": "selected",
		"ai_context_ids":  []any{id2.String(), id1.String()},
	})
	require.Len(t, got, 2)
	assert.Equal(t, id1, got[0].ID)
	assert.Equal(t, id2, got[1].ID)

	// Selected with empty IDs → none
	got = filterAIContextsByConfig(all, map[string]any{
		"ai_context_mode": "selected",
		"ai_context_ids":  []any{},
	})
	assert.Empty(t, got)

	// Non-empty ids without mode → filter (compat)
	got = filterAIContextsByConfig(all, map[string]any{
		"ai_context_ids": []string{id3.String()},
	})
	require.Len(t, got, 1)
	assert.Equal(t, id3, got[0].ID)

	// Unknown / disabled IDs simply omit (id not in loaded enabled set)
	got = filterAIContextsByConfig(all, map[string]any{
		"ai_context_mode": "selected",
		"ai_context_ids":  []any{id2.String(), "99999999-9999-9999-9999-999999999999"},
	})
	require.Len(t, got, 1)
	assert.Equal(t, id2, got[0].ID)
}

func TestFormatIVRExtraKnowledge(t *testing.T) {
	t.Parallel()
	assert.Empty(t, formatIVRExtraKnowledge(nil, nil))
	assert.Empty(t, formatIVRExtraKnowledge(map[string]any{}, nil))
	assert.Empty(t, formatIVRExtraKnowledge(map[string]any{
		"extra_knowledge": []any{
			map[string]any{"name": "", "value": "x"},
			map[string]any{"name": "Skip", "value": ""},
		},
	}, nil))

	got := formatIVRExtraKnowledge(map[string]any{
		"extra_knowledge": []any{
			map[string]any{"name": "Caller name", "value": "Test User"},
			map[string]any{"name": "Phone", "value": "{{caller_phone}}"},
			map[string]any{"name": "  ", "value": "nope"},
		},
	}, map[string]string{"caller_phone": "+15551212"})
	assert.Contains(t, got, "## Extra Knowledge")
	assert.Contains(t, got, "- **Caller name**: Test User")
	assert.Contains(t, got, "- **Phone**: +15551212")
	assert.NotContains(t, got, "nope")
}

func TestComposeAICallSystemPrompt_ExtraKnowledge(t *testing.T) {
	t.Parallel()
	m := &Manager{log: testLogger()}
	session := &CallSession{CallerName: "Ada", CallerPhone: "+1"}
	ctx := &IVRContext{Variables: map[string]string{
		"caller_name":  "Ada",
		"caller_phone": "+1",
	}}
	out := m.composeAICallSystemPrompt(session, ctx, "You are helpful.", "", map[string]any{
		"extra_knowledge": []any{
			map[string]any{"name": "Caller name", "value": "{{caller_name}}"},
		},
	})
	assert.Contains(t, out, "You are helpful.")
	assert.Contains(t, out, "## Extra Knowledge")
	assert.Contains(t, out, "- **Caller name**: Ada")
}
