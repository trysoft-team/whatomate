package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/flowgraph"
)

func TestExtractJSONObject(t *testing.T) {
	t.Parallel()
	raw := "Here you go:\n```json\n{\"name\":\"Hi\",\"graph\":{\"version\":2}}\n```\n"
	got := extractJSONObject(raw)
	assert.Contains(t, got, `"name":"Hi"`)

	got2 := extractJSONObject(`{"a":1}`)
	assert.Equal(t, `{"a":1}`, got2)
}

func TestParseAIFlowDraft_AndSanitize(t *testing.T) {
	t.Parallel()
	raw := `{
	  "name": "Welcome",
	  "description": "Greet and end",
	  "trigger_keywords": ["hi", "hello", "hi"],
	  "graph": {
	    "version": 2,
	    "entry_node": "__start__",
	    "nodes": [
	      {"id": "__start__", "type": "start", "label": "Start", "position": {"x": 100, "y": 80}, "config": {}},
	      {"id": "n1", "type": "message", "label": "Greet", "position": {"x": 100, "y": 240}, "config": {"message": "Hello"}},
	      {"id": "bad", "type": "mysql_query", "label": "Nope", "position": {"x": 0, "y": 0}, "config": {}},
	      {"id": "n2", "type": "end", "label": "End", "position": {"x": 100, "y": 400}, "config": {}}
	    ],
	    "edges": [
	      {"from": "__start__", "to": "n1", "condition": "default"},
	      {"from": "n1", "to": "n2", "condition": "default"},
	      {"from": "n1", "to": "missing", "condition": "default"}
	    ]
	  }
	}`
	draft, err := parseAIFlowDraft(raw)
	require.NoError(t, err)
	assert.Equal(t, "Welcome", draft.Name)

	graph := sanitizeAIGeneratedGraph(draft.Graph)
	require.NotNil(t, graph)
	assert.EqualValues(t, 2, graph["version"])
	assert.Equal(t, "__start__", graph["entry_node"])

	nodes, ok := graph["nodes"].([]any)
	require.True(t, ok)
	assert.Len(t, nodes, 3) // start, message, end — mysql dropped

	edges, ok := graph["edges"].([]any)
	require.True(t, ok)
	assert.Len(t, edges, 2)

	kw := sanitizeTriggerKeywords(draft.TriggerKeywords)
	assert.Equal(t, []string{"hi", "hello"}, kw)
}

func TestSanitizeAIGeneratedGraph_InjectsStart(t *testing.T) {
	t.Parallel()
	g := &ChatGraph{
		Version:   2,
		EntryNode: "n1",
		Nodes: []ChatNode{
			{ID: "n1", Type: ChatNodeMessage, Label: "Hi", Position: flowgraph.Position{X: 0, Y: 0}, Config: map[string]any{"message": "Hi"}},
			{ID: "n2", Type: ChatNodeEnd, Label: "End", Config: map[string]any{}},
		},
		Edges: []ChatEdge{{From: "n1", To: "n2", Condition: "default"}},
	}
	out := sanitizeAIGeneratedGraph(g)
	require.NotNil(t, out)
	assert.Equal(t, "__start__", out["entry_node"])
	nodes := out["nodes"].([]any)
	assert.GreaterOrEqual(t, len(nodes), 3)
	edges := out["edges"].([]any)
	hasStartEdge := false
	for _, e := range edges {
		m := e.(map[string]any)
		if m["from"] == "__start__" {
			hasStartEdge = true
		}
	}
	assert.True(t, hasStartEdge)
}
