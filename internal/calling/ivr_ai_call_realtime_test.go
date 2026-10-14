package calling

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/realtime"
)

// fakeGeminiLive accepts setup and the kickoff text, then runs script.
func fakeGeminiLive(t *testing.T, script func(c *websocket.Conn)) {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _, _ = c.ReadMessage() // setup
		_ = c.WriteJSON(map[string]any{"setupComplete": map[string]any{}})
		_, _, _ = c.ReadMessage() // kickoff text
		script(c)
	}))
	t.Cleanup(srv.Close)
	prev := geminiLiveEndpoint
	geminiLiveEndpoint = "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Cleanup(func() { geminiLiveEndpoint = prev })
}

func liveAudio(ms int) map[string]any {
	pcm := make([]int16, realtime.LiveOutputRate*ms/1000)
	for i := range pcm {
		pcm[i] = int16(1000 * (i % 2))
	}
	return map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{
		map[string]any{"inlineData": map[string]any{"mimeType": "audio/pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(realtime.PCM16ToBytes(pcm))}},
	}}}}
}

func toolCall(name string, args map[string]any) map[string]any {
	return map[string]any{"toolCall": map[string]any{"functionCalls": []any{
		map[string]any{"id": "call-1", "name": name, "args": args},
	}}}
}

// readToolResponse skips caller audio until the tool response arrives.
func readToolResponse(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	for {
		_, data, err := c.ReadMessage()
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(data, &m))
		if tr, ok := m["toolResponse"].(map[string]any); ok {
			return tr["functionResponses"].([]any)[0].(map[string]any)["response"].(map[string]any)
		}
	}
}

func newRealtimeTestCall(t *testing.T, tools []aiCallToolDef) (*realtimeCall, *AudioPlayer) {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 1}, "audio", "test")
	require.NoError(t, err)
	player := NewAudioPlayer(track)
	session := &CallSession{ID: "wacid.test", BridgeStarted: make(chan struct{})}
	m := &Manager{log: testLogger(), config: &config.CallingConfig{}, sessions: map[string]*CallSession{session.ID: session}}
	return &realtimeCall{
		m: m, session: session, player: player,
		ivr:    &IVRContext{Variables: map[string]string{}},
		prompt: &aiCallPrompt{apiKey: "k", transferTeam: "team-default", tools: tools},
	}, player
}

