package calling

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
)

// buildIVRAIContext loads enabled org AI Contexts (same source as chatbot AI)
// and formats them into a "## Context Information" block for the LLM system prompt.
// Account-specific contexts (matching session.AccountName) come first, then org-global.
//
// cfg may include:
//   - ai_context_mode: "all" (default) | "selected"
//   - ai_context_ids: []string UUIDs — used when mode is "selected", or when non-empty
//     without an explicit mode (backward-compatible filter).
//
// Empty/missing selection with mode "all" loads every enabled context.
func (m *Manager) buildIVRAIContext(session *CallSession, ctx *IVRContext, userMessage string, cfg map[string]any) string {
	if m.db == nil || session == nil {
		return ""
	}
	contexts := m.loadAIContexts(session.OrganizationID, session.AccountName)
	contexts = filterAIContextsByConfig(contexts, cfg)
	if len(contexts) == 0 {
		return ""
	}

	vars := map[string]string{}
	if ctx != nil && ctx.Variables != nil {
		for k, v := range ctx.Variables {
			vars[k] = v
		}
	}
	vars["user_message"] = userMessage
	vars["senderMessage"] = userMessage
	if session.CallerPhone != "" {
		vars["phone_number"] = session.CallerPhone
		vars["caller_phone"] = session.CallerPhone
	}
	if session.CallerName != "" {
		vars["caller_name"] = session.CallerName
		vars["contact_name"] = session.CallerName
	}

	var parts []string
	for _, ac := range contexts {
		content := strings.TrimSpace(ac.StaticContent)
		if ac.ContextType == models.ContextTypeAPI {
			apiContent, err := m.fetchIVRAIContextAPI(ac.ApiConfig, vars)
			if err != nil {
				m.log.Warn("IVR AI context API fetch failed",
					"context", ac.Name, "error", err, "call_id", session.ID)
			} else if strings.TrimSpace(apiContent) != "" {
				if content != "" {
					content = content + "\n\nData:\n" + apiContent
				} else {
					content = apiContent
				}
			}
		}
		if content != "" {
			parts = append(parts, fmt.Sprintf("### %s\n%s", ac.Name, content))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "## Context Information\n\n" + strings.Join(parts, "\n\n")
}

func (m *Manager) loadAIContexts(orgID uuid.UUID, accountName string) []models.AIContext {
	var accountContexts, globalContexts []models.AIContext
	if accountName != "" {
		_ = m.db.Where("organization_id = ? AND whats_app_account = ? AND is_enabled = true",
			orgID, accountName).
			Order("priority DESC").
			Find(&accountContexts).Error
	}
	_ = m.db.Where("organization_id = ? AND whats_app_account = '' AND is_enabled = true", orgID).
		Order("priority DESC").
		Find(&globalContexts).Error
	return append(accountContexts, globalContexts...)
}

// parseAIContextIDs extracts UUID list from config.ai_context_ids ([]any or []string).
func parseAIContextIDs(cfg map[string]any) []uuid.UUID {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg["ai_context_ids"]
	if !ok || raw == nil {
		return nil
	}
	var out []uuid.UUID
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			s, _ := item.(string)
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil {
				continue
			}
			out = append(out, id)
		}
	case []string:
		for _, s := range v {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil {
				continue
			}
			out = append(out, id)
		}
	}
	return out
}

// filterAIContextsByConfig applies ai_context_mode / ai_context_ids.
// - mode "all" or missing with empty ids → keep all
// - mode "selected" → keep only matching IDs (empty ids → none)
// - non-empty ids without mode → keep only matching IDs
func filterAIContextsByConfig(contexts []models.AIContext, cfg map[string]any) []models.AIContext {
	ids := parseAIContextIDs(cfg)
	mode := ""
	if cfg != nil {
		mode, _ = cfg["ai_context_mode"].(string)
		mode = strings.ToLower(strings.TrimSpace(mode))
	}
	switch mode {
	case "selected":
		return filterAIContextsByIDs(contexts, ids)
	case "all", "":
		if len(ids) == 0 {
			return contexts
		}
		return filterAIContextsByIDs(contexts, ids)
	default:
		if len(ids) == 0 {
			return contexts
		}
		return filterAIContextsByIDs(contexts, ids)
	}
}

