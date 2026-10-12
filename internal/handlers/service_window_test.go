package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
)

// windowTestData creates a contact in an agent session with an active transfer
// and an outgoing agent reply that Meta has accepted.
func windowTestData(t *testing.T, app *App) (models.Contact, models.AgentTransfer, models.Message) {
	t.Helper()
	uid := uuid.New().String()[:8]
	org := models.Organization{BaseModel: models.BaseModel{ID: uuid.New()}, Name: "win-" + uid, Slug: "win-" + uid}
	require.NoError(t, app.DB.Create(&org).Error)

	contact := models.Contact{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		PhoneNumber:    "917777" + uid,
		Metadata: models.JSONB{
			"bot_muted_until":  time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			"bot_muted_reason": "agent_reply",
			"keep":             "me",
		},
	}
	require.NoError(t, app.DB.Create(&contact).Error)

	transfer := models.AgentTransfer{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		ContactID:       contact.ID,
		WhatsAppAccount: "test-account",
		PhoneNumber:     contact.PhoneNumber,
		Status:          models.TransferStatusActive,
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	msg := models.Message{
		BaseModel:         models.BaseModel{ID: uuid.New()},
		OrganizationID:    org.ID,
		WhatsAppAccount:   "test-account",
		ContactID:         contact.ID,
		WhatsAppMessageID: "wamid.win-" + uid,
		Direction:         models.DirectionOutgoing,
		MessageType:       models.MessageTypeText,
		Content:           "Are you still there?",
		Status:            models.MessageStatusSent,
		Metadata:          models.JSONB{},
	}
	require.NoError(t, app.DB.Create(&msg).Error)
	return contact, transfer, msg
}

func TestUpdateMessageStatus_WindowClosedEndsSessionAndResumesChatbot(t *testing.T) {
	app := webhookTestApp(t)
	contact, transfer, msg := windowTestData(t, app)

	app.updateMessageStatus(msg.WhatsAppMessageID, "failed", []WebhookStatusError{
		{Code: 131047, Title: "Re-engagement message", Message: "Message failed to send because more than 24 hours have passed since the customer last replied to this number."},
	})

	var updatedContact models.Contact
	require.NoError(t, app.DB.First(&updatedContact, contact.ID).Error)
	assert.NotContains(t, updatedContact.Metadata, "bot_muted_until", "agent session must be closed")
	assert.NotContains(t, updatedContact.Metadata, "bot_muted_reason")
	assert.Equal(t, "me", updatedContact.Metadata["keep"], "other metadata is kept")

	var updatedTransfer models.AgentTransfer
	require.NoError(t, app.DB.First(&updatedTransfer, transfer.ID).Error)
	assert.Equal(t, models.TransferStatusResumed, updatedTransfer.Status, "chatbot must take over again")
	assert.NotNil(t, updatedTransfer.ResumedAt)
	assert.Nil(t, updatedTransfer.ResumedBy, "resumed by the system, not a user")
}

func TestUpdateMessageStatus_OtherFailuresKeepSession(t *testing.T) {
	app := webhookTestApp(t)
	contact, transfer, msg := windowTestData(t, app)

	app.updateMessageStatus(msg.WhatsAppMessageID, "failed", []WebhookStatusError{
		{Code: 131026, Title: "Message undeliverable"},
	})

	var updatedContact models.Contact
	require.NoError(t, app.DB.First(&updatedContact, contact.ID).Error)
	assert.Contains(t, updatedContact.Metadata, "bot_muted_until")

	var updatedTransfer models.AgentTransfer
	require.NoError(t, app.DB.First(&updatedTransfer, transfer.ID).Error)
	assert.Equal(t, models.TransferStatusActive, updatedTransfer.Status)
}

func TestIsServiceWindowClosedSendError(t *testing.T) {
	t.Parallel()
	closed := whatsapp.ParseMetaAPIError(400, []byte(`{"error":{"message":"(#131047) Re-engagement message","code":131047}}`))
	assert.True(t, isServiceWindowClosedSendError(closed))

	other := whatsapp.ParseMetaAPIError(400, []byte(`{"error":{"message":"(#131026) Message undeliverable","code":131026}}`))
	assert.False(t, isServiceWindowClosedSendError(other))
	assert.False(t, isServiceWindowClosedSendError(errors.New("timeout")))
	assert.False(t, isServiceWindowClosedSendError(nil))
}
