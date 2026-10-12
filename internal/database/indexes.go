package database

import (
	"fmt"

	"github.com/trysoft/whatomate/internal/config"
	"gorm.io/gorm"
)

type index struct {
	name, table, columns string
	unique               bool
	where                string // partial-index predicate
}

// indexStmt is one statement to run. name is the index (or, when isColumn,
// the generated column) it creates, so MySQL can skip existing ones; it is
// empty for the PostgreSQL ALTERs.
type indexStmt struct {
	name, table, sql string
	isColumn         bool
}

var indexDefs = []index{
	{name: "idx_messages_contact_created", table: "messages", columns: "contact_id, created_at DESC"},
	{name: "idx_messages_conversation", table: "messages", columns: "conversation_id"},
	{name: "idx_contacts_org_phone", table: "contacts", columns: "organization_id, phone_number", unique: true},
	{name: "idx_contacts_assigned_read", table: "contacts", columns: "assigned_user_id, is_read"},
	{name: "idx_sessions_phone_status", table: "chatbot_sessions", columns: "organization_id, phone_number, status"},
	{name: "idx_keyword_rules_priority", table: "keyword_rules", columns: "organization_id, is_enabled, priority DESC"},
	{name: "idx_agent_transfers_active", table: "agent_transfers", columns: "organization_id, phone_number, status"},
	{name: "idx_agent_transfers_org_contact", table: "agent_transfers", columns: "organization_id, contact_id, status"},
	{name: "idx_agent_transfers_agent_active", table: "agent_transfers", columns: "agent_id, status", where: "status = 'active'"},
	{name: "idx_agent_transfers_team", table: "agent_transfers", columns: "team_id, status", where: "team_id IS NOT NULL"},
	{name: "idx_whatsapp_accounts_org_phone", table: "whatsapp_accounts", columns: "organization_id, phone_id", unique: true},
	{name: "idx_templates_account_name_lang", table: "templates", columns: "whats_app_account, name, language", unique: true},
	{name: "idx_keyword_rules_account", table: "keyword_rules", columns: "whats_app_account, is_enabled, priority DESC"},
	{name: "idx_chatbot_flows_account", table: "chatbot_flows", columns: "whats_app_account, is_enabled"},
	{name: "idx_ai_contexts_account", table: "ai_contexts", columns: "whats_app_account, is_enabled, priority DESC"},
	{name: "idx_bulk_campaigns_account", table: "bulk_message_campaigns", columns: "whats_app_account, status"},
	{name: "idx_notification_rules_account", table: "notification_rules", columns: "whats_app_account, is_enabled"},
	{name: "idx_webhook_deliveries_created", table: "webhook_deliveries", columns: "created_at"},
	{name: "idx_messages_account", table: "messages", columns: "whats_app_account, created_at DESC"},
	{name: "idx_contacts_account", table: "contacts", columns: "whats_app_account"},
	{name: "idx_canned_responses_org_name", table: "canned_responses", columns: "organization_id, name", unique: true},
	{name: "idx_canned_responses_active", table: "canned_responses", columns: "organization_id, is_active, usage_count DESC"},
	{name: "idx_webhooks_org_active", table: "webhooks", columns: "organization_id, is_active"},
	{name: "idx_availability_logs_user_time", table: "user_availability_logs", columns: "user_id, started_at DESC"},
	{name: "idx_availability_logs_org_time", table: "user_availability_logs", columns: "organization_id, started_at DESC"},
	{name: "idx_sso_providers_org_provider", table: "sso_providers", columns: "organization_id, provider", unique: true},
	// Teams indexes
	{name: "idx_teams_org_active", table: "teams", columns: "organization_id, is_active"},
	// Create partial unique index (soft-deleted members)
	{name: "idx_team_members_unique", table: "team_members", columns: "team_id, user_id", unique: true, where: "deleted_at IS NULL"},
	{name: "idx_team_members_user", table: "team_members", columns: "user_id"},
	// Custom roles indexes
	{name: "idx_custom_roles_org_name", table: "custom_roles", columns: "organization_id, name", unique: true},
	{name: "idx_custom_roles_org_system", table: "custom_roles", columns: "organization_id, is_system"},
	{name: "idx_custom_roles_org_default", table: "custom_roles", columns: "organization_id, is_default", where: "is_default = true"},
	// User organizations
	{name: "idx_user_org_unique", table: "user_organizations", columns: "user_id, organization_id", unique: true, where: "deleted_at IS NULL"},
	// Conversation notes
	{name: "idx_conversation_notes_contact", table: "conversation_notes", columns: "organization_id, contact_id, created_at DESC"},
	// Call logs
	{name: "idx_call_logs_org_status", table: "call_logs", columns: "organization_id, status, created_at DESC"},
	{name: "idx_call_logs_contact", table: "call_logs", columns: "contact_id, created_at DESC"},
	{name: "idx_call_logs_wa_call_id", table: "call_logs", columns: "whatsapp_call_id", where: "whatsapp_call_id != ''"},
	// IVR flows
	{name: "idx_ivr_flows_org_active", table: "ivr_flows", columns: "organization_id, whatsapp_account, is_active"},
	{name: "idx_ivr_flows_org_call_start", table: "ivr_flows", columns: "organization_id, whatsapp_account", unique: true, where: "is_call_start = true AND is_active = true AND deleted_at IS NULL"},
}

