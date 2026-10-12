package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func coexistenceTestAccount(t *testing.T, app *App, prefix string) (models.Organization, models.WhatsAppAccount) {
	t.Helper()
	uid := uuid.New().String()[:8]
	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      prefix + "-org-" + uid,
		Slug:      prefix + "-org-" + uid,
	}
	require.NoError(t, app.DB.Create(&org).Error)
	acct := models.WhatsAppAccount{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		Name:           prefix + "-acct-" + uid,
		PhoneID:        "phone-" + prefix + "-" + uid,
		BusinessID:     "waba-" + prefix + "-" + uid,
		AccessToken:    "token",
		IsSMB:          true,
		Status:         "active",
	}
	require.NoError(t, app.DB.Create(&acct).Error)
	return org, acct
}

func postWebhook(t *testing.T, app *App, body string) {
	t.Helper()
	req := testutil.NewRequest(t)
	req.RequestCtx.Request.Header.SetMethod("POST")
	req.RequestCtx.Request.Header.SetContentType("application/json")
	req.RequestCtx.Request.SetBody([]byte(body))
	require.NoError(t, app.WebhookHandler(req))
}

func TestWebhookHandler_smb_app_state_sync(t *testing.T) {
	app := webhookTestApp(t)
	org, acct := coexistenceTestAccount(t, app, "sync")

	stateSync := func(entries string) string {
		return `{
			"object": "whatsapp_business_account",
			"entry": [{
				"id": "` + acct.BusinessID + `",
				"changes": [{
					"field": "smb_app_state_sync",
					"value": {
						"messaging_product": "whatsapp",
						"metadata": {"display_phone_number": "15551234567", "phone_number_id": "` + acct.PhoneID + `"},
						"state_sync": [` + entries + `]
					}
				}]
			}]
		}`
	}

	// Initial sync: two contacts in one webhook.
	postWebhook(t, app, stateSync(`
		{"type": "contact", "contact": {"full_name": "Pablo Morales", "first_name": "Pablo", "phone_number": "16505551234"}, "action": "add", "metadata": {"timestamp": "1738346006"}},
		{"type": "contact", "contact": {"first_name": "Ana", "phone_number": "16505559876"}, "action": "add", "metadata": {"timestamp": "1738346006"}}
	`))

	var pablo, ana models.Contact
	assert.Eventually(t, func() bool {
		return app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, "16505551234").First(&pablo).Error == nil &&
			app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, "16505559876").First(&ana).Error == nil
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "Pablo Morales", pablo.ProfileName)
	assert.Equal(t, "Ana", ana.ProfileName, "first_name is used when full_name is missing")

	// Edit arrives as another "add" and renames the contact.
	postWebhook(t, app, stateSync(`
		{"type": "contact", "contact": {"full_name": "Pablo M.", "first_name": "Pablo", "phone_number": "16505551234"}, "action": "add", "metadata": {"timestamp": "1738346100"}}
	`))
	assert.Eventually(t, func() bool {
		var c models.Contact
		return app.DB.Where("id = ?", pablo.ID).First(&c).Error == nil && c.ProfileName == "Pablo M."
	}, 2*time.Second, 10*time.Millisecond)

	// Removing from the phone's address book keeps the contact and its chats.
	postWebhook(t, app, stateSync(`
		{"type": "contact", "contact": {"phone_number": "16505551234"}, "action": "remove", "metadata": {"timestamp": "1738346200"}}
	`))
	time.Sleep(100 * time.Millisecond)
	var still models.Contact
	require.NoError(t, app.DB.Where("id = ?", pablo.ID).First(&still).Error)
	assert.False(t, still.DeletedAt.Valid)
}

