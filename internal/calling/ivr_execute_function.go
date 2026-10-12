package calling

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// executeFunction runs a standalone HTTP request (whatsCRM MAKE_REQUEST / execute_function parity).
// Outcomes: default (2xx), error.
func (m *Manager) executeFunction(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	url, _ := node.Config["url"].(string)
	method, _ := node.Config["method"].(string)
	if method == "" {
		method = "POST"
	}
	bodyTemplate, _ := node.Config["body_template"].(string)
	timeoutSecs := getConfigInt(node.Config, "timeout_seconds", 15)
	responseStoreAs, _ := node.Config["response_store_as"].(string)
	responsePath, _ := node.Config["response_path"].(string)
	name, _ := node.Config["name"].(string)

	headersRaw, _ := node.Config["headers"].(map[string]any)
	headers := make(map[string]string, len(headersRaw))
	for k, v := range headersRaw {
		if s, ok := v.(string); ok {
			headers[k] = interpolateTemplate(s, ctx.Variables)
		}
	}

	url = interpolateTemplate(url, ctx.Variables)
	body := interpolateTemplate(bodyTemplate, ctx.Variables)
	if strings.TrimSpace(url) == "" {
		m.log.Warn("IVR execute_function: empty URL", "call_id", session.ID, "name", name)
		return "error"
	}

	result, err := executeHTTPCallback(url, method, headers, body, time.Duration(timeoutSecs)*time.Second)
	if err != nil {
		m.log.Error("IVR execute_function failed", "error", err, "call_id", session.ID, "url", url, "name", name)
		return "error"
	}

	storeBody := result.Body
	if responsePath != "" {
		if extracted, ok := jsonPathString(result.Body, responsePath); ok {
			storeBody = extracted
		}
	}
	if responseStoreAs != "" {
		if ctx.Variables == nil {
			ctx.Variables = map[string]string{}
		}
		ctx.Variables[responseStoreAs] = storeBody
	}
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	ctx.Variables["last_http_body"] = storeBody
	ctx.Variables["last_http_status"] = fmt.Sprintf("%d", result.StatusCode)

	m.log.Info("IVR execute_function completed",
		"call_id", session.ID, "name", name, "url", url, "status", result.StatusCode)

	if result.StatusCode >= 200 && result.StatusCode < 300 {
		return "default"
	}
	return "error"
}

// jsonPathString extracts a dotted path from a JSON object string (e.g. "data.id").
func jsonPathString(raw, path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	var root any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return "", false
	}
	cur := root
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[part]
		if !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64, bool, nil:
		return fmt.Sprint(v), true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}
