package calling

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func aiCallCfgWithTransfer(returnToCall bool) map[string]any {
	return map[string]any{
		"tools": []any{
			map[string]any{"name": "transfer_to_agent", "handler": "transfer", "return_to_call": returnToCall},
			map[string]any{"name": "end_call", "handler": "hangup"},
		},
	}
}

func TestAICallTransferReturnsToCall(t *testing.T) {
	assert.True(t, aiCallTransferReturnsToCall(aiCallCfgWithTransfer(true)))
	assert.False(t, aiCallTransferReturnsToCall(aiCallCfgWithTransfer(false)))
	assert.False(t, aiCallTransferReturnsToCall(map[string]any{}))
}

func TestTakeAITransferReturn_NoAnswerGoesBackToAICall(t *testing.T) {
	ctx := &IVRContext{Variables: map[string]string{}}
	markAITransferReturn(ctx, "ai_call_1")

	assert.Equal(t, "ai_call_1", takeAITransferReturn(ctx, "no_answer"))
	assert.True(t, takeAITransferBusy(ctx))
	assert.False(t, takeAITransferBusy(ctx), "busy flag is consumed once")
}

func TestTakeAITransferReturn_OtherOutcomesFollowEdges(t *testing.T) {
	for _, outcome := range []string{"completed", "abandoned"} {
		ctx := &IVRContext{Variables: map[string]string{}}
		markAITransferReturn(ctx, "ai_call_1")
		assert.Equal(t, "", takeAITransferReturn(ctx, outcome), outcome)
		assert.False(t, takeAITransferBusy(ctx))
		assert.Equal(t, "", takeAITransferReturn(ctx, "no_answer"), "marker cleared after %s", outcome)
	}
}

func TestTakeAITransferReturn_WithoutMarkerFollowsEdges(t *testing.T) {
	ctx := &IVRContext{Variables: map[string]string{}}
	assert.Equal(t, "", takeAITransferReturn(ctx, "no_answer"))
}

func TestTakeAITransferReturn_CapsReturns(t *testing.T) {
	ctx := &IVRContext{Variables: map[string]string{}}
	for i := 0; i < maxAITransferReturns; i++ {
		markAITransferReturn(ctx, "ai_call_1")
		assert.Equal(t, "ai_call_1", takeAITransferReturn(ctx, "no_answer"))
		takeAITransferBusy(ctx)
	}
	markAITransferReturn(ctx, "ai_call_1")
	assert.Equal(t, "", takeAITransferReturn(ctx, "no_answer"), "falls through to the no_answer edge once capped")
}

func TestAITransferBusyPrompt(t *testing.T) {
	lines := []any{
		map[string]any{"role": "assistant", "text": "Welcome to the salon."},
		map[string]any{"role": "user", "text": "Can I talk to someone?"},
	}
	prompt := aiTransferBusyPrompt(lines)
	assert.Contains(t, prompt, "no agent answered")
	assert.Contains(t, prompt, "Assistant: Welcome to the salon.")
	assert.Contains(t, prompt, "Caller: Can I talk to someone?")

	assert.NotContains(t, aiTransferBusyPrompt(nil), "Conversation so far")
}

func TestAITransferBusyPrompt_KeepsRecentLines(t *testing.T) {
	var lines []any
	for i := 0; i < aiTransferHistoryLines+10; i++ {
		lines = append(lines, map[string]any{"role": "user", "text": "line"})
	}
	lines = append(lines, map[string]any{"role": "user", "text": "most recent"})
	prompt := aiTransferBusyPrompt(lines)
	assert.Contains(t, prompt, "most recent")
	assert.Equal(t, aiTransferHistoryLines, strings.Count(prompt, "Caller: "))
}
