package database

import (
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MigrationModel holds model info for migration progress
type MigrationModel struct {
	Name  string
	Model any
}

// GetMigrationModels returns all models to migrate with their names
func GetMigrationModels() []MigrationModel {
	return []MigrationModel{
		// Core models
		{"Organization", &models.Organization{}},
		{"Permission", &models.Permission{}},
		{"CustomRole", &models.CustomRole{}},
		{"User", &models.User{}},
		{"UserOrganization", &models.UserOrganization{}},
		{"Team", &models.Team{}},
		{"TeamMember", &models.TeamMember{}},
		{"APIKey", &models.APIKey{}},
		{"SSOProvider", &models.SSOProvider{}},
		{"Webhook", &models.Webhook{}},
		{"WebhookDelivery", &models.WebhookDelivery{}},
		{"CustomAction", &models.CustomAction{}},
		{"WhatsAppAccount", &models.WhatsAppAccount{}},
		{"SharedMetaAppCredentials", &models.SharedMetaAppCredentials{}},
		{"Contact", &models.Contact{}},
		{"Tag", &models.Tag{}},
		{"ContactList", &models.ContactList{}},
		{"ContactListMember", &models.ContactListMember{}},
		{"GoogleCredential", &models.GoogleCredential{}},
		{"UserPasskey", &models.UserPasskey{}},
		{"Message", &models.Message{}},
		{"Template", &models.Template{}},
		{"WhatsAppFlow", &models.WhatsAppFlow{}},

		// Bulk & Notifications
		{"BulkMessageCampaign", &models.BulkMessageCampaign{}},
		{"BulkMessageRecipient", &models.BulkMessageRecipient{}},
		{"NotificationRule", &models.NotificationRule{}},

		// Chatbot models
		{"ChatbotSettings", &models.ChatbotSettings{}},
		{"KeywordRule", &models.KeywordRule{}},
		{"ChatbotFlow", &models.ChatbotFlow{}},
		// ChatbotFlowStep table is no longer managed by AutoMigrate — the
		// v2 graph runner uses ChatbotFlow.Graph exclusively. The model
		// type is retained only so BackfillChatbotFlowGraph can read
		// existing rows once during startup, then the table can be
		// dropped in a future maintenance migration.
		{"ChatbotSession", &models.ChatbotSession{}},
		{"ChatbotSessionMessage", &models.ChatbotSessionMessage{}},
		{"AIContext", &models.AIContext{}},
		{"AIUsageLog", &models.AIUsageLog{}},
		{"AgentTransfer", &models.AgentTransfer{}},

		// User tracking
		{"UserAvailabilityLog", &models.UserAvailabilityLog{}},

		// Canned responses
		{"CannedResponse", &models.CannedResponse{}},

		// Catalogs
		{"Catalog", &models.Catalog{}},
		{"CatalogProduct", &models.CatalogProduct{}},

		// Dashboard
		{"Widget", &models.Widget{}},

		// Conversation Notes
		{"ConversationNote", &models.ConversationNote{}},

		// Calling / IVR
		{"CallLog", &models.CallLog{}},
		{"IVRFlow", &models.IVRFlow{}},
		{"CallTransfer", &models.CallTransfer{}},
		{"CallPermission", &models.CallPermission{}},
		{"AuditLog", &models.AuditLog{}},

		// Embedded store (used when no Redis server is configured)
		{"KVEntry", &models.KVEntry{}},
		{"ScheduledItem", &models.ScheduledItem{}},
		{"CampaignJob", &models.CampaignJob{}},
	}
}

// AutoMigrate runs auto migration for all models (silent mode)
func AutoMigrate(db *gorm.DB) error {
	migrationModels := GetMigrationModels()
	for _, m := range migrationModels {
		if err := db.AutoMigrate(m.Model); err != nil {
			return err
		}
	}
	return nil
}

// RunMigrationWithProgress runs migrations with a progress bar display
func RunMigrationWithProgress(db *gorm.DB, adminCfg *config.DefaultAdminConfig) error {
	// Silence GORM logging during migration
	silentDB := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})

	migrationModels := GetMigrationModels()
	indexes := indexesFor(db.Name())

	// Total steps: models + indexes + default admin check
	totalSteps := len(migrationModels) + len(indexes) + 1
	currentStep := 0
	barWidth := 40

	printProgress := func(step int, total int) {
		percent := float64(step) / float64(total)
		filled := int(percent * float64(barWidth))
		empty := barWidth - filled

		bar := repeatChar("█", filled) + "\033[90m" + repeatChar("░", empty) + "\033[0m"
		fmt.Printf("\r  Running migrations  %s %3d%%", bar, int(percent*100))
		_ = os.Stdout.Sync()
	}

	fmt.Println()

	// Migrate models
	for _, m := range migrationModels {
		printProgress(currentStep, totalSteps)
		if err := silentDB.AutoMigrate(m.Model); err != nil {
			fmt.Printf("\n  \033[31m✗ Migration failed: %s\033[0m\n\n", m.Name)
			return fmt.Errorf("failed to migrate %s: %w", m.Name, err)
		}
		currentStep++
	}

	// Create indexes
	for _, idx := range indexes {
		printProgress(currentStep, totalSteps)
		if err := createIndex(silentDB, idx); err != nil {
			fmt.Printf("\n  \033[31m✗ Index creation failed\033[0m\n\n")
			return fmt.Errorf("failed to create index: %w", err)
		}
		currentStep++
	}

	// Seed permissions (always run, will skip if already seeded)
	printProgress(currentStep, totalSteps)
	if err := SeedPermissionsAndRoles(silentDB); err != nil {
		fmt.Printf("\n  \033[31m✗ Failed to seed permissions\033[0m\n\n")
		return err
	}

	// Fix existing organizations - link permissions to system roles if missing
	if err := SeedSystemRolesForAllOrgs(silentDB); err != nil {
		fmt.Printf("\n  \033[31m✗ Failed to fix existing role permissions\033[0m\n\n")
		return err
	}

	// Backfill user_organizations from existing users
	if err := MigrateUserOrganizations(silentDB); err != nil {
		fmt.Printf("\n  \033[31m✗ Failed to backfill user organizations\033[0m\n\n")
		return err
	}

	// Create the first admin (only when there are no users). A nil adminCfg
	// skips it: a restore brings its own users.
	printProgress(currentStep, totalSteps)
	if adminCfg != nil {
		if err := CreateDefaultAdmin(silentDB, adminCfg); err != nil {
			fmt.Printf("\n  \033[31m✗ Setup failed\033[0m\n\n")
			return err
		}
	}
	currentStep++

	// Seed default widgets for all organizations
	printProgress(currentStep, totalSteps)
	if err := SeedDefaultWidgets(silentDB); err != nil {
		fmt.Printf("\n  \033[31m✗ Failed to seed widgets\033[0m\n\n")
		return err
	}

	// Backfill last_inbound_at from existing messages
	if err := BackfillLastInboundAt(silentDB); err != nil {
		fmt.Printf("\n  \033[31m✗ Failed to backfill last_inbound_at\033[0m\n\n")
		return err
	}

	printProgress(currentStep, totalSteps)
	fmt.Printf("\n  \033[32m✓ Migration completed\033[0m\n\n")

	return nil
}

