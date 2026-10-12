package tts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Provider identifiers for SynthOpts.Provider / org voice_provider.
const (
	ProviderPiper      = "piper"
	ProviderOpenAI     = "openai"
	ProviderElevenLabs = "elevenlabs"
)

// SynthOpts selects provider and voice for a single Generate call.
type SynthOpts struct {
	Provider       string // piper | openai | elevenlabs (empty = router default)
	Voice          string // OpenAI voice name or ElevenLabs voice_id
	OpenAIKey      string // required for openai provider
	ElevenLabsKey  string // required for elevenlabs provider
	OpenAIModel    string // optional; default gpt-4o-mini-tts
	CacheNamespace string // extra cache key discriminator (e.g. voice id)
}

// Synthesizer turns text into an OGG/Opus file under AudioDir and returns the filename.
type Synthesizer interface {
	Generate(ctx context.Context, text string, opts SynthOpts) (filename string, err error)
}

// cachePath returns a deterministic filename and absolute path for caching TTS output.
func cachePath(audioDir, namespace, text string) (filename, outPath string) {
	h := sha256.Sum256([]byte(namespace + "\n" + text))
	filename = "tts_" + fmt.Sprintf("%x", h[:8]) + ".ogg"
	return filename, filepath.Join(audioDir, filename)
}

func ensureAudioDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}

// ffmpegToOgg converts any ffmpeg-readable audio file to OGG/Opus 48kHz mono.
func ffmpegToOgg(ctx context.Context, ffmpegBin, inputPath, outputPath string) error {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-y",
		"-i", inputPath,
		"-ac", "1",
		"-ar", "48000",
		"-c:a", "libopus",
		"-b:a", "24k",
		"-application", "voip",
		"-frame_duration", "20",
		"-vn",
		outputPath,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg failed: %w (stderr: %s)", err, truncate(stderr.String(), 300))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
