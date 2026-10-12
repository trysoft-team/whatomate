package tts_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/tts"
)

func TestRouter_DefaultsToPiper(t *testing.T) {
	dir := t.TempDir()
	r := &tts.Router{
		Piper: &tts.PiperTTS{
			BinaryPath:    fakePiper(t),
			ModelPath:     filepath.Join(dir, "m.onnx"),
			OpusencBinary: fakeOpusenc(t),
			AudioDir:      dir,
		},
		DefaultProvider: tts.ProviderPiper,
	}
	_ = os.WriteFile(filepath.Join(dir, "m.onnx"), []byte("x"), 0644)
	name, err := r.Generate(context.Background(), "hello", tts.SynthOpts{})
	require.NoError(t, err)
	assert.Contains(t, name, "tts_")
}

func TestOpenAISynth_CachesAndCallsAPI(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "RIFFWAVDATA")
	}))
	t.Cleanup(srv.Close)

	ffmpeg := fakeBin(t, "ffmpeg", `printf 'OggS' > "${@: -1}"`)

	s := &tts.OpenAISynth{
		AudioDir:     dir,
		FFmpegBinary: ffmpeg,
		HTTPClient:   srv.Client(),
		Endpoint:     srv.URL,
	}
	opts := tts.SynthOpts{OpenAIKey: "test-key", Voice: "alloy"}
	name, err := s.Generate(context.Background(), "hi there", opts)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, name))
	_, err = s.Generate(context.Background(), "hi there", opts)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "second call should hit cache")
}

func TestElevenLabsSynth_RequiresVoice(t *testing.T) {
	s := &tts.ElevenLabsSynth{AudioDir: t.TempDir()}
	_, err := s.Generate(context.Background(), "hi", tts.SynthOpts{ElevenLabsKey: "k"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "voice_id")
}

func TestRouter_OpenAIProvider(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "wav")
	}))
	t.Cleanup(srv.Close)
	ffmpeg := fakeBin(t, "ffmpeg", `printf 'OggS' > "${@: -1}"`)
	r := &tts.Router{
		OpenAI: &tts.OpenAISynth{
			AudioDir:     dir,
			FFmpegBinary: ffmpeg,
			HTTPClient:   srv.Client(),
			Endpoint:     srv.URL,
		},
		DefaultProvider: tts.ProviderPiper,
	}
	name, err := r.Generate(context.Background(), "cloud", tts.SynthOpts{
		Provider:  tts.ProviderOpenAI,
		OpenAIKey: "k",
		Voice:     "nova",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, name)
}
