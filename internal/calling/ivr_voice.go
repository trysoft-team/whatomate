package calling

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/tts"
)

// synthesizeTTS generates OGG/Opus for text using org voice settings merged with node config.
func (m *Manager) synthesizeTTS(orgID uuid.UUID, text string, nodeConfig map[string]any) (string, error) {
	if m.tts == nil {
		return "", errTTSNotConfigured
	}
	opts := m.resolveSynthOpts(orgID, nodeConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return m.tts.Generate(ctx, text, opts)
}

var errTTSNotConfigured = errString("TTS not configured")

type errString string

func (e errString) Error() string { return string(e) }

// resolveSynthOpts loads org chatbot voice defaults and overlays node config.
func (m *Manager) resolveSynthOpts(orgID uuid.UUID, nodeConfig map[string]any) tts.SynthOpts {
	opts := tts.SynthOpts{Provider: tts.ProviderPiper}

	var settings models.ChatbotSettings
	if m.db != nil {
		_ = m.db.Where("organization_id = ? AND whats_app_account = ?", orgID, "").First(&settings).Error
	}

	if settings.AI.VoiceProvider != "" {
		opts.Provider = settings.AI.VoiceProvider
	}
	opts.Voice = settings.AI.OpenAITTSVoice
	if settings.AI.ElevenLabsVoiceID != "" && (opts.Provider == tts.ProviderElevenLabs || opts.Provider == "") {
		opts.Voice = settings.AI.ElevenLabsVoiceID
	}

	apiKey := settings.AI.APIKey
	elKey := settings.AI.ElevenLabsAPIKey
	if m.encryptionKey != "" {
		crypto.DecryptFields(m.encryptionKey, &apiKey, &elKey)
	}
	opts.OpenAIKey = apiKey
	opts.ElevenLabsKey = elKey
	if opts.OpenAIKey == "" && m.aiConfig != nil {
		opts.OpenAIKey = m.aiConfig.OpenAIKey
	}

	if nodeConfig != nil {
		if p, ok := nodeConfig["voice_provider"].(string); ok && strings.TrimSpace(p) != "" {
			opts.Provider = strings.TrimSpace(p)
		}
		if v, ok := nodeConfig["openai_voice"].(string); ok && v != "" {
			opts.Voice = v
			if opts.Provider == "" || opts.Provider == tts.ProviderPiper {
				opts.Provider = tts.ProviderOpenAI
			}
		}
		if v, ok := nodeConfig["voice_id"].(string); ok && v != "" {
			opts.Voice = v
			if opts.Provider == "" || opts.Provider == tts.ProviderPiper {
				opts.Provider = tts.ProviderElevenLabs
			}
		}
		// Explicit voice_id with elevenlabs provider
		if strings.EqualFold(opts.Provider, tts.ProviderElevenLabs) {
			if v, ok := nodeConfig["voice_id"].(string); ok && v != "" {
				opts.Voice = v
			} else if settings.AI.ElevenLabsVoiceID != "" {
				opts.Voice = settings.AI.ElevenLabsVoiceID
			}
		}
		if strings.EqualFold(opts.Provider, tts.ProviderOpenAI) {
			if v, ok := nodeConfig["openai_voice"].(string); ok && v != "" {
				opts.Voice = v
			} else if settings.AI.OpenAITTSVoice != "" {
				opts.Voice = settings.AI.OpenAITTSVoice
			} else if opts.Voice == "" {
				opts.Voice = "alloy"
			}
		}
	}

	return opts
}

// loadOrgAISettings returns decrypted org chatbot AI settings for IVR LLM/TTS.
func (m *Manager) loadOrgAISettings(orgID uuid.UUID) models.ChatbotSettings {
	var settings models.ChatbotSettings
	if m.db != nil {
		_ = m.db.Where("organization_id = ? AND whats_app_account = ?", orgID, "").First(&settings).Error
	}
	if settings.AI.APIKey != "" && m.encryptionKey != "" {
		crypto.DecryptFields(m.encryptionKey, &settings.AI.APIKey)
	}
	if settings.AI.ElevenLabsAPIKey != "" && m.encryptionKey != "" {
		crypto.DecryptFields(m.encryptionKey, &settings.AI.ElevenLabsAPIKey)
	}
	return settings
}
