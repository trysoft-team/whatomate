package calling

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAICallTools(t *testing.T) {
	raw := []any{
		map[string]any{
			"name":        "lookup",
			"description": "Lookup order",
			"handler":     "http",
			"http": map[string]any{
				"url":           "https://example.com/{{order_id}}",
				"method":        "POST",
				"body_template": `{"id":"{{order_id}}"}`,
			},
		},
		map[string]any{"name": "hangup", "handler": "hangup"},
	}
	tools := parseAICallTools(raw)
	require.Len(t, tools, 2)
	assert.Equal(t, "lookup", tools[0].Name)
	assert.Equal(t, "https://example.com/{{order_id}}", tools[0].HTTP.URL)
	assert.Equal(t, "hangup", tools[1].Handler)
}

func TestToolsToOpenAISchema(t *testing.T) {
	schema := toolsToOpenAISchema([]aiCallToolDef{{
		Name:        "lookup",
		Description: "d",
		Parameters:  map[string]any{"type": "object"},
	}})
	require.Len(t, schema, 1)
	assert.Equal(t, "function", schema[0]["type"])
}

func TestRunAICallTool_SetVariableAndHangup(t *testing.T) {
	m := &Manager{}
	ctx := &IVRContext{Variables: map[string]string{}}
	res, xfer, hang := m.runAICallTool(&aiCallToolDef{Name: "sv", Handler: "set_variable"}, `{"foo":"bar"}`, nil, ctx, "")
	assert.Contains(t, res, "ok")
	assert.False(t, xfer)
	assert.False(t, hang)
	assert.Equal(t, "bar", ctx.Variables["foo"])

	_, _, hang = m.runAICallTool(&aiCallToolDef{Name: "h", Handler: "hangup"}, `{}`, nil, ctx, "")
	assert.True(t, hang)
}

func TestRunAICallTool_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), "42")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(srv.Close)

	m := &Manager{}
	ctx := &IVRContext{Variables: map[string]string{"order_id": "42"}}
	def := &aiCallToolDef{
		Name:    "lookup",
		Handler: "http",
	}
	def.HTTP.URL = srv.URL
	def.HTTP.Method = "POST"
	def.HTTP.BodyTemplate = `{"id":"{{order_id}}"}`
	res, xfer, hang := m.runAICallTool(def, `{}`, nil, ctx, "")
	assert.False(t, xfer)
	assert.False(t, hang)
	assert.Contains(t, res, "ok")
}

func TestIvrOpenAIChatToolsAt_ParsesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": nil,
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "lookup",
							"arguments": `{"order_id":"1"}`,
						},
					}},
				},
			}},
		})
	}))
	t.Cleanup(srv.Close)

	content, tcs, err := ivrOpenAIChatToolsAt(srv.URL, "k", "gpt-4o-mini",
		[]map[string]any{{"role": "user", "content": "hi"}},
		[]map[string]any{{"type": "function", "function": map[string]any{"name": "lookup"}}},
		100,
	)
	require.NoError(t, err)
	assert.Empty(t, content)
	require.Len(t, tcs, 1)
	fn := tcs[0]["function"].(map[string]any)
	assert.Equal(t, "lookup", fn["name"])
}

func TestExecuteFunction_SuccessAndError(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"id":"abc"}}`)
	}))
	t.Cleanup(okSrv.Close)

	m := &Manager{log: testLogger()}
	ctx := &IVRContext{Variables: map[string]string{}}
	node := &IVRNode{Config: map[string]any{
		"url":               okSrv.URL,
		"method":            "GET",
		"response_store_as": "result",
		"response_path":     "data.id",
	}}
	out := m.executeFunction(&CallSession{ID: "c1"}, node, ctx)
	assert.Equal(t, "default", out)
	assert.Equal(t, "abc", ctx.Variables["result"])

	node.Config["url"] = "http://127.0.0.1:1"
	assert.Equal(t, "error", m.executeFunction(&CallSession{ID: "c1"}, node, ctx))
}

func TestInterpolateTemplate_TripleBrace(t *testing.T) {
	got := interpolateTemplate(`{"x":{{{val}}},"y":"{{val}}"}`, map[string]string{"val": "1"})
	assert.Equal(t, `{"x":1,"y":"1"}`, got)
}

func TestJsonPathString(t *testing.T) {
	v, ok := jsonPathString(`{"a":{"b":3}}`, "a.b")
	assert.True(t, ok)
	assert.Equal(t, "3", v)
}
