package database_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

// cleanAll truncates every table so each test starts with a blank slate.
func cleanAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	testutil.TruncateTables(db)
}

// --- SeedPermissionsAndRoles ---

func TestSeedPermissionsAndRoles_CreatesAllDefaultPermissions(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	err := database.SeedPermissionsAndRoles(db)
	require.NoError(t, err)

	var count int64
	db.Model(&models.Permission{}).Count(&count)

	expected := len(models.DefaultPermissions())
	assert.Equal(t, int64(expected), count, "all default permissions should be created")
}

func TestSeedPermissionsAndRoles_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	// Seed twice
	require.NoError(t, database.SeedPermissionsAndRoles(db))
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	var count int64
	db.Model(&models.Permission{}).Count(&count)

	expected := len(models.DefaultPermissions())
	assert.Equal(t, int64(expected), count, "idempotent: count should remain the same after two seeds")
}

func TestSeedPermissionsAndRoles_PermissionsHaveResourceAndAction(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	var perms []models.Permission
	db.Find(&perms)

	for _, p := range perms {
		assert.NotEmpty(t, p.Resource, "permission resource must not be empty")
		assert.NotEmpty(t, p.Action, "permission action must not be empty")
		assert.NotEqual(t, uuid.Nil, p.ID, "permission ID must be set")
	}
}

// --- SeedSystemRolesForOrg ---

func TestSeedSystemRolesForOrg_CreatesThreeSystemRoles(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	// Need permissions first
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Test Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)

	err := database.SeedSystemRolesForOrg(db, org.ID)
	require.NoError(t, err)

	var roles []models.CustomRole
	db.Where("organization_id = ? AND is_system = ?", org.ID, true).Find(&roles)
	assert.Len(t, roles, 3, "should create admin, manager, agent roles")

	names := make(map[string]bool)
	for _, r := range roles {
		names[r.Name] = true
	}
	assert.True(t, names["admin"], "admin role should exist")
	assert.True(t, names["manager"], "manager role should exist")
	assert.True(t, names["agent"], "agent role should exist")
}

func TestSeedSystemRolesForOrg_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Idempotent Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)

	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))
	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))

	var count int64
	db.Model(&models.CustomRole{}).Where("organization_id = ? AND is_system = ?", org.ID, true).Count(&count)
	assert.Equal(t, int64(3), count, "idempotent: still exactly 3 system roles")
}

func TestSeedSystemRolesForOrg_AgentIsDefault(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Default Role Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)
	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))

	var agentRole models.CustomRole
	err := db.Where("organization_id = ? AND name = ? AND is_system = ?", org.ID, "agent", true).First(&agentRole).Error
	require.NoError(t, err)
	assert.True(t, agentRole.IsDefault, "agent role should be the default role")
}

func TestSeedSystemRolesForOrg_AdminRoleHasAllPermissions(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Admin Perms Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)
	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))

	var adminRole models.CustomRole
	err := db.Where("organization_id = ? AND name = ? AND is_system = ?", org.ID, "admin", true).First(&adminRole).Error
	require.NoError(t, err)

	// Load permissions through the association
	var perms []models.Permission
	err = db.Model(&adminRole).Association("Permissions").Find(&perms)
	require.NoError(t, err)

	totalPerms := len(models.DefaultPermissions())
	assert.Equal(t, totalPerms, len(perms), "admin role should have all permissions")
}

// --- CreateDefaultAdmin ---

func TestCreateDefaultAdmin_CreatesOrgAndUser(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	cfg := &config.DefaultAdminConfig{
		Email:    "test-admin@example.com",
		Password: "testpassword123",
		FullName: "Test Admin",
	}

	err := database.CreateDefaultAdmin(db, cfg)
	require.NoError(t, err)

	// Verify user was created
	var user models.User
	err = db.Where("email = ?", cfg.Email).First(&user).Error
	require.NoError(t, err)
	assert.Equal(t, cfg.FullName, user.FullName)
	assert.True(t, user.IsActive)
	assert.True(t, user.IsSuperAdmin)
	assert.NotEmpty(t, user.PasswordHash)

	// Verify an organization was created
	var org models.Organization
	err = db.First(&org).Error
	require.NoError(t, err)
	assert.Equal(t, "Default Organization", org.Name)

	// Verify the user belongs to the organization
	assert.Equal(t, org.ID, user.OrganizationID)
}

func TestCreateDefaultAdmin_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	cfg := &config.DefaultAdminConfig{
		Email:    "idempotent-admin@example.com",
		Password: "pass123",
		FullName: "Idempotent Admin",
	}

	require.NoError(t, database.CreateDefaultAdmin(db, cfg))
	require.NoError(t, database.CreateDefaultAdmin(db, cfg))

	var count int64
	db.Model(&models.User{}).Where("email = ?", cfg.Email).Count(&count)
	assert.Equal(t, int64(1), count, "should not create duplicate admin")
}

