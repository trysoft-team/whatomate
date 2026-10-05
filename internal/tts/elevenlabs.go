package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ElevenLabsSynth synthesizes speech via ElevenLabs text-to-speech API.
type ElevenLabsSynth struct {
	AudioDir     string
	FFmpegBinary string
	HTTPClient   *http.Client
	EndpointBase string // override for tests; default https://api.elevenlabs.io
}

func (e *ElevenLabsSynth) client() *http.Client {
	if e.HTTPClient != nil {
		return e.HTTPClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (e *ElevenLabsSynth) base() string {
	if e.EndpointBase != "" {
		return strings.TrimRight(e.EndpointBase, "/")
	}
	return "https://api.elevenlabs.io"
}

// Generate downloads MP3 speech and converts to OGG/Opus via ffmpeg.
func (e *ElevenLabsSynth) Generate(ctx context.Context, text string, opts SynthOpts) (string, error) {
	if strings.TrimSpace(opts.ElevenLabsKey) == "" {
		return "", fmt.Errorf("elevenlabs TTS: API key required")
	}
	voiceID := strings.TrimSpace(opts.Voice)
	if voiceID == "" {
		return "", fmt.Errorf("elevenlabs TTS: voice_id required")
	}
	ns := "elevenlabs|" + voiceID
	if opts.CacheNamespace != "" {
		ns = opts.CacheNamespace
	}
	filename, outPath := cachePath(e.AudioDir, ns, text)
	if fileExists(outPath) {
		return filename, nil
	}
	if err := ensureAudioDir(e.AudioDir); err != nil {
		return "", err
	}

	body, _ := json.Marshal(map[string]any{
		"text":     text,
		"model_id": "eleven_multilingual_v2",
	})
	url := fmt.Sprintf("%s/v1/text-to-speech/%s?output_format=mp3_44100_128", e.base(), voiceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("xi-api-key", opts.ElevenLabsKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := e.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("elevenlabs TTS %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	tmpMP3 := filepath.Join(e.AudioDir, filename+".tmp.mp3")
	tmpOgg := filepath.Join(e.AudioDir, filename+".tmp.ogg")
	defer func() { _ = os.Remove(tmpMP3); _ = os.Remove(tmpOgg) }()

	if err := os.WriteFile(tmpMP3, raw, 0644); err != nil {
		return "", err
	}
	if err := ffmpegToOgg(ctx, e.FFmpegBinary, tmpMP3, tmpOgg); err != nil {
		return "", err
	}
	if err := os.Rename(tmpOgg, outPath); err != nil {
		return "", err
	}
	return filename, nil
}
