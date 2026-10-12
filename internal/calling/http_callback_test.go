package calling

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInterpolateTemplate_CallerVars(t *testing.T) {
	got := interpolateTemplate(
		`{"phone":"{{caller_phone}}","name":"{{caller_name}}","call":"{{call_id}}"}`,
		map[string]string{
			"caller_phone": "15550001234",
			"caller_name":  "Test Customer",
			"call_id":      "wacid.123",
		},
	)
	assert.Equal(t, `{"phone":"15550001234","name":"Test Customer","call":"wacid.123"}`, got)
}

func TestWithCallerName(t *testing.T) {
	vars := withCallerName(map[string]string{"caller_phone": "+1555"}, &CallSession{CallerName: "Ada"})
	assert.Equal(t, "Ada", vars["caller_name"])
	assert.Equal(t, "+1555", vars["caller_phone"])

	empty := withCallerName(nil, nil)
	assert.Empty(t, empty["caller_name"])
}

func TestResolvePromptAudio_NoTTSFallsBackToAudioFile(t *testing.T) {
	m := &Manager{}
	node := &IVRNode{Config: map[string]any{
		"audio_file":    "prebaked.ogg",
		"greeting_text": "Hi {{caller_name}}",
	}}
	got := m.resolvePromptAudio(node, &IVRContext{Variables: map[string]string{"caller_name": "Ada"}})
	assert.Equal(t, "prebaked.ogg", got)
}
