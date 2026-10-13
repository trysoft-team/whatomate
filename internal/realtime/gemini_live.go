package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
)

// LiveEndpoint is the Gemini Live (BidiGenerateContent) websocket.
const LiveEndpoint = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

// Live audio formats: 16 kHz PCM16 in, 24 kHz PCM16 out.
const (
	LiveInputRate  = 16000
	LiveOutputRate = 24000
)

// LiveModels are tried in order. 3.1 Flash Live comes first: the
// native-audio-latest alias often closes mid-call with 1007 CONTENT_TYPE_AUDIO.
var LiveModels = []string{
	"gemini-3.1-flash-live-preview",
	"gemini-2.5-flash-native-audio-preview-12-2025",
	"gemini-2.5-flash-native-audio-preview-09-2025",
	"gemini-2.5-flash-native-audio-latest",
}

const liveSetupTimeout = 15 * time.Second

// FunctionDeclaration describes a tool the model may call.
type FunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// LiveConfig is the session setup sent as the first websocket message.
type LiveConfig struct {
	Model             string
	Voice             string
	SystemInstruction string
	Tools             []FunctionDeclaration
	Transcription     bool
}

// SetupError means the model rejected or never acknowledged the setup, so
// the caller may try another model.
type SetupError struct{ Msg string }

func (e *SetupError) Error() string { return "gemini live setup: " + e.Msg }

// LiveSession is one open Gemini Live websocket. Send methods are safe for
// concurrent use; Recv must be called from a single goroutine.
type LiveSession struct {
	conn *websocket.Conn
	wmu  sync.Mutex
}

// ServerMessage is one BidiGenerateContentServerMessage.
type ServerMessage struct {
	SetupComplete *json.RawMessage `json:"setupComplete"`
	ServerContent *ServerContent   `json:"serverContent"`
	ToolCall      *ToolCall        `json:"toolCall"`
	GoAway        *json.RawMessage `json:"goAway"`
	Error         *json.RawMessage `json:"error"`
}

type ServerContent struct {
	ModelTurn           *Content       `json:"modelTurn"`
	TurnComplete        bool           `json:"turnComplete"`
	GenerationComplete  bool           `json:"generationComplete"`
	Interrupted         bool           `json:"interrupted"`
	InputTranscription  *Transcription `json:"inputTranscription"`
	OutputTranscription *Transcription `json:"outputTranscription"`
}

type Content struct {
	Parts []Part `json:"parts"`
}

type Part struct {
	Text       string `json:"text"`
	InlineData *Blob  `json:"inlineData"`
}

// Blob carries audio; encoding/json base64-decodes Data.
type Blob struct {
	MimeType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

type Transcription struct {
	Text     string `json:"text"`
	Finished bool   `json:"finished"`
}

type ToolCall struct {
	FunctionCalls []FunctionCall `json:"functionCalls"`
}

type FunctionCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// FunctionResponse answers one FunctionCall.
type FunctionResponse struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// DialLive opens a session and completes setup. endpoint is normally LiveEndpoint.
func DialLive(ctx context.Context, endpoint, apiKey string, cfg LiveConfig) (*LiveSession, error) {
	header := http.Header{}
	header.Set("x-goog-api-key", apiKey)
	dialer := websocket.Dialer{HandshakeTimeout: liveSetupTimeout}
	conn, resp, err := dialer.DialContext(ctx, endpoint, header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return nil, &SetupError{Msg: fmt.Sprintf("handshake status %d", resp.StatusCode)}
		}
		return nil, fmt.Errorf("gemini live dial: %w", err)
	}
	s := &LiveSession{conn: conn}
	if err := s.setup(cfg); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

func (s *LiveSession) setup(cfg LiveConfig) error {
	if err := s.send(map[string]any{"setup": setupBody(cfg)}); err != nil {
		return fmt.Errorf("gemini live send setup: %w", err)
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(liveSetupTimeout))
	msg, err := s.Recv()
	if err != nil {
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			return &SetupError{Msg: truncate(ce.Error(), 240)}
		}
		return &SetupError{Msg: truncate(err.Error(), 240)}
	}
	_ = s.conn.SetReadDeadline(time.Time{})
	if msg.Error != nil {
		return &SetupError{Msg: truncate(string(*msg.Error), 240)}
	}
	if msg.SetupComplete == nil {
		return &SetupError{Msg: "no setupComplete"}
	}
	return nil
}

func setupBody(cfg LiveConfig) map[string]any {
	decls := cfg.Tools
	if decls == nil {
		decls = []FunctionDeclaration{}
	}
	body := map[string]any{
		"model": "models/" + cfg.Model,
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{
					"prebuiltVoiceConfig": map[string]any{"voiceName": cfg.Voice},
				},
			},
		},
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": cfg.SystemInstruction}},
		},
		"tools": []map[string]any{{"functionDeclarations": decls}},
		// HIGH start so the caller can interrupt mid-sentence; LOW end so line
		// hiss does not hold the caller's turn open.
		"realtimeInputConfig": map[string]any{
			"automaticActivityDetection": map[string]any{
				"disabled":                 false,
				"startOfSpeechSensitivity": "START_SENSITIVITY_HIGH",
				"endOfSpeechSensitivity":   "END_SENSITIVITY_LOW",
				"prefixPaddingMs":          20,
				"silenceDurationMs":        400,
			},
			"activityHandling": "START_OF_ACTIVITY_INTERRUPTS",
		},
	}
	if cfg.Transcription {
		body["inputAudioTranscription"] = map[string]any{}
		body["outputAudioTranscription"] = map[string]any{}
	}
	return body
}

// SendText sends text as realtime input (used to make the model speak first).
func (s *LiveSession) SendText(text string) error {
	return s.send(map[string]any{"realtimeInput": map[string]any{"text": text}})
}

// SendAudio streams 16 kHz mono PCM16 caller audio.
func (s *LiveSession) SendAudio(pcm16 []int16) error {
	return s.send(map[string]any{"realtimeInput": map[string]any{
		"audio": map[string]any{
			"data":     base64.StdEncoding.EncodeToString(PCM16ToBytes(pcm16)),
			"mimeType": fmt.Sprintf("audio/pcm;rate=%d", LiveInputRate),
		},
	}})
}

// SendToolResponses answers the model's function calls.
func (s *LiveSession) SendToolResponses(rs []FunctionResponse) error {
	return s.send(map[string]any{"toolResponse": map[string]any{"functionResponses": rs}})
}

// Recv blocks for the next server message.
func (s *LiveSession) Recv() (*ServerMessage, error) {
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var msg ServerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("gemini live decode: %w", err)
	}
	return &msg, nil
}

// Close ends the session; a pending Recv returns an error.
func (s *LiveSession) Close() error {
	s.wmu.Lock()
	_ = s.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	s.wmu.Unlock()
	return s.conn.Close()
}

func (s *LiveSession) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
