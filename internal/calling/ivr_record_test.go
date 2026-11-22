package calling

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVoicemailBeepEnabled(t *testing.T) {
	t.Parallel()
	assert.True(t, voicemailBeepEnabled(nil))
	assert.True(t, voicemailBeepEnabled(map[string]any{}))
	assert.True(t, voicemailBeepEnabled(map[string]any{"beep": true}))
	assert.False(t, voicemailBeepEnabled(map[string]any{"beep": false}))
	assert.False(t, voicemailBeepEnabled(map[string]any{"beep": "off"}))
}

func TestVoicemailRecordSettings_ActiveFlowAliases(t *testing.T) {
	t.Parallel()
	// Busy Agents Voicemail stores these keys, not max_seconds / terminator.
	secs, term := voicemailRecordSettings(map[string]any{
		"max_duration_seconds": float64(60),
		"finish_on_key":        "#",
		"greeting_text":        "Please leave your name and a brief message after the beep.",
	})
	assert.Equal(t, 60, secs)
	assert.Equal(t, "#", term)
	assert.True(t, voicemailBeepEnabled(map[string]any{
		"greeting_text": "Please leave your name and a brief message after the beep.",
	}))
}

func TestRecordedSeconds(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, recordedSeconds(0))
	assert.Equal(t, 1, recordedSeconds(1))
	assert.Equal(t, 4, recordedSeconds(200))
}

func TestBundledBeepHasOpusPackets(t *testing.T) {
	t.Parallel()
	f, err := os.Open("defaultaudio/beep.opus")
	require.NoError(t, err)
	defer f.Close()
	packets, err := readOpusPackets(f)
	require.NoError(t, err)
	require.NotEmpty(t, packets)
	for _, p := range packets {
		assert.NotEmpty(t, p)
	}
}
