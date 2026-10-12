package handlers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func TestParseAICustomActionDraft_Webhook(t *testing.T) {
	t.Parallel()
	raw := "Sure!\n```json\n" + `{
		"name": "Create helpdesk ticket",
		"icon": "Ticket",
		"action_type": "Webhook",
		"config": {
			"url": "https://your-helpdesk.example.com/api/tickets?source={{organization.name}}",
			"method": "delete",
			"headers": {"Authorization": "Bearer YOUR_API_KEY", "Content-Type": "text/plain", "Bad Header": "x", "X-Multi": "a\r\nb", "X-Num": 5},
			"body": {"subject": "WhatsApp: {{contact.name}}", "phone": "{{contact.phone_number}}"}
		},
		"notes": "Replace the URL and YOUR_API_KEY."
	}` + "\n```"

	d, err := parseAICustomActionDraft(raw)
	require.NoError(t, err)
	assert.Equal(t, "Create helpdesk ticket", d.Name)
	assert.Equal(t, "ticket", d.Icon)
	assert.Equal(t, models.ActionTypeWebhook, d.ActionType)
	assert.Equal(t, "https://your-helpdesk.example.com/api/tickets?source={{organization.name}}", d.Config["url"])
	assert.Equal(t, "POST", d.Config["method"], "unsupported method falls back to POST")
	assert.Equal(t, map[string]string{"Authorization": "Bearer YOUR_API_KEY", "X-Num": "5"}, d.Config["headers"])
	body := d.Config["body"].(string)
	assert.Contains(t, body, `"subject": "WhatsApp: {{contact.name}}"`, "object body becomes JSON text")
	assert.Equal(t, "Replace the URL and YOUR_API_KEY.", d.Notes)
}

func TestParseAICustomActionDraft_URLAndJavascript(t *testing.T) {
	t.Parallel()

	d, err := parseAICustomActionDraft(`{"action":{"name":"Open in CRM","icon":"rocket","action_type":"url","config":{"url":"https://crm.example.com/search?q={{contact.phone_number}}"}}}`)
	require.NoError(t, err)
	assert.Equal(t, models.ActionTypeURL, d.ActionType)
	assert.Equal(t, "zap", d.Icon, "unknown icon falls back")
	assert.Equal(t, true, d.Config["open_in_new_tab"])

	d, err = parseAICustomActionDraft(`{"name":"","action_type":"javascript","config":{"code":"return { clipboard: contact.phone_number, toast: { message: 'Copied', type: 'success' } };"}}`)
	require.NoError(t, err)
	assert.Equal(t, models.ActionTypeJavascript, d.ActionType)
	assert.Equal(t, "Run script", d.Name)
	assert.Contains(t, d.Config["code"], "clipboard")

	// String bodies with unquoted variables are kept as written.
	d, err = parseAICustomActionDraft(`{"name":"Hook","action_type":"webhook","config":{"url":"https://h.example.com","method":"put","body":"{\"id\": {{contact.id}}}"}}`)
	require.NoError(t, err)
	assert.Equal(t, "PUT", d.Config["method"])
	assert.Equal(t, `{"id": {{contact.id}}}`, d.Config["body"])
}

func TestParseAICustomActionDraft_Rejects(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"no json here",
		`{"name":"x","action_type":"shell","config":{"cmd":"rm -rf /"}}`,
		`{"name":"x","action_type":"url","config":{"url":"javascript:alert(1)"}}`,
		`{"name":"x","action_type":"webhook","config":{"url":"{{contact.metadata.url}}"}}`,
		`{"name":"x","action_type":"javascript","config":{"code":"return {"}}`,
		`{"name":"x","action_type":"javascript","config":{"code":"` + strings.Repeat("a", aiCustomActionMaxCode+1) + `"}}`,
	} {
		_, err := parseAICustomActionDraft(raw)
		assert.Error(t, err, truncateRunes(raw, 80))
	}
}

func TestAICustomActionSystemPrompt_ListsIcons(t *testing.T) {
	t.Parallel()
	for _, icon := range aiCustomActionIcons {
		assert.Contains(t, aiCustomActionSystemPrompt, icon)
	}
}
