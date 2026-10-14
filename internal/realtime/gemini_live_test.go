package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fasthttp/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLive runs handler for each websocket connection and returns the ws:// URL.
func fakeLive(t *testing.T, handler func(*websocket.Conn, *http.Request)) string {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		handler(c, r)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func readJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_, data, err := c.ReadMessage()
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func TestDialLive_SetupAndConversation(t *testing.T) {
	audio := PCM16ToBytes([]int16{1, 2, 3})
	type seen struct {
		key    string
		setup  map[string]any
		text   map[string]any
		audio  map[string]any
		toolRs map[string]any
	}
	got := make(chan seen, 1)

	url := fakeLive(t, func(c *websocket.Conn, r *http.Request) {
		var s seen
		s.key = r.Header.Get("x-goog-api-key")
		s.setup = readJSON(t, c)
		// Live answers setup with a binary frame.
		_ = c.WriteMessage(websocket.BinaryMessage, []byte(`{"setupComplete":{}}`))
		s.text = readJSON(t, c)
		s.audio = readJSON(t, c)
		_ = c.WriteJSON(map[string]any{"serverContent": map[string]any{
			"modelTurn":           map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": "audio/pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(audio)}}}},
			"outputTranscription": map[string]any{"text": "Hi"},
			"turnComplete":        true,
		}})
		_ = c.WriteJSON(map[string]any{"toolCall": map[string]any{"functionCalls": []any{
			map[string]any{"id": "c1", "name": "end_call", "args": map[string]any{"reason": "done"}},
		}}})
		s.toolRs = readJSON(t, c)
		got <- s
	})

	sess, err := DialLive(context.Background(), url, "test-key", LiveConfig{
		Model: "m1", Voice: "Puck", SystemInstruction: "be brief", Transcription: true,
		Tools: []FunctionDeclaration{{Name: "end_call", Description: "hang up", Parameters: map[string]any{"type": "object"}}},
	})
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	require.NoError(t, sess.SendText("hello"))
	require.NoError(t, sess.SendAudio([]int16{7, 8}))

	msg, err := sess.Recv()
	require.NoError(t, err)
	require.NotNil(t, msg.ServerContent)
	assert.True(t, msg.ServerContent.TurnComplete)
	assert.Equal(t, "Hi", msg.ServerContent.OutputTranscription.Text)
	assert.Equal(t, audio, msg.ServerContent.ModelTurn.Parts[0].InlineData.Data)

	msg, err = sess.Recv()
	require.NoError(t, err)
	require.NotNil(t, msg.ToolCall)
	fc := msg.ToolCall.FunctionCalls[0]
	assert.Equal(t, "c1", fc.ID)
	assert.Equal(t, "done", fc.Args["reason"])
	require.NoError(t, sess.SendToolResponses([]FunctionResponse{{ID: fc.ID, Name: fc.Name, Response: map[string]any{"result": "ending"}}}))

	s := <-got
	assert.Equal(t, "test-key", s.key)
	setup := s.setup["setup"].(map[string]any)
	assert.Equal(t, "models/m1", setup["model"])
	assert.Contains(t, setup, "inputAudioTranscription")
	voice := setup["generationConfig"].(map[string]any)["speechConfig"].(map[string]any)["voiceConfig"].(map[string]any)["prebuiltVoiceConfig"].(map[string]any)
	assert.Equal(t, "Puck", voice["voiceName"])
	decls := setup["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	assert.Equal(t, "end_call", decls[0].(map[string]any)["name"])

	assert.Equal(t, "hello", s.text["realtimeInput"].(map[string]any)["text"])
	a := s.audio["realtimeInput"].(map[string]any)["audio"].(map[string]any)
	assert.Equal(t, "audio/pcm;rate=16000", a["mimeType"])
	raw, _ := base64.StdEncoding.DecodeString(a["data"].(string))
	assert.Equal(t, []int16{7, 8}, BytesToPCM16(raw))

	rs := s.toolRs["toolResponse"].(map[string]any)["functionResponses"].([]any)[0].(map[string]any)
	assert.Equal(t, "c1", rs["id"])
	assert.Equal(t, "ending", rs["response"].(map[string]any)["result"])
}

func TestDialLive_SetupFailuresAreSetupErrors(t *testing.T) {
	cases := map[string]func(*websocket.Conn){
		"error message": func(c *websocket.Conn) {
			_ = c.WriteJSON(map[string]any{"error": map[string]any{"message": "model not found"}})
		},
		"policy close": func(c *websocket.Conn) {
			_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "bad model"))
		},
		"unexpected first message": func(c *websocket.Conn) {
			_ = c.WriteJSON(map[string]any{"serverContent": map[string]any{}})
		},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			url := fakeLive(t, func(c *websocket.Conn, _ *http.Request) {
				_, _, _ = c.ReadMessage()
				reply(c)
			})
			_, err := DialLive(context.Background(), url, "k", LiveConfig{Model: "m"})
			var se *SetupError
			assert.True(t, errors.As(err, &se), "got %v", err)
		})
	}
}
