package calling

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	aiCallFnEdgePrefix      = "fn:"
	aiCallSubroutineMaxHops = 32
)

// aiCallToolUsesNodes reports whether a tool should run a linked canvas subgraph.
// Only handler=nodes uses fn: edges; return_to_call alone must not hijack HTTP.
func aiCallToolUsesNodes(def *aiCallToolDef) bool {
	if def == nil {
		return false
	}
	return strings.EqualFold(def.Handler, "nodes")
}

// aiCallSubroutineAllowed are node types safe to run while an AI Call owns the
// media path (no DTMF gather / TTS takeover / nested AI / transfer).
var aiCallSubroutineAllowed = map[IVRNodeType]struct{}{
	IVRNodeHTTPCallback:    {},
	IVRNodeExecuteFunction: {},
	IVRNodeSetVariable:     {},
	IVRNodeCondition:       {},
	IVRNodeWebhook:         {},
	IVRNodeSetLabels:       {},
	IVRNodeSendMessage:     {},
	IVRNodeLanguage:        {},
	IVRNodeTiming:          {},
	IVRNodeDelay:           {},
}

// resolveAICallFnEdge returns the subroutine entry node for fn:<tool>, or "".
// Uses exact condition match only — must not fall back to "default" (that edge
// leaves the AI Call when the session ends).
func resolveAICallFnEdge(graph *IVRFlowGraph, aiCallNodeID, toolName string) string {
	if graph == nil || aiCallNodeID == "" || toolName == "" {
		return ""
	}
	want := aiCallFnEdgePrefix + toolName
	for _, e := range graph.OutgoingEdges(aiCallNodeID) {
		if e.Condition == want {
			return e.To
		}
	}
	return ""
}

type aiCallSubroutineResult struct {
	OK           bool              `json:"ok"`
	Success      bool              `json:"success"`
	Tool         string            `json:"tool,omitempty"`
	Outcome      string            `json:"outcome,omitempty"`
	LastNodeID   string            `json:"last_node_id,omitempty"`
	LastNodeType string            `json:"last_node_type,omitempty"`
	Error        string            `json:"error,omitempty"`
	HTTPBody     string            `json:"http_body,omitempty"`
	Message      string            `json:"message,omitempty"`
	Variables    map[string]string `json:"variables,omitempty"`
	Path         []string          `json:"path,omitempty"`
}

func (r aiCallSubroutineResult) JSON() string {
	b, err := json.Marshal(r)
	if err != nil {
		return `{"ok":false,"error":"marshal"}`
	}
	return string(b)
}

// runAICallToolSubroutine walks linked nodes for an AI Call function edge and
// returns a JSON tool result for the LLM. okLinked is false when no fn: edge exists.
func (m *Manager) runAICallToolSubroutine(
	session *CallSession,
	def *aiCallToolDef,
	argsJSON string,
	ctx *IVRContext,
) (result string, okLinked bool) {
	if session == nil || def == nil || ctx == nil {
		return "", false
	}
	session.mu.Lock()
	graph := session.IVRGraph
	aiNodeID := session.AICallNodeID
	session.mu.Unlock()

	startID := resolveAICallFnEdge(graph, aiNodeID, def.Name)
	if startID == "" {
		return "", false
	}
	// Only handler=nodes (or legacy ReturnToCall) runs linked canvas subgraphs.
	// Orphan fn: edges on http/transfer/etc. must not hijack inline handlers.
	if !aiCallToolUsesNodes(def) {
		return "", false
	}

	// Merge tool args into session vars so HTTP/condition nodes can use them.
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(argsJSON), &args)
	for k, v := range args {
		ctx.Variables[k] = fmt.Sprint(v)
	}

	out := m.runIVRSubroutine(session, graph, ctx, startID, def.Name)
	return out.JSON(), true
}

