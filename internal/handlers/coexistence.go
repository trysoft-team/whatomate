package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/contactutil"
	"github.com/trysoft/whatomate/internal/models"
)

// Account statuses driven by coexistence account_update webhooks.
const accountStatusDisconnected = "disconnected"

// historyMediaPlaceholder is stored for app-history media Meta doesn't replay.
const historyMediaPlaceholder = "[media]"

// SMBStateSyncEntry is one contact change in an smb_app_state_sync webhook.
type SMBStateSyncEntry struct {
	Type    string `json:"type"`
	Contact struct {
		FullName    string `json:"full_name"`
		FirstName   string `json:"first_name"`
		PhoneNumber string `json:"phone_number"`
	} `json:"contact"`
	Action string `json:"action"`
}

// flexInt accepts a JSON number or a numeric string.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexInt(n)
	return nil
}

// HistoryChunk is one entry of a coexistence "history" webhook.
type HistoryChunk struct {
	Metadata struct {
		Phase      flexInt `json:"phase"`
		ChunkOrder flexInt `json:"chunk_order"`
		Progress   flexInt `json:"progress"`
	} `json:"metadata"`
	Threads []HistoryThread      `json:"threads,omitempty"`
	Errors  []WebhookStatusError `json:"errors,omitempty"`
}

// HistoryThread holds the replayed messages exchanged with one WhatsApp user.
type HistoryThread struct {
	ID       string           `json:"id"` // the WhatsApp user's phone number
	Messages []HistoryMessage `json:"messages"`
}

// HistoryMessage is a regular message object plus its last known delivery status.
type HistoryMessage struct {
	IncomingTextMessage
	HistoryContext *struct {
		Status string `json:"status"`
	} `json:"history_context,omitempty"`
}

// UnmarshalJSON decodes the embedded message and history_context separately so
// a custom decoder on IncomingTextMessage can't swallow history_context.
func (m *HistoryMessage) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &m.IncomingTextMessage); err != nil {
		return err
	}
	var extra struct {
		HistoryContext *struct {
			Status string `json:"status"`
		} `json:"history_context,omitempty"`
	}
	if err := json.Unmarshal(b, &extra); err != nil {
		return err
	}
	m.HistoryContext = extra.HistoryContext
	return nil
}

// processContactSyncBatch applies each contact change from an smb_app_state_sync webhook.
func (a *App) processContactSyncBatch(phoneNumberID string, entries []SMBStateSyncEntry) {
	for _, e := range entries {
		if e.Type != "" && e.Type != "contact" {
			continue
		}
		if e.Contact.PhoneNumber == "" {
			continue
		}
		name := strings.TrimSpace(e.Contact.FullName)
		if name == "" {
			name = strings.TrimSpace(e.Contact.FirstName)
		}
		a.processContactSync(phoneNumberID, e.Contact.PhoneNumber, name, e.Action)
	}
}

// processHistoryWebhook imports replayed WhatsApp Business app chat history.
// History messages are stored without triggering chatbots, outbound webhooks
// or live inbox broadcasts, since they describe conversations that already happened.
func (a *App) processHistoryWebhook(phoneNumberID string, chunks []HistoryChunk, mediaMessages []IncomingTextMessage) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("Panic recovered in processHistoryWebhook", "panic", r, "phone_id", phoneNumberID)
		}
	}()

	account, err := a.getWhatsAppAccountCached(phoneNumberID)
	if err != nil {
		a.Log.Error("WhatsApp account not found for history", "phone_id", phoneNumberID, "error", err)
		return
	}

	for _, chunk := range chunks {
		for _, e := range chunk.Errors {
			a.Log.Warn("WhatsApp Business app chat history not shared",
				"phone_id", phoneNumberID, "code", e.Code, "message", e.Message)
		}
		imported := 0
		for _, thread := range chunk.Threads {
			imported += a.importHistoryThread(account, thread)
		}
		if len(chunk.Threads) > 0 {
			a.Log.Info("Imported chat history chunk",
				"phone_id", phoneNumberID,
				"phase", int(chunk.Metadata.Phase),
				"chunk_order", int(chunk.Metadata.ChunkOrder),
				"progress", int(chunk.Metadata.Progress),
				"messages", imported)
		}
	}

	for _, msg := range mediaMessages {
		a.applyHistoryMedia(account, msg)
	}
}

func normalizeWAPhone(p string) string {
	return strings.TrimPrefix(strings.TrimSpace(p), "+")
}

func historyStatus(status string) models.MessageStatus {
	switch strings.ToUpper(status) {
	case "READ", "PLAYED":
		return models.MessageStatusRead
	case "DELIVERED":
		return models.MessageStatusDelivered
	case "ERROR":
		return models.MessageStatusFailed
	case "PENDING":
		return models.MessageStatusPending
	default:
		return models.MessageStatusSent
	}
}

