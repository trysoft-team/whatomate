package tts_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/tts"
)

// LIVE_PIPER_TEST=1 go test ./internal/tts -run TestLivePiperGenerate -v -count=1
func TestLivePiperGenerate(t *testing.T) {
	if os.Getenv("LIVE_PIPER_TEST") != "1" {
		t.Skip("set LIVE_PIPER_TEST=1")
	}
	home, _ := os.UserHomeDir()
	bin := filepath.Join(home, ".local/bin/piper")
	model := filepath.Join(home, ".local/share/piper/models/en_US-lessac-medium.onnx")
	opusenc := "/opt/homebrew/bin/opusenc"
	require.FileExists(t, bin)
	require.FileExists(t, model)
	require.FileExists(t, opusenc)

	dir := t.TempDir()
	p := &tts.PiperTTS{
		BinaryPath:    bin,
		ModelPath:     model,
		OpusencBinary: opusenc,
		AudioDir:      dir,
	}
	name, err := p.Generate("Welcome to the salon. Soft glam is twenty dollars.")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, name))
	info, err := os.Stat(filepath.Join(dir, name))
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(1000))
	t.Logf("generated %s (%d bytes)", name, info.Size())
}
