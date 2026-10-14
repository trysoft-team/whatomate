package calling

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMapAICallExit_TransferSetsTeam(t *testing.T) {
	ctx := &IVRContext{Variables: map[string]string{}}
	out := mapAICallExit(aiCallExit{Type: "transfer", TeamID: "team-1", Reason: "asked"}, "fallback", ctx)
	assert.Equal(t, "transfer", out)
	assert.Equal(t, "team-1", ctx.Variables["prefer_team_id"])
	assert.Equal(t, "asked", ctx.Variables["transfer_reason"])
}

func TestMapAICallExit_TransferUsesDefaultTeam(t *testing.T) {
	ctx := &IVRContext{Variables: map[string]string{}}
	out := mapAICallExit(aiCallExit{Type: "transfer"}, "default-team", ctx)
	assert.Equal(t, "transfer", out)
	assert.Equal(t, "default-team", ctx.Variables["prefer_team_id"])
}

func TestMapAICallExit_HangupAndError(t *testing.T) {
	assert.Equal(t, "completed", mapAICallExit(aiCallExit{Type: "hangup"}, "", nil))
	assert.Equal(t, "error", mapAICallExit(aiCallExit{Type: "error"}, "", nil))
}

func TestGeminiVoice(t *testing.T) {
	assert.Equal(t, "Kore", geminiVoice(" kore "))
	assert.Equal(t, "Puck", geminiVoice("21m00Tcm4TlvDq8ikWAM"))
	assert.Equal(t, "Puck", geminiVoice(""))
}

func TestMergePromptWithEmptyInstructionsUsesContext(t *testing.T) {
	merged := mergeSystemWithAIContext("", "## Context Information\n\n### Salon\nOpen 9-5")
	assert.Contains(t, merged, "Context Information")
	merged2 := mergeSystemWithAIContext("Be brief.", "## Context Information\n\nx")
	assert.Contains(t, merged2, "Be brief.")
	assert.Contains(t, merged2, "Context Information")
}