// importHistoryThread stores one thread's messages and returns how many were new.
func (a *App) importHistoryThread(account *models.WhatsAppAccount, thread HistoryThread) int {
	contactPhone := normalizeWAPhone(thread.ID)
	if contactPhone == "" {
		return 0
	}
	contact, _, err := contactutil.GetOrCreateContact(a.DB, account.OrganizationID, contactPhone, "")
	if err != nil {
		a.Log.Error("Failed to get or create contact for history", "phone", contactPhone, "error", err)
		return 0
	}

	ctx := context.Background()
	imported := 0
	var latestAt time.Time
	latestPreview := ""
	for _, hm := range thread.Messages {
		if hm.ID == "" {
			continue
		}
		var existing int64
		a.DB.Model(&models.Message{}).
			Where("organization_id = ? AND whats_app_message_id = ?", account.OrganizationID, hm.ID).
			Count(&existing)
		if existing > 0 {
			continue
		}

		incoming := normalizeWAPhone(hm.From) == contactPhone
		msgType := "text"
		content := historyMediaPlaceholder
		var media *MediaInfo
		if hm.Type != "media_placeholder" {
			extracted := a.extractMessageContent(ctx, hm.IncomingTextMessage, account)
			msgType = extracted.Type
			content = extracted.Text
			media = extracted.Media
		}

		message := models.Message{
			BaseModel:         models.BaseModel{ID: uuid.New()},
			OrganizationID:    account.OrganizationID,
			WhatsAppAccount:   account.Name,
			ContactID:         contact.ID,
			WhatsAppMessageID: hm.ID,
			MessageType:       models.MessageType(msgType),
			Content:           content,
			Metadata:          models.JSONB{"source": "whatsapp_business_app_history"},
		}
		if incoming {
			message.Direction = models.DirectionIncoming
			message.Status = models.MessageStatusReceived
		} else {
			message.Direction = models.DirectionOutgoing
			status := ""
			if hm.HistoryContext != nil {
				status = hm.HistoryContext.Status
			}
			message.Status = historyStatus(status)
		}
		if media != nil {
			message.MediaURL = media.MediaURL
			message.MediaMimeType = media.MediaMimeType
			message.MediaFilename = media.MediaFilename
		}
		if ts, err := strconv.ParseInt(hm.Timestamp, 10, 64); err == nil && ts > 0 {
			at := time.Unix(ts, 0)
			message.CreatedAt = at
			message.UpdatedAt = at
		}

		if err := a.DB.Create(&message).Error; err != nil {
			a.Log.Error("Failed to save history message", "message_id", hm.ID, "error", err)
			continue
		}
		imported++

		if message.CreatedAt.After(latestAt) {
			latestAt = message.CreatedAt
			latestPreview = content
			if msgType != "text" {
				latestPreview = "[" + msgType + "]"
			}
		}
	}

	if imported > 0 && (contact.LastMessageAt == nil || latestAt.After(*contact.LastMessageAt)) {
		if len(latestPreview) > 100 {
			latestPreview = latestPreview[:97] + "..."
		}
		updates := map[string]any{
			"last_message_at":      latestAt,
			"last_message_preview": latestPreview,
		}
		if contact.WhatsAppAccount == "" {
			updates["whats_app_account"] = account.Name
		}
		a.DB.Model(contact).Updates(updates)
	}
	return imported
}

// applyHistoryMedia fills in a media message that the history replay first sent
// as a media_placeholder. Meta only sends these for the last 14 days of media.
func (a *App) applyHistoryMedia(account *models.WhatsAppAccount, msg IncomingTextMessage) {
	if msg.ID == "" {
		return
	}
	var existing models.Message
	if err := a.DB.Where("organization_id = ? AND whats_app_message_id = ?", account.OrganizationID, msg.ID).
		First(&existing).Error; err != nil {
		a.Log.Debug("History media for unknown message, skipping", "message_id", msg.ID)
		return
	}

	extracted := a.extractMessageContent(context.Background(), msg, account)
	updates := map[string]any{
		"message_type": extracted.Type,
		"content":      extracted.Text,
	}
	if extracted.Media != nil {
		updates["media_url"] = extracted.Media.MediaURL
		updates["media_mime_type"] = extracted.Media.MediaMimeType
		updates["media_filename"] = extracted.Media.MediaFilename
	}
	if err := a.DB.Model(&existing).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to apply history media", "message_id", msg.ID, "error", err)
	}
}

// processAccountUpdate marks accounts disconnected when the business unlinks the
// WhatsApp Business app (or Meta offboards it), and active again on reconnect.
func (a *App) processAccountUpdate(wabaID, phoneNumber, event, reason string) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("Panic recovered in processAccountUpdate", "panic", r, "waba_id", wabaID)
		}
	}()

	var status string
	switch event {
	case "PARTNER_REMOVED", "ACCOUNT_OFFBOARDED":
		status = accountStatusDisconnected
	case "ACCOUNT_RECONNECTED":
		status = "active"
	default:
		return
	}
	if wabaID == "" {
		return
	}

	var accounts []models.WhatsAppAccount
	if err := a.DB.Where("business_id = ?", wabaID).Find(&accounts).Error; err != nil {
		a.Log.Error("Failed to load accounts for account_update", "waba_id", wabaID, "error", err)
		return
	}
	for _, acc := range accounts {
		if acc.Status == status {
			continue
		}
		if err := a.DB.Model(&acc).Update("status", status).Error; err != nil {
			a.Log.Error("Failed to update account status", "account_id", acc.ID, "error", err)
			continue
		}
		a.InvalidateWhatsAppAccountCache(acc.PhoneID)
		a.Log.Warn("WhatsApp account status changed by Meta",
			"account", acc.Name, "event", event, "reason", reason,
			"phone_number", phoneNumber, "status", status)
	}
}