// repeatChar repeats a character n times
func repeatChar(char string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += char
	}
	return result
}

// CreateDefaultAdmin creates the first organization and a super-admin user
// when the database has no users yet.
func CreateDefaultAdmin(db *gorm.DB, cfg *config.DefaultAdminConfig) error {
	var users int64
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		return fmt.Errorf("failed to count users: %w", err)
	}
	if users > 0 {
		return nil
	}

	// Find any existing organization, or create "Default Organization" if none exist
	var org models.Organization
	if err := db.First(&org).Error; err != nil {
		// No organizations exist, create default one
		orgName := cfg.OrganizationName
		if orgName == "" {
			orgName = "Default Organization"
		}
		org = models.Organization{
			BaseModel: models.BaseModel{ID: uuid.New()},
			Name:      orgName,
			Settings:  models.JSONB{},
		}
		if err := db.Create(&org).Error; err != nil {
			return fmt.Errorf("failed to create default organization: %w", err)
		}
	}

	// Hash the default password from config
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(cfg.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Seed permissions if not exist
	if err := SeedPermissionsAndRoles(db); err != nil {
		return fmt.Errorf("failed to seed permissions: %w", err)
	}

	// Seed system roles for this organization if not exist
	if err := SeedSystemRolesForOrg(db, org.ID); err != nil {
		return fmt.Errorf("failed to seed system roles: %w", err)
	}

	// Get admin system role for the organization
	var adminRole models.CustomRole
	if err := db.Where("organization_id = ? AND name = ? AND is_system = ?", org.ID, "admin", true).First(&adminRole).Error; err != nil {
		return fmt.Errorf("failed to find admin role: %w", err)
	}

	// Create default admin user (super admin for cross-organization access)
	admin := models.User{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		Email:          cfg.Email,
		PasswordHash:   string(passwordHash),
		FullName:       cfg.FullName,
		RoleID:         &adminRole.ID,
		IsActive:       true,
		IsAvailable:    true,
		IsSuperAdmin:   true,
		Settings:       models.JSONB{},
	}
	if err := db.Create(&admin).Error; err != nil {
		return fmt.Errorf("failed to create default admin user: %w", err)
	}

	// Create UserOrganization entry for the default admin
	userOrg := models.UserOrganization{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		UserID:         admin.ID,
		OrganizationID: org.ID,
		RoleID:         &adminRole.ID,
		IsDefault:      true,
	}
	if err := db.Create(&userOrg).Error; err != nil {
		return fmt.Errorf("failed to create user organization entry: %w", err)
	}

	return nil
}

