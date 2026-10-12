package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GeminiTTSModels are tried in order for generateContent AUDIO modality.
var GeminiTTSModels = []string{
	"gemini-2.5-flash-preview-tts",
	"gemini-3.1-flash-tts-preview",
	"gemini-2.5-pro-preview-tts",
}

// GeminiTTSSampleRate is the native PCM rate returned by Gemini TTS.
const GeminiTTSSampleRate = 24000

// NormalizeGeminiVoice maps a voice id to a known Gemini prebuilt voice name.
func NormalizeGeminiVoice(voice string) string {
	v := strings.TrimSpace(voice)
	voices := []string{
		"Achernar", "Achird", "Algenib", "Algieba", "Alnilam", "Aoede", "Autonoe",
		"Callirrhoe", "Charon", "Despina", "Enceladus", "Erinome", "Fenrir",
		"Gacrux", "Iapetus", "Kore", "Laomedeia", "Leda", "Orus", "Puck",
		"Pulcherrima", "Rasalgethi", "Sadachbia", "Sadaltager", "Schedar",
		"Sulafat", "Umbriel", "Vindemiatrix", "Zephyr", "Zubenelgenubi",
	}
	for _, name := range voices {
		if strings.EqualFold(name, v) {
			return name
		}
	}
	return "Puck"
}

// SynthesizeGeminiWAV calls Gemini generateContent with AUDIO modality and
// returns a mono PCM WAV suitable for browser <audio> playback.
func SynthesizeGeminiWAV(ctx context.Context, apiKey, text, voice string, client *http.Client) ([]byte, error) {
	apiKey = strings.TrimSpace(apiKey)
	text = strings.TrimSpace(text)
	if apiKey == "" {
		return nil, fmt.Errorf("gemini TTS: API key required")
	}
	if text == "" {
		return nil, fmt.Errorf("gemini TTS: text required")
	}
	if len(text) > 2000 {
		text = text[:2000]
	}
	voice = NormalizeGeminiVoice(voice)
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
	}

	body := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": text}}},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{
					"prebuiltVoiceConfig": map[string]string{
						"voiceName": voice,
					},
				},
			},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, model := range GeminiTTSModels {
		url := fmt.Sprintf(
			"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
			model, apiKey,
		)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("%s: %s", model, truncate(string(raw), 160))
			continue
		}
		var parsed struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						InlineData *struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			lastErr = err
			continue
		}
		var decodeErr error
		for _, c := range parsed.Candidates {
			for _, p := range c.Content.Parts {
				if p.InlineData == nil || p.InlineData.Data == "" {
					continue
				}
				decoded, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
				if err != nil {
					decodeErr = err
					continue
				}
				pcm := geminiAudioToPCM24(decoded, p.InlineData.MimeType)
				if len(pcm) == 0 {
					continue
				}
				return pcm16ToWAV(pcm, GeminiTTSSampleRate), nil
			}
		}
		if decodeErr != nil {
			lastErr = fmt.Errorf("%s: decode audio: %w", model, decodeErr)
		} else {
			lastErr = fmt.Errorf("%s: no audio in response", model)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("gemini TTS failed")
	}
	return nil, lastErr
}

func geminiAudioToPCM24(raw []byte, mime string) []byte {
	mime = strings.ToLower(mime)
	// Raw PCM / L16
	if strings.Contains(mime, "pcm") || strings.Contains(mime, "l16") || mime == "" {
		return raw
	}
	// WAV / RIFF container
	if len(raw) > 44 && string(raw[:4]) == "RIFF" {
		// Find "data" chunk
		i := 12
		for i+8 <= len(raw) {
			chunkID := string(raw[i : i+4])
			chunkSize := int(binary.LittleEndian.Uint32(raw[i+4 : i+8]))
			i += 8
			if chunkID == "data" {
				end := i + chunkSize
				if end > len(raw) {
					end = len(raw)
				}
				return raw[i:end]
			}
			i += chunkSize
			if chunkSize%2 == 1 {
				i++
			}
		}
	}
	return raw
}

func pcm16ToWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = GeminiTTSSampleRate
	}
	dataLen := len(pcm)
	buf := make([]byte, 44+dataLen)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataLen))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(buf[20:], 1)  // audio format PCM
	binary.LittleEndian.PutUint16(buf[22:], 1)  // mono
	binary.LittleEndian.PutUint32(buf[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(sampleRate*2)) // byte rate
	binary.LittleEndian.PutUint16(buf[32:], 2)                    // block align
	binary.LittleEndian.PutUint16(buf[34:], 16)                   // bits per sample
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataLen))
	copy(buf[44:], pcm)
	return buf
}
