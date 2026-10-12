package handlers

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
)

// metaErrorServiceWindowClosed is Meta's "Re-engagement message" error: more
// than 24 hours have passed since the customer last wrote, so only templates
// can reach them.
const metaErrorServiceWindowClosed = 131047

// agentSessionEndReasonWindowClosed tells the UI why the session ended.
const agentSessionEndReasonWindowClosed = "service_window_closed"

// isServiceWindowClosedSendError reports whether a send failed because the
// 24-hour window is closed (see whatsapp.ParseMetaAPIError for the format).
func isServiceWindowClosedSendError(err error) bool {
	return err != nil && strings.Contains(err.Error(), fmt.Sprintf("API error %d:", metaErrorServiceWindowClosed))
}

func hasServiceWindowClosedStatusError(errs []WebhookStatusError) bool {
	for _, e := range errs {
		if e.Code == metaErrorServiceWindowClosed {
			return true
		}
	}
	return false
}

// releaseContactForClosedWindow ends the agent session and hands active
// transfers back to the chatbot, since the agent can no longer reply.
func (a *App) releaseContactForClosedWindow(orgID, contactID uuid.UUID) {
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).First(&contact).Error; err != nil {
		return
	}

	if _, muted := contact.Metadata["bot_muted_until"]; muted {
		if err := a.clearChatbotAgentSessionMuteWithReason(&contact, orgID, agentSessionEndReasonWindowClosed); err != nil {
			a.Log.Error("Failed to close agent session after 24h window closed", "error", err, "contact_id", contactID)
		} else {
			a.Log.Info("Agent session closed: 24h window closed", "contact_id", contactID)
		}
	}

	var transfers []models.AgentTransfer
	if err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
		orgID, contactID, models.TransferStatusActive).Find(&transfers).Error; err != nil {
		a.Log.Error("Failed to load transfers after 24h window closed", "error", err, "contact_id", contactID)
		return
	}
	for i := range transfers {
		if err := a.resumeAgentTransfer(&transfers[i], nil); err != nil {
			a.Log.Error("Failed to resume transfer after 24h window closed", "error", err, "transfer_id", transfers[i].ID)
			continue
		}
		a.Log.Info("Transfer resumed: 24h window closed", "transfer_id", transfers[i].ID, "contact_id", contactID)
	}
}
