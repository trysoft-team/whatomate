package calling

import (
	"fmt"
	"strconv"
	"strings"
)

// When the AI Call transfer tool has return_to_call set and no agent answers,
// the flow goes back to the AI Call node instead of the transfer node's
// no_answer edge, and the AI tells the caller agents are busy.
const (
	aiTransferReturnNodeVar  = "__ai_transfer_return_node"
	aiTransferReturnCountVar = "__ai_transfer_returns"
	aiTransferBusyVar        = "__ai_transfer_busy"
	maxAITransferReturns     = 2
	aiTransferHistoryLines   = 40

	aiTransferBusyWelcome = "Sorry, all our agents are busy right now. I'm still here to help — what else can I do for you?"
)

func aiCallTransferReturnsToCall(cfg map[string]any) bool {
	for _, t := range parseAICallTools(cfg["tools"]) {
		if t.Handler == "transfer" || t.Name == "transfer_to_agent" {
			return t.ReturnToCall
		}
	}
	return false
}

func markAITransferReturn(ctx *IVRContext, aiNodeID string) {
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	ctx.Variables[aiTransferReturnNodeVar] = aiNodeID
}

// takeAITransferReturn consumes the return marker after a transfer node and
// returns the AI Call node to resume, or "" to follow the transfer's edges.
func takeAITransferReturn(ctx *IVRContext, transferOutcome string) string {
	if ctx.Variables == nil {
		return ""
	}
	nodeID := ctx.Variables[aiTransferReturnNodeVar]
	delete(ctx.Variables, aiTransferReturnNodeVar)
	if nodeID == "" || transferOutcome != "no_answer" {
		return ""
	}
	n, _ := strconv.Atoi(ctx.Variables[aiTransferReturnCountVar])
	if n >= maxAITransferReturns {
		return ""
	}
	ctx.Variables[aiTransferReturnCountVar] = strconv.Itoa(n + 1)
	ctx.Variables[aiTransferBusyVar] = "1"
	return nodeID
}

func takeAITransferBusy(ctx *IVRContext) bool {
	if ctx == nil || ctx.Variables == nil || ctx.Variables[aiTransferBusyVar] == "" {
		return false
	}
	delete(ctx.Variables, aiTransferBusyVar)
	return true
}

// aiTransferBusyPrompt is appended to the system prompt of the resumed AI
// Call. A new realtime session has no memory, so the earlier transcript is
// replayed for continuity.
func aiTransferBusyPrompt(transcript []any) string {
	var b strings.Builder
	b.WriteString("Earlier in this call you tried to transfer the caller to a human agent, but no agent answered — all agents are busy. ")
	b.WriteString("Tell the caller the agents are busy, then keep helping them yourself and continue from where the conversation left off. ")
	b.WriteString("Do not greet them again. Only call transfer_to_agent again if the caller insists on a person.")

	var lines []string
	for _, raw := range transcript {
		line, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := line["role"].(string)
		text, _ := line["text"].(string)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		speaker := "Caller"
		if role == "assistant" || role == "model" || role == "bot" {
			speaker = "Assistant"
		}
		lines = append(lines, fmt.Sprintf("%s: %s", speaker, text))
	}
	if len(lines) > aiTransferHistoryLines {
		lines = lines[len(lines)-aiTransferHistoryLines:]
	}
	if len(lines) > 0 {
		b.WriteString("\n\nConversation so far:\n")
		b.WriteString(strings.Join(lines, "\n"))
	}
	return b.String()
}

// applyAITransferBusy rewrites the system prompt and opening line when the AI
// Call resumes after an unanswered transfer.
func (m *Manager) applyAITransferBusy(session *CallSession, ctx *IVRContext, systemInstr, welcome string) (string, string) {
	if !takeAITransferBusy(ctx) {
		return systemInstr, welcome
	}
	var transcript []any
	if m.db != nil {
		transcript, _ = m.loadAICallArtifacts(session.CallLogID)["transcript"].([]any)
	}
	m.log.Info("IVR ai_call resuming after unanswered transfer", "call_id", session.ID, "history_lines", len(transcript))
	return systemInstr + "\n\n" + aiTransferBusyPrompt(transcript), aiTransferBusyWelcome
}
