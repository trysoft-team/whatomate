package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/calling"
	"github.com/trysoft/whatomate/internal/flowgraph"
)

func TestParseAIIVRFlowDraft_AndSanitize(t *testing.T) {
	t.Parallel()
	raw := `{
	  "name": "Main Line",
	  "description": "Greet and hang up",
	  "menu": {
	    "version": 2,
	    "entry_node": "__start__",
	    "nodes": [
	      {"id": "__start__", "type": "start", "label": "Start", "position": {"x": 100, "y": 80}, "config": {}},
	      {"id": "n1", "type": "greeting", "label": "Hi", "position": {"x": 100, "y": 240}, "config": {"greeting_text": "Hello"}},
	      {"id": "bad", "type": "mysql_query", "label": "Nope", "position": {"x": 0, "y": 0}, "config": {}},
	      {"id": "n2", "type": "hangup", "label": "Bye", "position": {"x": 100, "y": 400}, "config": {}}
	    ],
	    "edges": [
	      {"from": "__start__", "to": "n1", "condition": "default"},
	      {"from": "n1", "to": "n2", "condition": "default"},
	      {"from": "n1", "to": "missing", "condition": "default"}
	    ]
	  }
	}`
	draft, err := parseAIIVRFlowDraft(raw)
	require.NoError(t, err)
	assert.Equal(t, "Main Line", draft.Name)

	menu := sanitizeAIGeneratedIVRMenu(draft.Menu)
	require.NotNil(t, menu)
	assert.EqualValues(t, 2, menu["version"])
	assert.Equal(t, "__start__", menu["entry_node"])

	nodes, ok := menu["nodes"].([]any)
	require.True(t, ok)
	assert.Len(t, nodes, 3) // start, greeting, hangup — mysql dropped

	edges, ok := menu["edges"].([]any)
	require.True(t, ok)
	assert.Len(t, edges, 2)
}

func TestSanitizeAIGeneratedIVRMenu_InjectsStart(t *testing.T) {
	t.Parallel()
	g := &calling.IVRFlowGraph{
		Version:   2,
		EntryNode: "n1",
		Nodes: []calling.IVRNode{
			{ID: "n1", Type: calling.IVRNodeGreeting, Label: "Hi", Position: flowgraph.Position{X: 0, Y: 0}, Config: map[string]any{"greeting_text": "Hi"}},
			{ID: "n2", Type: calling.IVRNodeHangup, Label: "End", Config: map[string]any{}},
		},
		Edges: []calling.IVREdge{{From: "n1", To: "n2", Condition: "default"}},
	}
	out := sanitizeAIGeneratedIVRMenu(g)
	require.NotNil(t, out)
	assert.Equal(t, "__start__", out["entry_node"])
	nodes := out["nodes"].([]any)
	assert.GreaterOrEqual(t, len(nodes), 3)
	edges := out["edges"].([]any)
	assert.GreaterOrEqual(t, len(edges), 2)
}

func TestParseAIIVRFlowDraft_GraphAlias(t *testing.T) {
	t.Parallel()
	raw := `{"name":"X","graph":{"version":2,"entry_node":"__start__","nodes":[{"id":"__start__","type":"start","config":{}},{"id":"h1","type":"hangup","config":{}}],"edges":[{"from":"__start__","to":"h1","condition":"default"}]}}`
	draft, err := parseAIIVRFlowDraft(raw)
	require.NoError(t, err)
	require.NotNil(t, draft.Menu)
	menu := sanitizeAIGeneratedIVRMenu(draft.Menu)
	require.NotNil(t, menu)
}
