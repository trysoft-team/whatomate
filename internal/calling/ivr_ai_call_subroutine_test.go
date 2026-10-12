package calling

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAICallFnEdge_ExactOnly(t *testing.T) {
	t.Parallel()
	g := &IVRFlowGraph{
		Nodes: []IVRNode{
			{ID: "ai", Type: IVRNodeAICall},
			{ID: "http", Type: IVRNodeHTTPCallback},
			{ID: "done", Type: IVRNodeHangup},
		},
		Edges: []IVREdge{
			{From: "ai", To: "done", Condition: "default"},
			{From: "ai", To: "http", Condition: "fn:create_booking"},
		},
	}
	g.BuildMaps()
	assert.Equal(t, "http", resolveAICallFnEdge(g, "ai", "create_booking"))
	assert.Equal(t, "", resolveAICallFnEdge(g, "ai", "other"))
	// Must not fall back to default
	assert.Equal(t, "", resolveAICallFnEdge(g, "ai", ""))
}

func TestRunIVRSubroutine_HTTPThenCondition(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"booking_id":"B1"}`)
	}))
	t.Cleanup(srv.Close)

	g := &IVRFlowGraph{
		Nodes: []IVRNode{
			{
				ID:   "http1",
				Type: IVRNodeHTTPCallback,
				Config: map[string]any{
					"url":               srv.URL,
					"method":            "POST",
					"response_store_as": "booking_resp",
				},
			},
			{
				ID:   "cond1",
				Type: IVRNodeCondition,
				Config: map[string]any{
					"field":    "last_http_status",
					"operator": "equals",
					"value":    "200",
				},
			},
			{
				ID:   "ok",
				Type: IVRNodeSetVariable,
				Config: map[string]any{
					"assignments": []any{
						map[string]any{"name": "booking_ok", "value": "true"},
					},
				},
			},
			{
				ID:   "bad",
				Type: IVRNodeSetVariable,
				Config: map[string]any{
					"assignments": []any{
						map[string]any{"name": "booking_ok", "value": "false"},
					},
				},
			},
		},
		Edges: []IVREdge{
			{From: "http1", To: "cond1", Condition: "http:2xx"},
			{From: "http1", To: "bad", Condition: "http:non2xx"},
			{From: "cond1", To: "ok", Condition: "true"},
			{From: "cond1", To: "bad", Condition: "false"},
		},
	}
	g.BuildMaps()

	m := &Manager{log: testLogger()}
	session := &CallSession{ID: "c1", IVRGraph: g, AICallNodeID: "ai"}
	ctx := &IVRContext{Variables: map[string]string{}}

	out := m.runIVRSubroutine(session, g, ctx, "http1", "create_booking")
	require.True(t, out.OK, out.Error)
	assert.Equal(t, "true", ctx.Variables["booking_ok"])
	assert.Contains(t, out.HTTPBody, "booking_id")
	assert.Contains(t, out.Path, "http1")
	assert.Contains(t, out.Path, "cond1")
	assert.Contains(t, out.Path, "ok")
}

func TestRunAICallTool_PrefersLinkedSubroutine(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `ok`)
	}))
	t.Cleanup(srv.Close)

	g := &IVRFlowGraph{
		Nodes: []IVRNode{
			{ID: "ai", Type: IVRNodeAICall, Config: map[string]any{}},
			{
				ID:   "http1",
				Type: IVRNodeHTTPCallback,
				Config: map[string]any{
					"url":    srv.URL,
					"method": "POST",
				},
			},
		},
		Edges: []IVREdge{
			{From: "ai", To: "http1", Condition: "fn:book"},
		},
	}
	g.BuildMaps()

	m := &Manager{log: testLogger()}
	session := &CallSession{ID: "c1", IVRGraph: g, AICallNodeID: "ai"}
	ctx := &IVRContext{Variables: map[string]string{}}
	def := &aiCallToolDef{
		Name:    "book",
		Handler: "nodes",
	}

	result, transfer, hangup := m.runAICallTool(def, `{"date":"2026-09-24"}`, session, ctx, "")
	assert.False(t, transfer)
	assert.False(t, hangup)
	assert.Contains(t, result, `"ok":true`)
	assert.Equal(t, "2026-09-24", ctx.Variables["date"])
	assert.Equal(t, "200", ctx.Variables["last_http_status"])
}

func TestRunAICallTool_HTTPIgnoresOrphanFnEdge(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `inline`)
	}))
	t.Cleanup(srv.Close)

	g := &IVRFlowGraph{
		Nodes: []IVRNode{
			{ID: "ai", Type: IVRNodeAICall},
			{
				ID:     "http1",
				Type:   IVRNodeHTTPCallback,
				Config: map[string]any{"url": "http://should-not-hit.invalid", "method": "POST"},
			},
		},
		Edges: []IVREdge{
			{From: "ai", To: "http1", Condition: "fn:book"},
		},
	}
	g.BuildMaps()

	m := &Manager{log: testLogger()}
	session := &CallSession{ID: "c1", IVRGraph: g, AICallNodeID: "ai"}
	ctx := &IVRContext{Variables: map[string]string{}}
	def := &aiCallToolDef{
		Name:         "book",
		Handler:      "http",
		ReturnToCall: true, // must NOT divert to nodes subroutine
		HTTP: struct {
			URL          string
			Method       string
			Headers      map[string]string
			BodyTemplate string
		}{URL: srv.URL, Method: "POST"},
	}
	result, _, _ := m.runAICallTool(def, `{}`, session, ctx, "")
	assert.Contains(t, result, `"success":true`)
	assert.Contains(t, result, "inline")
	assert.NotContains(t, result, "should-not-hit")
	assert.NotContains(t, result, "no linked nodes")
}

func TestRunAICallTool_NodesWithoutLink(t *testing.T) {
	t.Parallel()
	m := &Manager{log: testLogger()}
	session := &CallSession{ID: "c1", AICallNodeID: "ai", IVRGraph: &IVRFlowGraph{}}
	session.IVRGraph.BuildMaps()
	ctx := &IVRContext{Variables: map[string]string{}}
	def := &aiCallToolDef{Name: "notify", Handler: "nodes"}
	result, _, _ := m.runAICallTool(def, `{}`, session, ctx, "")
	assert.Contains(t, result, `"ok":false`)
	assert.Contains(t, result, "no linked nodes")
}

func TestParseAICallTools_ReturnToCallDoesNotForceNodes(t *testing.T) {
	t.Parallel()
	tools := parseAICallTools([]any{
		map[string]any{"name": "x", "handler": "http", "return_to_call": true},
	})
	require.Len(t, tools, 1)
	assert.Equal(t, "http", tools[0].Handler)
	assert.True(t, tools[0].ReturnToCall)
	assert.False(t, aiCallToolUsesNodes(&tools[0]))
}

func TestRunAICallTool_NoLinkFallsBackToInline(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `inline`)
	}))
	t.Cleanup(srv.Close)

	m := &Manager{log: testLogger()}
	session := &CallSession{ID: "c1", AICallNodeID: "ai", IVRGraph: &IVRFlowGraph{}}
	session.IVRGraph.BuildMaps()
	ctx := &IVRContext{Variables: map[string]string{}}
	def := &aiCallToolDef{
		Name:    "book",
		Handler: "http",
		HTTP: struct {
			URL          string
			Method       string
			Headers      map[string]string
			BodyTemplate string
		}{URL: srv.URL, Method: "POST"},
	}
	result, _, _ := m.runAICallTool(def, `{}`, session, ctx, "")
	assert.Contains(t, result, `"success":true`)
	assert.Contains(t, result, `"ok":true`)
	assert.Contains(t, result, "inline")
}

func TestRunIVRSubroutine_RejectsAudioNode(t *testing.T) {
	t.Parallel()
	g := &IVRFlowGraph{
		Nodes: []IVRNode{
			{ID: "greet", Type: IVRNodeGreeting, Config: map[string]any{}},
		},
	}
	g.BuildMaps()
	m := &Manager{log: testLogger()}
	out := m.runIVRSubroutine(&CallSession{}, g, &IVRContext{Variables: map[string]string{}}, "greet", "x")
	assert.False(t, out.OK)
	assert.Contains(t, out.Error, "not allowed")
}
