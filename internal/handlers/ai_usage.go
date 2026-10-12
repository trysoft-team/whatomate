package handlers

import (
	"encoding/json"

	"github.com/trysoft/whatomate/internal/models"
)

// aiUsageContext carries attribution for a single provider call.
type aiUsageContext struct {
	Settings *models.ChatbotSettings
	Session  *models.ChatbotSession
	Purpose  string
}

func (a *App) recordAIUsage(ctx aiUsageContext, provider, model string, promptTokens, completionTokens, totalTokens int) {
	if a == nil || a.DB == nil || ctx.Settings == nil {
		return
	}
	if totalTokens <= 0 && promptTokens <= 0 && completionTokens <= 0 {
		return
	}
	if totalTokens <= 0 {
		totalTokens = promptTokens + completionTokens
	}
	purpose := ctx.Purpose
	if purpose == "" {
		purpose = models.AIUsagePurposeAIComplete
	}
	log := models.AIUsageLog{
		OrganizationID:   ctx.Settings.OrganizationID,
		WhatsAppAccount:  ctx.Settings.WhatsAppAccount,
		Provider:         provider,
		Model:            model,
		Purpose:          purpose,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
	if ctx.Session != nil {
		sid := ctx.Session.ID
		log.SessionID = &sid
		cid := ctx.Session.ContactID
		log.ContactID = &cid
		if log.WhatsAppAccount == "" {
			log.WhatsAppAccount = ctx.Session.WhatsAppAccount
		}
	}
	if err := a.DB.Create(&log).Error; err != nil {
		a.Log.Error("Failed to record AI usage", "error", err, "provider", provider, "purpose", purpose)
		return
	}
	a.NotifyWidgetLiveUpdate(ctx.Settings.OrganizationID, "ai")
}

func parseOpenAIUsage(body []byte) (prompt, completion, total int) {
	var result struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &result) != nil {
		return 0, 0, 0
	}
	return result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens
}

func parseAnthropicUsage(body []byte) (prompt, completion, total int) {
	var result struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &result) != nil {
		return 0, 0, 0
	}
	prompt = result.Usage.InputTokens
	completion = result.Usage.OutputTokens
	return prompt, completion, prompt + completion
}

func parseGoogleUsage(body []byte) (prompt, completion, total int) {
	var result struct {
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(body, &result) != nil {
		return 0, 0, 0
	}
	prompt = result.UsageMetadata.PromptTokenCount
	completion = result.UsageMetadata.CandidatesTokenCount
	total = result.UsageMetadata.TotalTokenCount
	if total <= 0 {
		total = prompt + completion
	}
	return prompt, completion, total
}
