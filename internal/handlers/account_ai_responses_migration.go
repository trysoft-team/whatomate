package handlers

import (
	"fmt"
	"strings"

	"github.com/zerodha/logf"
	"gorm.io/gorm"

	"github.com/trysoft/whatomate/internal/models"
)

// BackfillAccountAIResponsesEnabled copies org chatbot_settings.ai_enabled onto
// each WhatsApp account's ai_responses_enabled once per organization, so existing
// installs keep automatic AI replies after the toggle moved to account settings.
// Idempotent via organization.settings.ai_responses_account_migrated.
func BackfillAccountAIResponsesEnabled(db *gorm.DB, lo logf.Logger) error {
	if !db.Migrator().HasColumn(&models.WhatsAppAccount{}, "ai_responses_enabled") {
		return nil
	}

	var orgs []models.Organization
	if err := db.Find(&orgs).Error; err != nil {
		return fmt.Errorf("list orgs for AI responses backfill: %w", err)
	}

	for i := range orgs {
		org := &orgs[i]
		if org.Settings == nil {
			org.Settings = models.JSONB{}
		}
		if v, ok := org.Settings["ai_responses_account_migrated"].(bool); ok && v {
			continue
		}

		var settings []models.ChatbotSettings
		if err := db.Where("organization_id = ?", org.ID).Find(&settings).Error; err != nil {
			return fmt.Errorf("load chatbot settings for org %s: %w", org.ID, err)
		}

		enabledByAccount := map[string]bool{}
		orgWide := false
		for _, s := range settings {
			if !s.AI.Enabled {
				continue
			}
			if strings.TrimSpace(s.WhatsAppAccount) == "" {
				orgWide = true
			} else {
				enabledByAccount[s.WhatsAppAccount] = true
			}
		}

		q := db.Model(&models.WhatsAppAccount{}).Where("organization_id = ?", org.ID)
		var result *gorm.DB
		if orgWide {
			result = q.Update("ai_responses_enabled", true)
		} else if len(enabledByAccount) > 0 {
			names := make([]string, 0, len(enabledByAccount))
			for name := range enabledByAccount {
				names = append(names, name)
			}
			result = q.Where("name IN ?", names).Update("ai_responses_enabled", true)
		}
		if result != nil {
			if result.Error != nil {
				return fmt.Errorf("backfill ai_responses_enabled for org %s: %w", org.ID, result.Error)
			}
			if result.RowsAffected > 0 {
				lo.Info("Backfilled account AI responses flags", "org", org.ID, "rows", result.RowsAffected)
			}
		}

		org.Settings["ai_responses_account_migrated"] = true
		if err := db.Model(org).Update("settings", org.Settings).Error; err != nil {
			return fmt.Errorf("mark AI responses migration for org %s: %w", org.ID, err)
		}
	}
	return nil
}
