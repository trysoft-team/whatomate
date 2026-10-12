package tts

import (
	"context"
	"fmt"
	"strings"
)

// Router selects among Piper, OpenAI, and ElevenLabs synthesizers.
type Router struct {
	Piper           *PiperTTS
	OpenAI          *OpenAISynth
	ElevenLabs      *ElevenLabsSynth
	DefaultProvider string // piper | openai | elevenlabs
}

// Generate routes to the requested (or default) provider.
func (r *Router) Generate(ctx context.Context, text string, opts SynthOpts) (string, error) {
	if r == nil {
		return "", fmt.Errorf("tts router not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("empty TTS text")
	}
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(r.DefaultProvider))
	}
	if provider == "" {
		provider = ProviderPiper
	}

	switch provider {
	case ProviderOpenAI:
		if r.OpenAI == nil {
			return "", fmt.Errorf("openai TTS not configured")
		}
		return r.OpenAI.Generate(ctx, text, opts)
	case ProviderElevenLabs:
		if r.ElevenLabs == nil {
			return "", fmt.Errorf("elevenlabs TTS not configured")
		}
		return r.ElevenLabs.Generate(ctx, text, opts)
	default:
		if r.Piper == nil {
			return "", fmt.Errorf("piper TTS not configured")
		}
		return r.Piper.Generate(text)
	}
}

// Ensure Router implements Synthesizer.
var _ Synthesizer = (*Router)(nil)
