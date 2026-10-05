package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func aiScreenChildren(t *testing.T, screen any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, c := range screenChildren(screen) {
		out = append(out, c.(map[string]any))
	}
	return out
}

func TestParseAIWhatsAppFlowDraft_CleansMessyOutput(t *testing.T) {
	t.Parallel()
	raw := "Sure!\n```json\n" + `{
	  "name": "Demo booking",
	  "category": "appointment_booking",
	  "screens": [
	    {
	      "id": "screen 1",
	      "title": "Your details",
	      "layout": {"type": "SingleColumnLayout", "children": [
	        {"type": "TextHeading", "text": "Book a demo"},
	        {"type": "Image", "src": "https://example.com/x.png"},
	        {"type": "TextInput", "name": "full name", "label": "Full name", "input-type": "fullname", "required": true},
	        {"type": "TextInput", "name": "full name", "label": "Nickname"},
	        {"type": "TextInput", "name": "email", "label": ""},
	        {"type": "Footer", "label": "Next", "on-click-action": {"name": "complete", "payload": {}}}
	      ]}
	    },
	    {
	      "id": "EMPTY",
	      "title": "Nothing usable",
	      "layout": {"children": [{"type": "Footer", "label": "Go"}]}
	    },
	    {
	      "id": "SUCCESS",
	      "title": "Preferences",
	      "layout": {"children": [
	        {"type": "RadioButtonsGroup", "name": "team_size", "label": "Team size",
	         "data-source": [{"id": "1-10", "title": "1-10"}, "11-50", {"title": ""}]},
	        {"type": "PhotoPicker", "name": "logo", "label": "Logo", "max-uploaded-photos": 5},
	        {"type": "DocumentPicker", "name": "brief", "label": "Brief"}
	      ]}
	    }
	  ]
	}` + "\n```"

	draft, err := parseAIWhatsAppFlowDraft(raw)
	require.NoError(t, err)
	assert.Equal(t, "Demo booking", draft.Name)
	assert.Equal(t, "APPOINTMENT_BOOKING", draft.Category)
	require.Len(t, draft.Screens, 2, "screen without inputs or text is dropped")

	first := draft.Screens[0].(map[string]any)
	assert.Equal(t, "SCREEN_B", first["id"], "digits become letters, Meta rejects them")
	children := aiScreenChildren(t, first)
	require.Len(t, children, 4, "heading, two inputs, footer; Image and label-less input dropped")
	assert.Equal(t, "full_name", children[1]["name"])
	assert.Equal(t, "text", children[1]["input-type"], "unknown input-type falls back to text")
	assert.Equal(t, true, children[1]["required"])
	assert.Equal(t, "full_name_b", children[2]["name"], "duplicate field names are made unique")
	for _, c := range children {
		assert.Regexp(t, `^id_[a-z]{12}$`, c["id"])
	}

	footer := children[3]
	assert.Equal(t, "Next", footer["label"])
	action := footer["on-click-action"].(map[string]any)
	assert.Equal(t, "navigate", action["name"], "complete is only allowed on the last screen")
	assert.Equal(t, "SUCCESS_SCREEN", action["next"].(map[string]any)["name"])

	last := draft.Screens[1].(map[string]any)
	assert.Equal(t, "SUCCESS_SCREEN", last["id"])
	lastChildren := aiScreenChildren(t, last)
	require.Len(t, lastChildren, 3, "radio, one picker, footer; second picker dropped")
	opts := lastChildren[0]["data-source"].([]any)
	require.Len(t, opts, 2)
	assert.Equal(t, "b_ba", opts[0].(map[string]any)["id"])
	assert.Equal(t, "11-50", opts[1].(map[string]any)["title"])
	assert.Equal(t, 1, lastChildren[1]["max-uploaded-photos"], "Complete Flow can only carry one file")
	lastAction := lastChildren[2]["on-click-action"].(map[string]any)
	assert.Equal(t, "complete", lastAction["name"])
	assert.Equal(t, "Submit", lastChildren[2]["label"])

	require.NoError(t, validateFlowStructure(draft.Screens))
	sanitized := sanitizeScreensForMeta(draft.Screens)
	require.Len(t, sanitized, 2)
	assert.Equal(t, true, sanitized[1].(map[string]any)["terminal"])
}

func TestParseAIWhatsAppFlowDraft_KeepsBranchingNavigate(t *testing.T) {
	t.Parallel()
	raw := `{"name":"Support","category":"nonsense","screens":[
	  {"id":"START","title":"Start","layout":{"children":[
	    {"type":"TextBody","text":"Hi"},
	    {"type":"Footer","label":"Skip ahead","on-click-action":{"name":"navigate","next":{"type":"screen","name":"DONE"}}}]}},
	  {"id":"MIDDLE","title":"Middle","layout":{"children":[{"type":"TextArea","name":"issue","label":"Issue"}]}},
	  {"id":"DONE","title":"Done","layout":{"children":[{"type":"OptIn","name":"ok","label":"Contact me"}]}}
	]}`
	draft, err := parseAIWhatsAppFlowDraft(raw)
	require.NoError(t, err)
	assert.Equal(t, "OTHER", draft.Category)
	require.Len(t, draft.Screens, 3)

	start := aiScreenChildren(t, draft.Screens[0])
	next := start[1]["on-click-action"].(map[string]any)["next"].(map[string]any)["name"]
	assert.Equal(t, "DONE", next, "a valid forward target is kept")

	middle := aiScreenChildren(t, draft.Screens[1])
	require.Len(t, middle, 2, "missing footer is added")
	assert.Equal(t, "DONE", middle[1]["on-click-action"].(map[string]any)["next"].(map[string]any)["name"])
}

func TestParseAIWhatsAppFlowDraft_Rejects(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"no json":       "I cannot help with that.",
		"no screens":    `{"name":"x","screens":[]}`,
		"nothing valid": `{"screens":[{"id":"A","layout":{"children":[{"type":"Image","src":"x"}]}}]}`,
	} {
		_, err := parseAIWhatsAppFlowDraft(raw)
		assert.Error(t, err, name)
	}
}

func TestParseAIWhatsAppFlowDraft_AcceptsFlowJSONWrapper(t *testing.T) {
	t.Parallel()
	raw := `{"name":"Wrapped","flow_json":{"version":"6.1","screens":[
	  {"id":"ONLY","title":"Only","layout":{"children":[{"type":"TextInput","name":"email","label":"Email","input-type":"email"}]}}]}}`
	draft, err := parseAIWhatsAppFlowDraft(raw)
	require.NoError(t, err)
	require.Len(t, draft.Screens, 1)
	children := aiScreenChildren(t, draft.Screens[0])
	assert.Equal(t, "email", children[0]["input-type"])
	assert.Equal(t, "complete", children[1]["on-click-action"].(map[string]any)["name"])
}
