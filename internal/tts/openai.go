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

// OpenAISynth synthesizes speech via OpenAI /v1/audio/speech.
type OpenAISynth struct {
	AudioDir     string
	FFmpegBinary string
	HTTPClient   *http.Client
	Endpoint     string // override for tests; default OpenAI speech URL
}

func (o *OpenAISynth) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (o *OpenAISynth) endpoint() string {
	if o.Endpoint != "" {
		return o.Endpoint
	}
	return "https://api.openai.com/v1/audio/speech"
}

// Generate downloads WAV speech and converts to OGG/Opus via ffmpeg.
func (o *OpenAISynth) Generate(ctx context.Context, text string, opts SynthOpts) (string, error) {
	if strings.TrimSpace(opts.OpenAIKey) == "" {
		return "", fmt.Errorf("openai TTS: API key required")
	}
	voice := opts.Voice
	if voice == "" {
		voice = "alloy"
	}
	model := opts.OpenAIModel
	if model == "" {
		model = "gpt-4o-mini-tts"
	}
	ns := "openai|" + model + "|" + voice
	if opts.CacheNamespace != "" {
		ns = opts.CacheNamespace
	}
	filename, outPath := cachePath(o.AudioDir, ns, text)
	if fileExists(outPath) {
		return filename, nil
	}
	if err := ensureAudioDir(o.AudioDir); err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]any{
		"model":           model,
		"voice":           voice,
		"input":           text,
		"response_format": "wav",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+opts.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai TTS %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	tmpWav := filepath.Join(o.AudioDir, filename+".tmp.wav")
	tmpOgg := filepath.Join(o.AudioDir, filename+".tmp.ogg")
	defer func() { _ = os.Remove(tmpWav); _ = os.Remove(tmpOgg) }()

	if err := os.WriteFile(tmpWav, raw, 0644); err != nil {
		return "", err
	}
	if err := ffmpegToOgg(ctx, o.FFmpegBinary, tmpWav, tmpOgg); err != nil {
		return "", err
	}
	if err := os.Rename(tmpOgg, outPath); err != nil {
		return "", err
	}
	return filename, nil
}