func TestCreateDefaultAdmin_UsesExistingOrg(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	// Pre-create an organization
	existingOrg := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Pre-existing Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&existingOrg).Error)

	cfg := &config.DefaultAdminConfig{
		Email:    "admin-existing-org@example.com",
		Password: "password",
		FullName: "Admin",
	}

	err := database.CreateDefaultAdmin(db, cfg)
	require.NoError(t, err)

	var user models.User
	err = db.Where("email = ?", cfg.Email).First(&user).Error
	require.NoError(t, err)
	assert.Equal(t, existingOrg.ID, user.OrganizationID, "admin should belong to existing org")

	// Should not have created a new org
	var orgCount int64
	db.Model(&models.Organization{}).Count(&orgCount)
	assert.Equal(t, int64(1), orgCount, "should reuse existing organization")
}

// --- MigrateUserOrganizations ---

func TestMigrateUserOrganizations_BackfillsMissingRowOnce(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	org := models.Organization{Name: "Backfill Org", Slug: "backfill-org", Settings: models.JSONB{}}
	require.NoError(t, db.Create(&org).Error)
	role := models.CustomRole{OrganizationID: org.ID, Name: "agent"}
	require.NoError(t, db.Create(&role).Error)
	user := models.User{
		OrganizationID: org.ID,
		Email:          "backfill@example.com",
		FullName:       "Backfill",
		PasswordHash:   "x",
		RoleID:         &role.ID,
	}
	require.NoError(t, db.Create(&user).Error)
	var before int64
	require.NoError(t, db.Model(&models.UserOrganization{}).Where("user_id = ?", user.ID).Count(&before).Error)
	require.Zero(t, before)

	require.NoError(t, database.MigrateUserOrganizations(db))
	require.NoError(t, database.MigrateUserOrganizations(db))

	var rows []models.UserOrganization
	require.NoError(t, db.Where("user_id = ?", user.ID).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.NotEqual(t, uuid.Nil, rows[0].ID)
	assert.Equal(t, org.ID, rows[0].OrganizationID)
	require.NotNil(t, rows[0].RoleID)
	assert.Equal(t, role.ID, *rows[0].RoleID)
	assert.True(t, rows[0].IsDefault)
}

// --- SeedDefaultWidgetsForOrg ---

func TestSeedDefaultWidgetsForOrg_CreatesFullDefaultSet(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	orgID := uuid.New()
	userID := uuid.New()
	require.NoError(t, db.Create(&models.Organization{
		BaseModel: models.BaseModel{ID: orgID},
		Name:      "Widget Seed Org",
		Settings:  models.JSONB{},
	}).Error)
	require.NoError(t, db.Create(&models.User{
		BaseModel:      models.BaseModel{ID: userID},
		OrganizationID: orgID,
		Email:          "widget-seed@example.com",
		FullName:       "Widget Seeder",
		PasswordHash:   "x",
	}).Error)

	require.NoError(t, database.SeedDefaultWidgetsForOrg(db, orgID, userID))

	var widgets []models.Widget
	require.NoError(t, db.Where("organization_id = ?", orgID).Order("display_order").Find(&widgets).Error)
	require.Len(t, widgets, 14)

	byName := make(map[string]models.Widget, len(widgets))
	for _, w := range widgets {
		byName[w.Name] = w
		assert.True(t, w.IsDefault, "widget %s should be default", w.Name)
		assert.True(t, w.IsShared, "widget %s should be shared", w.Name)
	}

	assert.Equal(t, "messages", byName["Total Messages"].DataSource)
	assert.Equal(t, "number", byName["Total Messages"].DisplayType)
	assert.Equal(t, "failed", filterValue(byName["Failed Messages"], "status"))
	assert.Equal(t, "sum", byName["AI Token Usage"].Metric)
	assert.Equal(t, "total_tokens", byName["AI Token Usage"].Field)
	assert.Equal(t, "calls", byName["Call Volume"].DataSource)
	assert.Equal(t, "webhooks", byName["Webhooks"].DataSource)
	assert.Equal(t, "queue", byName["Live Queue"].DisplayType)
	assert.Equal(t, "agent_status", byName["Agent Status"].DisplayType)
	assert.Equal(t, "alert_list", byName["Ops Alerts"].DisplayType)
	assert.Equal(t, "sparkline", byName["Message Trend"].DisplayType)
	assert.Equal(t, "shortcuts", byName["Quick Actions"].DisplayType)
	assert.Equal(t, "table", byName["Recent Messages"].DisplayType)
}

func filterValue(w models.Widget, field string) string {
	for _, raw := range w.Filters {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["field"] == field {
			if v, ok := m["value"].(string); ok {
				return v
			}
		}
	}
	return ""
}