func (i index) render(driver string) []indexStmt {
	kind := "INDEX"
	if i.unique {
		kind = "UNIQUE INDEX"
	}
	if driver != config.DriverMySQL {
		sql := fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s(%s)", kind, i.name, i.table, i.columns)
		if i.where != "" {
			sql += " WHERE " + i.where
		}
		return []indexStmt{{name: i.name, table: i.table, sql: sql}}
	}
	if i.where == "" || !i.unique {
		return []indexStmt{{name: i.name, table: i.table, sql: fmt.Sprintf("CREATE %s %s ON %s(%s)", kind, i.name, i.table, i.columns)}}
	}
	// MySQL and MariaDB have no partial indexes. Rows outside the predicate
	// hold NULL in this column and never conflict.
	col := i.name + "_active"
	return []indexStmt{
		{name: col, table: i.table, isColumn: true, sql: fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TINYINT AS (CASE WHEN %s THEN 1 END) VIRTUAL", i.table, col, i.where)},
		{name: i.name, table: i.table, sql: fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s(%s, %s)", i.name, i.table, i.columns, col)},
	}
}

// indexesFor returns the index creation statements for driver.
func indexesFor(driver string) []indexStmt {
	var stmts []indexStmt
	if driver == config.DriverPostgres {
		// Expand phone_number columns to support group JIDs (e.g., 120363422675615917@g.us)
		for _, table := range []string{"contacts", "chatbot_sessions", "agent_transfers", "bulk_message_recipients"} {
			stmts = append(stmts, indexStmt{table: table, sql: "ALTER TABLE " + table + " ALTER COLUMN phone_number TYPE varchar(50)"})
		}
	}
	for _, i := range indexDefs {
		stmts = append(stmts, i.render(driver)...)
	}
	if driver == config.DriverPostgres {
		// GIN index for JSONB tag filtering
		stmts = append(stmts, indexStmt{name: "idx_contacts_tags", table: "contacts", sql: `CREATE INDEX IF NOT EXISTS idx_contacts_tags ON contacts USING GIN (tags)`})
	}
	return stmts
}

// createIndex runs s. MySQL has no IF NOT EXISTS for indexes or columns, so
// there it skips what already exists.
func createIndex(db *gorm.DB, s indexStmt) error {
	if db.Name() == config.DriverMySQL {
		query := `SELECT count(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`
		if s.isColumn {
			query = `SELECT count(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`
		}
		var n int64
		if err := db.Raw(query, s.table, s.name).Row().Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	return db.Exec(s.sql).Error
}

// CreateIndexes creates additional indexes not handled by GORM tags
func CreateIndexes(db *gorm.DB) error {
	for _, s := range indexesFor(db.Name()) {
		if err := createIndex(db, s); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}
	return nil
}
