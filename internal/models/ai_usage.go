package models

import "github.com/google/uuid"

// AI usage purposes
const (
	AIUsagePurposeChatbotReply   = "chatbot_reply"
	AIUsagePurposeFlowGenerate   = "flow_generate"
	AIUsagePurposeAIComplete     = "ai_complete"
	AIUsagePurposeWidgetGenerate = "widget_generate"
	AIUsagePurposeChatAssist     = "chat_assist"
	AIUsagePurposeHandoffSummary = "handoff_summary"
	AIUsagePurposeTranscribe     = "transcribe"

	AIUsagePurposeCustomActionGenerate   = "custom_action_generate"
	AIUsagePurposeCannedResponseGenerate = "canned_response_generate"
	AIUsagePurposeTemplateGenerate       = "template_generate"
)

// AIUsageLog records token consumption from provider API calls.
type AIUsageLog struct {
	BaseModel
	OrganizationID   uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	WhatsAppAccount  string     `gorm:"size:100;index" json:"whatsapp_account"`
	Provider         string     `gorm:"size:20;index;not null" json:"provider"` // openai, anthropic, google
	Model            string     `gorm:"size:100;index" json:"model"`
	Purpose          string     `gorm:"size:40;index;not null" json:"purpose"` // chatbot_reply, flow_generate, …
	PromptTokens     int        `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int        `gorm:"not null;default:0" json:"completion_tokens"`
	TotalTokens      int        `gorm:"not null;default:0;index" json:"total_tokens"`
	SessionID        *uuid.UUID `gorm:"type:uuid;index" json:"session_id,omitempty"`
	ContactID        *uuid.UUID `gorm:"type:uuid;index" json:"contact_id,omitempty"`

	Organization *Organization   `gorm:"foreignKey:OrganizationID" json:"organization,omitempty"`
	Session      *ChatbotSession `gorm:"foreignKey:SessionID" json:"session,omitempty"`
	Contact      *Contact        `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
}

func (AIUsageLog) TableName() string {
	return "ai_usage_logs"
}