// runIVRSubroutine executes a logic-only subgraph starting at startID.
// Does not change session.IVRCtx.CurrentNode (AI Call remains current).
func (m *Manager) runIVRSubroutine(
	session *CallSession,
	graph *IVRFlowGraph,
	ctx *IVRContext,
	startID, toolName string,
) aiCallSubroutineResult {
	res := aiCallSubroutineResult{Tool: toolName, Variables: map[string]string{}}
	if graph == nil || ctx == nil || startID == "" {
		res.Error = "invalid subroutine"
		return res
	}

	nodeID := startID
	var lastHTTPBody string

	for hop := 0; hop < aiCallSubroutineMaxHops; hop++ {
		node := graph.Node(nodeID)
		if node == nil {
			res.Error = "node not found: " + nodeID
			return res
		}
		if _, ok := aiCallSubroutineAllowed[node.Type]; !ok {
			res.Error = fmt.Sprintf("node type %q not allowed in AI Call function subroutine", node.Type)
			res.LastNodeID = node.ID
			res.LastNodeType = string(node.Type)
			return res
		}

		res.Path = append(res.Path, node.ID)
		res.LastNodeID = node.ID
		res.LastNodeType = string(node.Type)

		outcome := m.executeAICallSubroutineNode(session, node, ctx)
		res.Outcome = outcome

		if storeAs, _ := node.Config["response_store_as"].(string); storeAs != "" {
			if v := ctx.Variables[storeAs]; v != "" {
				lastHTTPBody = v
			}
		}
		if node.Type == IVRNodeHTTPCallback || node.Type == IVRNodeExecuteFunction {
			if lastHTTPBody == "" {
				// Prefer well-known keys if author didn't set response_store_as.
				for _, k := range []string{"http_body", "response", "last_http_body"} {
					if v := ctx.Variables[k]; v != "" {
						lastHTTPBody = v
						break
					}
				}
			}
		}

		if outcome == "" {
			// Terminal within subroutine (no further routing).
			break
		}

		next := graph.ResolveEdge(node.ID, outcome)
		if next == "" {
			break
		}
		nodeID = next
	}

	if len(res.Path) >= aiCallSubroutineMaxHops {
		res.Error = "subroutine hop limit exceeded"
		return res
	}

	res.OK = res.Error == ""
	res.HTTPBody = lastHTTPBody
	res.Success = subroutineSuccess(res, ctx)
	if res.Success {
		res.Message = "Linked nodes completed successfully. Confirm the outcome to the caller using http_body or variables if useful."
	} else if res.Error != "" {
		res.Message = "Linked nodes failed before completion (" + res.Error + "). Tell the caller the action was not processed and offer to try again or transfer."
	} else {
		res.Message = "Linked nodes did not succeed. Tell the caller the action was not processed and offer to try again or transfer."
	}
	// Snapshot a few useful vars (avoid dumping secrets wholesale: cap size).
	for k, v := range ctx.Variables {
		if len(res.Variables) >= 40 {
			break
		}
		if strings.HasPrefix(k, "_") {
			continue
		}
		if len(v) > 2000 {
			v = v[:2000] + "…"
		}
		res.Variables[k] = v
	}
	return res
}

func subroutineSuccess(res aiCallSubroutineResult, ctx *IVRContext) bool {
	if res.Error != "" {
		return false
	}
	if ctx != nil && ctx.Variables != nil {
		if v := strings.ToLower(strings.TrimSpace(ctx.Variables["booking_ok"])); v == "true" || v == "1" || v == "yes" {
			return true
		}
		if st := ctx.Variables["last_http_status"]; st != "" {
			if st == "200" || st == "201" || st == "202" || st == "204" {
				return true
			}
		}
	}
	switch res.Outcome {
	case "http:2xx", "default", "true":
		return true
	case "http:non2xx", "error", "false":
		return false
	}
	return res.OK && res.HTTPBody != ""
}

func (m *Manager) executeAICallSubroutineNode(
	session *CallSession,
	node *IVRNode,
	ctx *IVRContext,
) string {
	switch node.Type {
	case IVRNodeHTTPCallback:
		return m.executeHTTPCallback(session, node, ctx)
	case IVRNodeExecuteFunction:
		return m.executeFunction(session, node, ctx)
	case IVRNodeSetVariable:
		return m.executeSetVariable(session, node, ctx)
	case IVRNodeCondition:
		return m.executeCondition(session, node, ctx)
	case IVRNodeWebhook:
		return m.executeWebhook(session, node, ctx)
	case IVRNodeSetLabels:
		return m.executeSetLabels(session, node, ctx)
	case IVRNodeSendMessage:
		return m.executeSendMessage(session, node, ctx)
	case IVRNodeLanguage:
		return m.executeLanguage(session, node, ctx)
	case IVRNodeTiming:
		return m.executeTiming(session, node)
	case IVRNodeDelay:
		// Silence-only: do not play hold music while AI Call owns audio.
		secs := getConfigInt(node.Config, "seconds", 1)
		if secs < 0 {
			secs = 0
		}
		if secs > 10 {
			secs = 10 // keep tool latency bounded
		}
		if secs > 0 {
			time.Sleep(time.Duration(secs) * time.Second)
		}
		return "default"
	default:
		return ""
	}
}