// MigrateUserOrganizations backfills user_organizations from existing users
func MigrateUserOrganizations(db *gorm.DB) error {
	var users []models.User
	if err := db.Raw(`
		SELECT u.* FROM users u
		LEFT JOIN user_organizations uo ON uo.user_id = u.id AND uo.organization_id = u.organization_id AND uo.deleted_at IS NULL
		WHERE uo.id IS NULL AND u.deleted_at IS NULL`).Scan(&users).Error; err != nil {
		return err
	}
	for _, u := range users {
		row := models.UserOrganization{UserID: u.ID, OrganizationID: u.OrganizationID, RoleID: u.RoleID, IsDefault: true}
		if err := db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// BackfillLastInboundAt sets last_inbound_at for existing contacts from their
// most recent incoming message. Only updates contacts where the field is NULL.
func BackfillLastInboundAt(db *gorm.DB) error {
	if db.Name() == config.DriverMySQL {
		// MySQL has no UPDATE ... FROM.
		return db.Exec(`
		UPDATE contacts c
		JOIN (
			SELECT contact_id, MAX(created_at) AS max_created
			FROM messages
			WHERE direction = 'incoming' AND deleted_at IS NULL
			GROUP BY contact_id
		) sub ON c.id = sub.contact_id
		SET c.last_inbound_at = sub.max_created
		WHERE c.last_inbound_at IS NULL AND c.deleted_at IS NULL
	`).Error
	}
	return db.Exec(`
		UPDATE contacts AS c
		SET last_inbound_at = sub.max_created
		FROM (
			SELECT contact_id, MAX(created_at) AS max_created
			FROM messages
			WHERE direction = 'incoming' AND deleted_at IS NULL
			GROUP BY contact_id
		) sub
		WHERE c.id = sub.contact_id AND c.last_inbound_at IS NULL AND c.deleted_at IS NULL
	`).Error
}

// SeedPermissionsAndRoles seeds the default permissions and system roles
func SeedPermissionsAndRoles(db *gorm.DB) error {
	// Get all default permissions
	defaultPerms := models.DefaultPermissions()

	// Add any missing permissions
	for _, perm := range defaultPerms {
		var existing models.Permission
		if err := db.Where("resource = ? AND action = ?", perm.Resource, perm.Action).First(&existing).Error; err != nil {
			// Permission doesn't exist, create it
			perm.ID = uuid.New()
			if err := db.Create(&perm).Error; err != nil {
				return fmt.Errorf("failed to create permission %s:%s: %w", perm.Resource, perm.Action, err)
			}
		}
	}

	return nil
}

// SeedSystemRolesForAllOrgs creates system roles for all existing organizations
// This is idempotent - it skips organizations that already have system roles
func SeedSystemRolesForAllOrgs(db *gorm.DB) error {
	var orgs []models.Organization
	if err := db.Find(&orgs).Error; err != nil {
		return fmt.Errorf("failed to fetch organizations: %w", err)
	}

	for _, org := range orgs {
		if err := SeedSystemRolesForOrg(db, org.ID); err != nil {
			return fmt.Errorf("failed to seed roles for org %s: %w", org.ID, err)
		}
	}

	// Fix any system roles that don't have permissions linked
	if err := FixSystemRolePermissions(db); err != nil {
		return fmt.Errorf("failed to fix role permissions: %w", err)
	}

	// Migrate existing users from old role column to new role_id
	if err := MigrateExistingUserRoles(db); err != nil {
		return fmt.Errorf("failed to migrate user roles: %w", err)
	}

	// Make admin@admin.com a super admin if exists
	if err := db.Exec("UPDATE users SET is_super_admin = true WHERE email = 'admin@admin.com'").Error; err != nil {
		return fmt.Errorf("failed to set super admin: %w", err)
	}

	return nil
}

// FixSystemRolePermissions links permissions to existing system roles that have no permissions
func FixSystemRolePermissions(db *gorm.DB) error {
	// Get all permissions from database
	var permissions []models.Permission
	if err := db.Find(&permissions).Error; err != nil {
		return fmt.Errorf("failed to fetch permissions: %w", err)
	}

	if len(permissions) == 0 {
		return nil // No permissions to link
	}

	// Create permission map for lookup
	permMap := make(map[string]models.Permission)
	for _, p := range permissions {
		permMap[p.Resource+":"+p.Action] = p
	}

	// Get system role permission mappings
	rolePermissions := models.SystemRolePermissions()

	// Find system roles without permissions
	var systemRoles []models.CustomRole
	if err := db.Where("is_system = ?", true).Find(&systemRoles).Error; err != nil {
		return fmt.Errorf("failed to fetch system roles: %w", err)
	}

	for _, role := range systemRoles {
		// Check if role has permissions
		var permCount int64
		db.Table("role_permissions").Where("custom_role_id = ?", role.ID).Count(&permCount)

		if permCount > 0 {
			continue // Already has permissions, don't overwrite customizations
		}

		// Get the permission keys for this role
		permKeys, ok := rolePermissions[role.Name]
		if !ok {
			continue // Unknown role name
		}

		// Link permissions to role
		var permsToAdd []models.Permission
		for _, key := range permKeys {
			if perm, ok := permMap[key]; ok {
				permsToAdd = append(permsToAdd, perm)
			}
		}

		if len(permsToAdd) > 0 {
			if err := db.Model(&role).Association("Permissions").Replace(permsToAdd); err != nil {
				return fmt.Errorf("failed to link permissions to role %s: %w", role.Name, err)
			}
		}
	}

	return nil
}

// MigrateExistingUserRoles migrates users from the old role column to the new role_id
// This is safe to run on fresh installs - it will simply do nothing if the column doesn't exist
func MigrateExistingUserRoles(db *gorm.DB) error {
	// Check if the old 'role' column exists in the users table
	if !db.Migrator().HasColumn("users", "role") {
		return nil // Fresh install, no old role column
	}

	// Get users who have old role but no role_id assigned
	type UserWithLegacyRole struct {
		ID             uuid.UUID
		OrganizationID uuid.UUID
		LegacyRole     string
	}

	var usersToMigrate []UserWithLegacyRole
	err := db.Raw(`
		SELECT id, organization_id, role as legacy_role
		FROM users
		WHERE role_id IS NULL AND role IS NOT NULL AND role != ''
	`).Scan(&usersToMigrate).Error
	if err != nil {
		return fmt.Errorf("failed to fetch users with legacy roles: %w", err)
	}

	if len(usersToMigrate) == 0 {
		return nil // No users to migrate
	}

	// Get all system roles grouped by organization
	var systemRoles []models.CustomRole
	if err := db.Where("is_system = ?", true).Find(&systemRoles).Error; err != nil {
		return fmt.Errorf("failed to fetch system roles: %w", err)
	}

	// Create lookup: orgID -> roleName -> roleID
	roleMap := make(map[uuid.UUID]map[string]uuid.UUID)
	for _, role := range systemRoles {
		if roleMap[role.OrganizationID] == nil {
			roleMap[role.OrganizationID] = make(map[string]uuid.UUID)
		}
		roleMap[role.OrganizationID][role.Name] = role.ID
	}

	// Migrate each user
	for _, user := range usersToMigrate {
		orgRoles, ok := roleMap[user.OrganizationID]
		if !ok {
			continue // Organization doesn't have system roles yet
		}

		roleID, ok := orgRoles[user.LegacyRole]
		if !ok {
			continue // Role not found (shouldn't happen for admin/manager/agent)
		}

		// Update user's role_id
		if err := db.Exec("UPDATE users SET role_id = ? WHERE id = ?", roleID, user.ID).Error; err != nil {
			return fmt.Errorf("failed to update user %s role: %w", user.ID, err)
		}
	}

	return nil
}

// SeedSystemRolesForOrg creates system roles for an organization
func SeedSystemRolesForOrg(db *gorm.DB, orgID uuid.UUID) error {
	// Check if system roles exist for this org
	var roleCount int64
	if err := db.Model(&models.CustomRole{}).Where("organization_id = ? AND is_system = ?", orgID, true).Count(&roleCount).Error; err != nil {
		return fmt.Errorf("failed to count roles: %w", err)
	}

	if roleCount > 0 {
		return nil // Already seeded
	}

	// Get all permissions from database
	var permissions []models.Permission
	if err := db.Find(&permissions).Error; err != nil {
		return fmt.Errorf("failed to fetch permissions: %w", err)
	}

	// Create permission map for lookup
	permMap := make(map[string]models.Permission)
	for _, p := range permissions {
		permMap[p.Resource+":"+p.Action] = p
	}

	// Get system role permission mappings
	rolePermissions := models.SystemRolePermissions()

	// Create system roles
	systemRoles := []struct {
		Name        string
		Description string
		IsDefault   bool
	}{
		{"admin", "Full system access", false},
		{"manager", "Manage chatbot, campaigns, and team operations", false},
		{"agent", "Handle customer conversations", true},
	}

	for _, sr := range systemRoles {
		role := models.CustomRole{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: orgID,
			Name:           sr.Name,
			Description:    sr.Description,
			IsSystem:       true,
			IsDefault:      sr.IsDefault,
		}

		// Add permissions
		permKeys := rolePermissions[sr.Name]
		for _, key := range permKeys {
			if perm, ok := permMap[key]; ok {
				role.Permissions = append(role.Permissions, perm)
			}
		}

		if err := db.Create(&role).Error; err != nil {
			return fmt.Errorf("failed to create %s role: %w", sr.Name, err)
		}
	}

	return nil
}

// SeedDefaultWidgets creates default dashboard widgets for all organizations
func SeedDefaultWidgets(db *gorm.DB) error {
	// Find the super admin user (admin@admin.com)
	var superAdmin models.User
	if err := db.Where("email = ?", "admin@admin.com").First(&superAdmin).Error; err != nil {
		// No super admin exists yet, skip widget creation
		return nil
	}

	// Get all organizations
	var orgs []models.Organization
	if err := db.Find(&orgs).Error; err != nil {
		return fmt.Errorf("failed to fetch organizations: %w", err)
	}

	for _, org := range orgs {
		// Skip orgs that already have widgets
		var exists int64
		db.Model(&models.Widget{}).Where("organization_id = ?", org.ID).Count(&exists)
		if exists > 0 {
			continue
		}

		if err := SeedDefaultWidgetsForOrg(db, org.ID, superAdmin.ID); err != nil {
			return err
		}
	}

	return nil
}

// SeedDefaultWidgetsForOrg creates default dashboard widgets for a single organization.
// Used when a new organization is created at runtime.
func SeedDefaultWidgetsForOrg(db *gorm.DB, orgID, userID uuid.UUID) error {
	type defaultWidget struct {
		Name         string
		Description  string
		DataSource   string
		Metric       string
		Field        string
		Filters      models.JSONBArray
		DisplayType  string
		Color        string
		Config       models.JSONB
		ShowChange   bool
		DisplayOrder int
		GridX        int
		GridY        int
		GridW        int
		GridH        int
	}

	defaultWidgetsData := []defaultWidget{
		// Row 0 — core volume
		{"Total Messages", "Total number of messages sent and received", "messages", "count", "", nil, "number", "blue", nil, true, 0, 0, 0, 3, 3},
		{"Active Contacts", "Number of contacts with recent activity", "contacts", "count", "", nil, "number", "green", nil, true, 1, 3, 0, 3, 3},
		{"Chatbot Sessions", "Active chatbot conversation sessions", "sessions", "count", "", nil, "number", "purple", nil, true, 2, 6, 0, 3, 3},
		{"Total Campaigns", "Number of bulk message campaigns", "campaigns", "count", "", nil, "number", "orange", nil, true, 3, 9, 0, 3, 3},

		// Row 1 — ops health
		{"Failed Messages", "Messages that failed to send or deliver", "messages", "count", "", models.JSONBArray{map[string]any{"field": "status", "operator": "equals", "value": "failed"}}, "number", "red", nil, true, 4, 0, 3, 3, 3},
		{"Call Volume", "Voice calls in the selected period", "calls", "count", "", nil, "number", "indigo", nil, true, 5, 3, 3, 3, 3},
		{"AI Token Usage", "Total AI tokens used in the selected period", "ai", "sum", "total_tokens", nil, "number", "purple", nil, true, 6, 6, 3, 3, 3},
		{"Webhooks", "Active outbound webhook configurations", "webhooks", "count", "", models.JSONBArray{map[string]any{"field": "is_active", "operator": "equals", "value": "true"}}, "number", "slate", nil, false, 7, 9, 3, 3, 3},

		// Row 2 — activity + shortcuts
		{"Recent Messages", "Latest conversations from your contacts", "messages", "count", "", nil, "table", "", nil, false, 8, 0, 6, 6, 8},
		{"Quick Actions", "Common tasks and shortcuts", "shortcuts", "count", "", nil, "shortcuts", "", models.JSONB{"shortcuts": []any{"chat", "campaigns", "templates", "chatbot", "agentAnalytics"}}, false, 9, 6, 6, 6, 8},

		// Row 3 — live ops
		{"Live Queue", "Active transfers waiting for an agent", "transfers", "count", "", nil, "queue", "amber", nil, false, 10, 0, 14, 6, 5},
		{"Agent Status", "Agent availability and active chats", "agents", "count", "", nil, "agent_status", "teal", nil, false, 11, 6, 14, 6, 5},

		// Row 4 — alerts + trend
		{"Ops Alerts", "Failed messages and SLA breaches", "messages", "count", "", nil, "alert_list", "rose", nil, false, 12, 0, 19, 6, 5},
		{"Message Trend", "Daily message volume", "messages", "count", "", nil, "sparkline", "blue", nil, true, 13, 6, 19, 6, 4},
	}

	for _, wd := range defaultWidgetsData {
		metric := wd.Metric
		if metric == "" {
			metric = "count"
		}
		displayType := wd.DisplayType
		if displayType == "" {
			displayType = "number"
		}
		filters := wd.Filters
		if filters == nil {
			filters = models.JSONBArray{}
		}
		config := wd.Config
		if config == nil {
			config = models.JSONB{}
		}
		widget := models.Widget{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: orgID,
			UserID:         &userID,
			Name:           wd.Name,
			Description:    wd.Description,
			DataSource:     wd.DataSource,
			Metric:         metric,
			Field:          wd.Field,
			Filters:        filters,
			DisplayType:    displayType,
			ShowChange:     wd.ShowChange,
			Color:          wd.Color,
			Size:           "small",
			Config:         config,
			DisplayOrder:   wd.DisplayOrder,
			GridX:          wd.GridX,
			GridY:          wd.GridY,
			GridW:          wd.GridW,
			GridH:          wd.GridH,
			IsShared:       true,
			IsDefault:      true,
		}
		if err := db.Create(&widget).Error; err != nil {
			return fmt.Errorf("failed to create widget %s: %w", wd.Name, err)
		}
	}

	return nil
}
