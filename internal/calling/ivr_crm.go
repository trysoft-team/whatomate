package calling

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
)

// executeSetLabels adds/removes contact tags. Outcomes: "default", "error".
func (m *Manager) executeSetLabels(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	if session.ContactID == uuid.Nil || m.db == nil {
		m.log.Warn("IVR set_labels skipped: no contact", "call_id", session.ID)
		return "error"
	}
	var contact models.Contact
	if err := m.db.Where("id = ? AND organization_id = ?", session.ContactID, session.OrganizationID).First(&contact).Error; err != nil {
		m.log.Error("IVR set_labels contact load failed", "error", err, "call_id", session.ID)
		return "error"
	}

	add := stringSliceFromIVRConfig(node.Config, "add", "labelsToAdd")
	remove := stringSliceFromIVRConfig(node.Config, "remove", "labelsToRemove")
	// Interpolate label names
	for i, t := range add {
		add[i] = interpolateTemplate(t, ctx.Variables)
	}
	for i, t := range remove {
		remove[i] = interpolateTemplate(t, ctx.Variables)
	}

	tagSet := map[string]struct{}{}
	for _, t := range contact.Tags {
		if s, ok := t.(string); ok && s != "" {
			tagSet[s] = struct{}{}
		}
	}
	for _, t := range remove {
		delete(tagSet, t)
	}
	for _, t := range add {
		if t != "" {
			tagSet[t] = struct{}{}
		}
	}
	tags := make(models.JSONBArray, 0, len(tagSet))
	tagStrings := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
		tagStrings = append(tagStrings, t)
	}
	sort.Strings(tagStrings)
	if err := m.db.Model(&contact).Update("tags", tags).Error; err != nil {
		m.log.Error("IVR set_labels update failed", "error", err, "call_id", session.ID)
		return "error"
	}
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	ctx.Variables["tags"] = strings.Join(tagStrings, ",")
	m.log.Info("IVR set_labels applied", "call_id", session.ID, "tags", tagStrings)
	return "default"
}

// executeSendMessage sends a WhatsApp text to a configured phone (or the caller).
// Config "to" / "phone" may use {{variables}}; empty → caller_phone / session.CallerPhone.
// Outcomes: "default", "error".
func (m *Manager) executeSendMessage(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	if m.whatsapp == nil || m.db == nil {
		return "error"
	}
	body, _ := node.Config["body"].(string)
	vars := map[string]string{}
	if ctx != nil && ctx.Variables != nil {
		vars = ctx.Variables
	}
	body = strings.TrimSpace(stripUnresolvedTemplates(interpolateTemplate(body, vars)))
	if body == "" {
		if msg := strings.TrimSpace(vars["message"]); msg != "" {
			body = msg
		}
	}
	if body == "" {
		body = fallbackSendMessageBody(vars)
	}
	if body == "" {
		m.log.Warn("IVR send_message empty body", "call_id", session.ID, "vars", len(vars))
		return "error"
	}

	phone := resolveSendMessagePhone(node, session, vars)
	if phone == "" {
		m.log.Warn("IVR send_message no recipient phone", "call_id", session.ID)
		return "error"
	}

	var account models.WhatsAppAccount
	if err := m.db.Where("organization_id = ? AND name = ?", session.OrganizationID, session.AccountName).
		First(&account).Error; err != nil {
		m.log.Error("IVR send_message account load failed", "error", err, "call_id", session.ID)
		return "error"
	}
	wa := account.ToWAAccount()

	msgID, err := m.whatsapp.SendTextMessage(context.Background(), wa, whatsapp.Recipient{Phone: phone}, body)
	if err != nil {
		m.log.Error("IVR send_message failed", "error", err, "call_id", session.ID, "to", phone)
		return "error"
	}
	m.log.Info("IVR send_message sent", "call_id", session.ID, "message_id", msgID, "to", phone)
	return "default"
}

// resolveSendMessagePhone returns the WhatsApp recipient for a send_message node.
// Order: config to/phone (templates), tool/session vars phone|to|recipient, then caller.
func resolveSendMessagePhone(node *IVRNode, session *CallSession, vars map[string]string) string {
	if node != nil && node.Config != nil {
		for _, key := range []string{"to", "phone"} {
			if raw, ok := node.Config[key].(string); ok {
				if phone := normalizedPhone(interpolateTemplate(raw, vars)); phone != "" {
					return phone
				}
			}
		}
	}
	for _, key := range []string{"phone", "to", "recipient", "confirm_phone"} {
		if phone := normalizedPhone(vars[key]); phone != "" {
			return phone
		}
	}
	if phone := normalizedPhone(vars["caller_phone"]); phone != "" {
		return phone
	}
	if session != nil {
		return normalizedPhone(session.CallerPhone)
	}
	return ""
}

// normalizedPhone trims and rejects empty / unresolved {{template}} leftovers.
func normalizedPhone(raw string) string {
	phone := strings.TrimSpace(raw)
	if phone == "" || strings.Contains(phone, "{{") {
		return ""
	}
	return phone
}

