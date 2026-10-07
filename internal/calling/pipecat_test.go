package calling

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
)

func TestMapPipecatEvent_TransferSetsTeam(t *testing.T) {
	ctx := &IVRContext{Variables: map[string]string{}}
	out := mapPipecatEvent(PipecatControlEvent{Type: "transfer", TeamID: "team-1", Reason: "asked"}, "fallback", ctx)
	assert.Equal(t, "transfer", out)
	assert.Equal(t, "team-1", ctx.Variables["prefer_team_id"])
	assert.Equal(t, "asked", ctx.Variables["transfer_reason"])
}

func TestMapPipecatEvent_TransferUsesDefaultTeam(t *testing.T) {
	ctx := &IVRContext{Variables: map[string]string{}}
	out := mapPipecatEvent(PipecatControlEvent{Type: "transfer"}, "default-team", ctx)
	assert.Equal(t, "transfer", out)
	assert.Equal(t, "default-team", ctx.Variables["prefer_team_id"])
}

func TestMapPipecatEvent_HangupAndError(t *testing.T) {
	assert.Equal(t, "completed", mapPipecatEvent(PipecatControlEvent{Type: "hangup"}, "", nil))
	assert.Equal(t, "completed", mapPipecatEvent(PipecatControlEvent{Type: "completed"}, "", nil))
	assert.Equal(t, "error", mapPipecatEvent(PipecatControlEvent{Type: "error"}, "", nil))
}

func TestSignalPipecatEvent(t *testing.T) {
	m := &Manager{sessions: map[string]*CallSession{}}
	ch := make(chan PipecatControlEvent, 1)
	m.sessions["call-1"] = &CallSession{ID: "call-1", PipecatEvent: ch}
	require.True(t, m.SignalPipecatEvent("call-1", PipecatControlEvent{Type: "transfer", TeamID: "t"}))
	ev := <-ch
	assert.Equal(t, "transfer", ev.Type)
	assert.Equal(t, "t", ev.TeamID)
	assert.False(t, m.SignalPipecatEvent("missing", PipecatControlEvent{Type: "hangup"}))
}

func TestPipecatEnabled(t *testing.T) {
	m := &Manager{}
	assert.False(t, m.PipecatEnabled())
	m.SetPipecat(&config.PipecatConfig{Enabled: true, BaseURL: "http://localhost:7860", AuthToken: "secret"})
	assert.True(t, m.PipecatEnabled())
	m.SetPipecat(&config.PipecatConfig{Enabled: true, BaseURL: "http://localhost:7860"})
	assert.False(t, m.PipecatEnabled())
}

func TestMergePromptWithEmptyInstructionsUsesContext(t *testing.T) {
	merged := mergeSystemWithAIContext("", "## Context Information\n\n### Salon\nOpen 9-5")
	assert.Contains(t, merged, "Context Information")
	merged2 := mergeSystemWithAIContext("Be brief.", "## Context Information\n\nx")
	assert.Contains(t, merged2, "Be brief.")
	assert.Contains(t, merged2, "Context Information")
}