func filterAIContextsByIDs(contexts []models.AIContext, ids []uuid.UUID) []models.AIContext {
	if len(ids) == 0 {
		return nil
	}
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]models.AIContext, 0, len(ids))
	for _, c := range contexts {
		if _, ok := want[c.ID]; ok {
			out = append(out, c)
		}
	}
	return out
}

func (m *Manager) fetchIVRAIContextAPI(apiConfig models.JSONB, vars map[string]string) (string, error) {
	if apiConfig == nil {
		return "", fmt.Errorf("empty api config")
	}
	url, _ := apiConfig["url"].(string)
	method, _ := apiConfig["method"].(string)
	if method == "" {
		method = "GET"
	}
	bodyTemplate, _ := apiConfig["body"].(string)
	if bodyTemplate == "" {
		bodyTemplate, _ = apiConfig["body_template"].(string)
	}
	timeoutSecs := 10
	if t, ok := apiConfig["timeout_seconds"].(float64); ok && t > 0 {
		timeoutSecs = int(t)
	}

	headers := map[string]string{}
	if hdrs, ok := apiConfig["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				headers[k] = interpolateTemplate(s, vars)
			}
		}
	}

	url = interpolateTemplate(url, vars)
	body := interpolateTemplate(bodyTemplate, vars)
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("empty URL")
	}

	result, err := executeHTTPCallback(url, method, headers, body, time.Duration(timeoutSecs)*time.Second)
	if err != nil {
		return "", err
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return "", fmt.Errorf("status %d", result.StatusCode)
	}

	if path, ok := apiConfig["response_path"].(string); ok && strings.TrimSpace(path) != "" {
		if extracted, ok := jsonPathString(result.Body, path); ok {
			return extracted, nil
		}
	}
	// Pretty-compact JSON when possible
	var raw any
	if err := json.Unmarshal([]byte(result.Body), &raw); err == nil {
		if b, err := json.Marshal(raw); err == nil {
			return string(b), nil
		}
	}
	return result.Body, nil
}

// mergeSystemWithAIContext appends AI Context block to the system prompt.
func mergeSystemWithAIContext(systemInstr, contextData string) string {
	systemInstr = strings.TrimSpace(systemInstr)
	contextData = strings.TrimSpace(contextData)
	if contextData == "" {
		return systemInstr
	}
	if systemInstr == "" {
		return contextData
	}
	return systemInstr + "\n\n" + contextData
}

// formatIVRExtraKnowledge builds a "## Extra Knowledge" block from AI Call
// config key extra_knowledge: [{name, value}, ...]. Values are template-
// interpolated. Empty names or empty values are skipped.
func formatIVRExtraKnowledge(cfg map[string]any, vars map[string]string) string {
	if cfg == nil {
		return ""
	}
	raw, ok := cfg["extra_knowledge"].([]any)
	if !ok || len(raw) == 0 {
		return ""
	}
	if vars == nil {
		vars = map[string]string{}
	}
	var lines []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		val, _ := m["value"].(string)
		val = strings.TrimSpace(interpolateTemplate(val, vars))
		if val == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- **%s**: %s", name, val))
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Extra Knowledge\n\n" + strings.Join(lines, "\n")
}

// composeAICallSystemPrompt merges org/system instructions with AI Contexts
// and optional extra_knowledge facts from the AI Call node config.
func (m *Manager) composeAICallSystemPrompt(session *CallSession, ctx *IVRContext, baseSystem, userMessage string, cfg map[string]any) string {
	vars := map[string]string{}
	if ctx != nil && ctx.Variables != nil {
		for k, v := range ctx.Variables {
			vars[k] = v
		}
	}
	out := mergeSystemWithAIContext(baseSystem, m.buildIVRAIContext(session, ctx, userMessage, cfg))
	return mergeSystemWithAIContext(out, formatIVRExtraKnowledge(cfg, vars))
}