func TestRealtimeCall_EndCallPlaysFarewellThenHangsUp(t *testing.T) {
	gotResp := make(chan map[string]any, 1)
	fakeGeminiLive(t, func(c *websocket.Conn) {
		_ = c.WriteJSON(liveAudio(300))
		_ = c.WriteJSON(toolCall("end_call", nil))
		gotResp <- readToolResponse(t, c)
		_, _, _ = c.ReadMessage() // block until the session closes
	})

	call, player := newRealtimeTestCall(t, nil)
	start := time.Now()
	ev, err := call.run(realtime.LiveConfig{Model: "m"}, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.Equal(t, "hangup", ev.Type)
	assert.Equal(t, "ending", (<-gotResp)["result"])
	assert.GreaterOrEqual(t, int(player.sequenceNumber), 14, "the 300 ms farewell (15 frames) is played before hanging up")
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestRealtimeCall_TransferAsksForAnnouncementWhenSilent(t *testing.T) {
	gotResp := make(chan map[string]any, 1)
	fakeGeminiLive(t, func(c *websocket.Conn) {
		_ = c.WriteJSON(toolCall("transfer_to_agent", map[string]any{"reason": "wants a person"}))
		gotResp <- readToolResponse(t, c)
		_ = c.WriteJSON(liveAudio(200)) // the announcement
		_, _, _ = c.ReadMessage()
	})

	call, player := newRealtimeTestCall(t, nil)
	ev, err := call.run(realtime.LiveConfig{Model: "m"}, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.Equal(t, "transfer", ev.Type)
	assert.Equal(t, "team-default", ev.TeamID)
	assert.Equal(t, "wants a person", ev.Reason)
	resp := <-gotResp
	assert.Equal(t, "transferring", resp["result"])
	assert.NotEmpty(t, resp["instruction"], "silent model is asked to announce the transfer")
	assert.GreaterOrEqual(t, int(player.sequenceNumber), 9, "announcement plays before the transfer")
}

func TestRealtimeCall_NodeToolResultGoesBackToModel(t *testing.T) {
	gotResp := make(chan map[string]any, 1)
	fakeGeminiLive(t, func(c *websocket.Conn) {
		_ = c.WriteJSON(toolCall("remember", map[string]any{"name": "Ada"}))
		gotResp <- readToolResponse(t, c)
		_ = c.WriteJSON(toolCall("end_call", nil))
		_, _, _ = c.ReadMessage()
	})

	call, _ := newRealtimeTestCall(t, []aiCallToolDef{{Name: "remember", Handler: "set_variable"}})
	ev, err := call.run(realtime.LiveConfig{Model: "m"}, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "hangup", ev.Type)
	assert.Equal(t, `{"ok":true}`, (<-gotResp)["result"])
	assert.Equal(t, "Ada", call.ivr.Variables["name"])
}

func TestRealtimeCall_DropBeforeAudioIsRetriable(t *testing.T) {
	fakeGeminiLive(t, func(c *websocket.Conn) {
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1007, "CONTENT_TYPE_AUDIO"))
	})
	call, _ := newRealtimeTestCall(t, nil)
	ev, err := call.run(realtime.LiveConfig{Model: "m"}, 30*time.Second)
	assert.Error(t, err)
	assert.Nil(t, ev)
}

func TestRealtimeCall_EndsWhenCallSessionEnds(t *testing.T) {
	fakeGeminiLive(t, func(c *websocket.Conn) { _, _, _ = c.ReadMessage() })
	call, _ := newRealtimeTestCall(t, nil)
	go func() {
		time.Sleep(200 * time.Millisecond)
		call.m.mu.Lock()
		delete(call.m.sessions, call.session.ID)
		call.m.mu.Unlock()
	}()
	ev, err := call.run(realtime.LiveConfig{Model: "m"}, 30*time.Second)
	require.NoError(t, err)
	assert.Nil(t, ev, "caller hanging up ends the AI Call as completed")
}

func TestRealtimeCall_NextFramePadsStalePartial(t *testing.T) {
	call, _ := newRealtimeTestCall(t, nil)
	call.pending = make([]int16, realtimeFrame24k+10)
	call.pendingAt = time.Now()
	assert.Len(t, call.nextFrame(), realtimeFrame24k)
	assert.Nil(t, call.nextFrame(), "a fresh partial frame waits for more audio")
	call.pendingAt = time.Now().Add(-time.Second)
	assert.Len(t, call.nextFrame(), realtimeFrame24k, "a stale partial frame is padded")
	assert.Empty(t, call.pending)
}

func TestRealtimeToolDeclarations(t *testing.T) {
	decls := realtimeToolDeclarations([]aiCallToolDef{
		{Name: "transfer_to_agent", Description: "Transfer when they ask for a person."},
		{Name: "end_call", Description: "Hang up when done."},
		{Name: "book", Description: "", Parameters: map[string]any{"type": "object"}},
	})
	require.Len(t, decls, 3)
	assert.Equal(t, "Transfer when they ask for a person.", decls[0].Description)
	assert.Equal(t, "Hang up when done. "+endCallRequiredNote, decls[1].Description)
	assert.Equal(t, "book", decls[2].Description, "missing description falls back to the name")

	defaults := realtimeToolDeclarations(nil)
	assert.Equal(t, defaultTransferToolDesc, defaults[0].Description)
	assert.Equal(t, defaultEndCallToolDesc, defaults[1].Description)
}

func TestRealtimeAICallAvailable(t *testing.T) {
	assert.True(t, realtimeAICallAvailable("google", "k"))
	assert.True(t, realtimeAICallAvailable("openrouter", "AIzaXYZ"))
	assert.False(t, realtimeAICallAvailable("openai", "sk-1"))
	assert.False(t, realtimeAICallAvailable("google", ""))
}

func TestRealtimeSystemInstructionAndKickoff(t *testing.T) {
	s := realtimeSystemInstruction("Base.", " Welcome to the salon! ")
	assert.True(t, strings.HasPrefix(s, "Base.\n\nPhone style:"))
	assert.Contains(t, s, "paraphrase naturally, keep it brief):\nWelcome to the salon!")
	assert.NotContains(t, realtimeSystemInstruction("Base.", ""), "When the call starts")

	assert.Equal(t, "The phone call is now connected. Speak first now: Hi", realtimeKickoff("Hi"))
	assert.Contains(t, realtimeKickoff(""), "Greet the caller briefly")
}
