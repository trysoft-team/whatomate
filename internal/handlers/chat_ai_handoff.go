package handlers

import (
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/websocket"
)

// handoffSummaryMinMessages skips summaries for chats with nothing to summarize.
const handoffSummaryMinMessages = 2

// queueHandoffSummary summarizes the conversation for the agent picking up a
// transfer, stores it on the transfer and pushes it to connected clients.
// It runs in the background and is a no-op when AI or the setting is off.
func (a *App) queueHandoffSummary(transfer *models.AgentTransfer) {
	if transfer == nil || a.DB == nil {
		return
	}
	t := *transfer
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.generateHandoffSummary(&t)
	}()
}

func (a *App) generateHandoffSummary(transfer *models.AgentTransfer) {
	settings, errMsg := a.chatAISettings(transfer.OrganizationID, transfer.WhatsAppAccount)
	if errMsg != "" || !settings.AI.HandoffSummary {
		return
	}
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", transfer.ContactID, transfer.OrganizationID).First(&contact).Error; err != nil {
		return
	}
	conv, n := a.chatAIConversation(&contact, 60)
	if n < handoffSummaryMinMessages {
		return
	}
	summary, err := a.summarizeConversation(settings, conv, models.AIUsagePurposeHandoffSummary)
	if err != nil {
		a.Log.Warn("handoff summary failed", "error", err, "transfer_id", transfer.ID)
		return
	}
	text := formatHandoffSummary(summary)
	if err := a.DB.Model(&models.AgentTransfer{}).Where("id = ?", transfer.ID).Update("ai_summary", text).Error; err != nil {
		a.Log.Warn("failed to store handoff summary", "error", err, "transfer_id", transfer.ID)
		return
	}
	if a.WSHub != nil {
		a.WSHub.BroadcastToOrg(transfer.OrganizationID, websocket.WSMessage{
			Type: websocket.TypeAgentTransferSummary,
			Payload: map[string]any{
				"id":         transfer.ID.String(),
				"contact_id": transfer.ContactID.String(),
				"ai_summary": text,
			},
		})
	}
}