// stripUnresolvedTemplates removes leftover {{var}} tokens after interpolation.
func stripUnresolvedTemplates(s string) string {
	out := s
	for {
		start := strings.Index(out, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(out[start:], "}}")
		if end < 0 {
			break
		}
		out = out[:start] + out[start+end+2:]
	}
	return strings.TrimSpace(out)
}

// fallbackSendMessageBody builds a short WhatsApp body from common session vars.
// Org-specific branding belongs in each flow's Send Message template, not here.
func fallbackSendMessageBody(vars map[string]string) string {
	if vars == nil {
		return ""
	}
	if msg := strings.TrimSpace(vars["message"]); msg != "" {
		return msg
	}
	var parts []string
	name := strings.TrimSpace(vars["guest_name"])
	if name == "" {
		name = strings.TrimSpace(vars["caller_name"])
	}
	if name == "" {
		name = strings.TrimSpace(vars["name"])
	}
	if name != "" {
		parts = append(parts, "Name: "+name)
	}
	if d := strings.TrimSpace(vars["date"]); d != "" {
		parts = append(parts, "Date: "+d)
	}
	if t := strings.TrimSpace(vars["time"]); t != "" {
		parts = append(parts, "Time: "+t)
	}
	if n := strings.TrimSpace(vars["notes"]); n != "" {
		parts = append(parts, "Notes: "+n)
	}
	if len(parts) == 0 {
		return "Your request is confirmed. Thank you!"
	}
	return "Confirmation\n" + strings.Join(parts, "\n")
}

// executeWebhook fires an async HTTP request and always continues (outcome "default").
func (m *Manager) executeWebhook(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	url, _ := node.Config["url"].(string)
	method, _ := node.Config["method"].(string)
	if method == "" {
		method = "POST"
	}
	bodyTemplate, _ := node.Config["body_template"].(string)
	timeoutSecs := getConfigInt(node.Config, "timeout_seconds", 10)

	headersRaw, _ := node.Config["headers"].(map[string]any)
	headers := make(map[string]string, len(headersRaw))
	vars := map[string]string{}
	if ctx != nil && ctx.Variables != nil {
		vars = ctx.Variables
	}
	for k, v := range headersRaw {
		if s, ok := v.(string); ok {
			headers[k] = interpolateTemplate(s, vars)
		}
	}
	url = interpolateTemplate(url, vars)
	body := interpolateTemplate(bodyTemplate, vars)
	callID := session.ID

	go func() {
		result, err := executeHTTPCallback(url, method, headers, body, time.Duration(timeoutSecs)*time.Second)
		if err != nil {
			m.log.Error("IVR webhook failed", "error", err, "call_id", callID, "url", url)
			return
		}
		m.log.Info("IVR webhook completed", "call_id", callID, "url", url, "status", result.StatusCode)
	}()

	return "default"
}

// executeLanguage stores a locale hint on the session for subsequent TTS.
func (m *Manager) executeLanguage(session *CallSession, node *IVRNode, ctx *IVRContext) string {
	locale, _ := node.Config["locale"].(string)
	locale = strings.TrimSpace(locale)
	if locale == "" {
		locale, _ = node.Config["language"].(string)
		locale = strings.TrimSpace(locale)
	}
	session.mu.Lock()
	session.Locale = locale
	session.mu.Unlock()
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	ctx.Variables["locale"] = locale
	m.log.Info("IVR language set", "call_id", session.ID, "locale", locale)
	return "default"
}

// prepareTransferFromNode applies agent preference, optional queue announce, then
// returns teamID / overflowTeamID for executeTransfer.
func (m *Manager) prepareTransferFromNode(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) (teamID, overflowTeamID string) {
	teamID, _ = node.Config["team_id"].(string)
	overflowTeamID, _ = node.Config["overflow_team_id"].(string)
	if teamID == "" && ctx != nil && ctx.Variables != nil {
		if v := strings.TrimSpace(ctx.Variables["prefer_team_id"]); v != "" {
			teamID = v
		}
	}

	if agentStr, _ := node.Config["agent_id"].(string); strings.TrimSpace(agentStr) != "" {
		if id, err := uuid.Parse(strings.TrimSpace(agentStr)); err == nil {
			session.mu.Lock()
			session.PreferredAgentID = &id
			session.mu.Unlock()
		}
	}

	announce, _ := node.Config["announce_queue"].(bool)
	if announce && player != nil && m.tts != nil && m.db != nil {
		var waiting int64
		m.db.Model(&models.CallTransfer{}).
			Where("organization_id = ? AND status = ?", session.OrganizationID, models.CallTransferStatusWaiting).
			Count(&waiting)
		pos := waiting + 1
		text := fmt.Sprintf("Please hold. You are number %d in the queue.", pos)
		if custom, _ := node.Config["queue_announce_text"].(string); strings.TrimSpace(custom) != "" {
			vars := map[string]string{"position": fmt.Sprintf("%d", pos)}
			if ctx != nil && ctx.Variables != nil {
				for k, v := range ctx.Variables {
					vars[k] = v
				}
			}
			text = interpolateTemplate(custom, vars)
		}
		if filename, err := m.synthesizeTTS(session.OrganizationID, text, nil); err == nil && m.config.AudioDir != "" {
			fullPath := filepath.Join(m.config.AudioDir, filename)
			if _, err := player.PlayFile(fullPath); err != nil {
				m.log.Error("IVR queue announce playback failed", "error", err, "call_id", session.ID)
			}
		}
	}
	return teamID, overflowTeamID
}

func stringSliceFromIVRConfig(cfg map[string]any, keys ...string) []string {
	for _, key := range keys {
		raw, ok := cfg[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case []string:
			return append([]string{}, v...)
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			return out
		case string:
			parts := strings.Split(v, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return nil
}