func TestWebhookHandler_History(t *testing.T) {
	app := webhookTestApp(t)
	org, acct := coexistenceTestAccount(t, app, "hist")
	uid := uuid.New().String()[:8]
	outID := "wamid.hist_out_" + uid
	mediaID := "wamid.hist_media_" + uid
	inID := "wamid.hist_in_" + uid

	history := `{
		"object": "whatsapp_business_account",
		"entry": [{
			"id": "` + acct.BusinessID + `",
			"changes": [{
				"field": "history",
				"value": {
					"messaging_product": "whatsapp",
					"metadata": {"display_phone_number": "15550783881", "phone_number_id": "` + acct.PhoneID + `"},
					"history": [{
						"metadata": {"phase": 0, "chunk_order": 1, "progress": 55},
						"threads": [{
							"id": "16505550001",
							"messages": [
								{"from": "15550783881", "id": "` + outID + `", "timestamp": "1739230955", "type": "text",
								 "text": {"body": "Here's the info you requested!"}, "history_context": {"status": "READ"}},
								{"from": "15550783881", "id": "` + mediaID + `", "timestamp": "1739230960", "type": "media_placeholder",
								 "history_context": {"status": "PLAYED"}},
								{"from": "16505550001", "id": "` + inID + `", "timestamp": "1739230970", "type": "text",
								 "text": {"body": "Thanks!"}, "history_context": {"status": "READ"}}
							]
						}]
					}]
				}
			}]
		}]
	}`
	postWebhook(t, app, history)

	var contact models.Contact
	assert.Eventually(t, func() bool {
		var n int64
		app.DB.Model(&models.Message{}).Where("organization_id = ?", org.ID).Count(&n)
		return n == 3 &&
			app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, "16505550001").First(&contact).Error == nil &&
			contact.LastMessageAt != nil
	}, 2*time.Second, 10*time.Millisecond)

	var out, media, in models.Message
	require.NoError(t, app.DB.Where("whats_app_message_id = ?", outID).First(&out).Error)
	require.NoError(t, app.DB.Where("whats_app_message_id = ?", mediaID).First(&media).Error)
	require.NoError(t, app.DB.Where("whats_app_message_id = ?", inID).First(&in).Error)

	assert.Equal(t, models.DirectionOutgoing, out.Direction)
	assert.Equal(t, models.MessageStatusRead, out.Status)
	assert.Equal(t, "Here's the info you requested!", out.Content)
	assert.Equal(t, int64(1739230955), out.CreatedAt.Unix(), "keeps the original message time")
	assert.Equal(t, contact.ID, out.ContactID)
	assert.Equal(t, "whatsapp_business_app_history", out.Metadata["source"])

	assert.Equal(t, historyMediaPlaceholder, media.Content)
	assert.Equal(t, models.MessageStatusRead, media.Status)

	assert.Equal(t, models.DirectionIncoming, in.Direction)
	assert.Equal(t, models.MessageStatusReceived, in.Status)

	require.NotNil(t, contact.LastMessageAt)
	assert.Equal(t, int64(1739230970), contact.LastMessageAt.Unix())
	assert.Equal(t, "Thanks!", contact.LastMessagePreview)
	assert.Equal(t, acct.Name, contact.WhatsAppAccount)

	// Meta may replay a chunk; messages must not be duplicated.
	postWebhook(t, app, history)
	time.Sleep(150 * time.Millisecond)
	var n int64
	app.DB.Model(&models.Message{}).Where("organization_id = ?", org.ID).Count(&n)
	assert.Equal(t, int64(3), n)

	// A business that declines history sharing sends an error chunk; nothing is imported.
	postWebhook(t, app, `{
		"object": "whatsapp_business_account",
		"entry": [{"id": "`+acct.BusinessID+`", "changes": [{"field": "history", "value": {
			"messaging_product": "whatsapp",
			"metadata": {"display_phone_number": "15550783881", "phone_number_id": "`+acct.PhoneID+`"},
			"history": [{"errors": [{"code": 2593109, "title": "History sync is turned off by the business from the WhatsApp Business App",
				"message": "History sync is turned off by the business from the WhatsApp Business App"}]}]
		}}]}]
	}`)
	time.Sleep(100 * time.Millisecond)
	app.DB.Model(&models.Message{}).Where("organization_id = ?", org.ID).Count(&n)
	assert.Equal(t, int64(3), n)
}

func TestWebhookHandler_AccountUpdate(t *testing.T) {
	app := webhookTestApp(t)
	_, acct := coexistenceTestAccount(t, app, "acctupd")

	update := func(value string) string {
		return `{"object": "whatsapp_business_account", "entry": [{"id": "` + acct.BusinessID + `", "time": 1739212624,
			"changes": [{"field": "account_update", "value": ` + value + `}]}]}`
	}
	statusOf := func() string {
		var a models.WhatsAppAccount
		require.NoError(t, app.DB.Where("id = ?", acct.ID).First(&a).Error)
		return a.Status
	}

	postWebhook(t, app, update(`{"phone_number": "15550783881", "event": "PARTNER_REMOVED",
		"disconnection_info": {"reason": "PRIMARY_INACTIVITY", "initiated_by": "SYSTEM"}}`))
	assert.Eventually(t, func() bool { return statusOf() == accountStatusDisconnected }, 2*time.Second, 10*time.Millisecond)

	postWebhook(t, app, update(`{"event": "ACCOUNT_RECONNECTED"}`))
	assert.Eventually(t, func() bool { return statusOf() == "active" }, 2*time.Second, 10*time.Millisecond)

	postWebhook(t, app, update(`{"event": "ACCOUNT_OFFBOARDED"}`))
	assert.Eventually(t, func() bool { return statusOf() == accountStatusDisconnected }, 2*time.Second, 10*time.Millisecond)

	// Unrelated account_update events leave the status alone.
	postWebhook(t, app, update(`{"event": "VERIFIED_ACCOUNT"}`))
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, accountStatusDisconnected, statusOf())
}
