package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/websocket"
	"gorm.io/gorm"
)

const (
	// Cache TTLs - 6 hours since these rarely change (invalidated on update anyway)
	settingsCacheTTL        = 6 * time.Hour
	flowsCacheTTL           = 6 * time.Hour
	keywordRulesCacheTTL    = 6 * time.Hour
	whatsappAccountCacheTTL = 6 * time.Hour
	webhooksCacheTTL        = 6 * time.Hour
	slaSettingsCacheTTL     = 6 * time.Hour
	aiContextsCacheTTL      = 6 * time.Hour
	userPermissionsCacheTTL = 6 * time.Hour
	rolePermissionsCacheTTL = 6 * time.Hour
	tagsCacheTTL            = 6 * time.Hour

	// Cache key prefixes
	settingsCachePrefix        = "chatbot:settings:"
	flowsCachePrefix           = "chatbot:flows:"
	keywordRulesCachePrefix    = "chatbot:keywords:"
	whatsappAccountCachePrefix = "whatsapp:account:"
	webhooksCachePrefix        = "webhooks:"
	slaSettingsCacheKey        = "chatbot:sla_enabled_settings"
	aiContextsCachePrefix      = "chatbot:ai_contexts:"
	userPermissionsCachePrefix = "permissions:user:"
	rolePermissionsCachePrefix = "permissions:role:"
	tagsCachePrefix            = "tags:"
)

// chatbotSettingsCache is used for caching since AI.APIKey has json:"-" tag
type chatbotSettingsCache struct {
	models.ChatbotSettings
	AIAPIKey         string `json:"ai_api_key_cache"`
	TranscribeAPIKey string `json:"transcribe_api_key_cache"`
}

// getChatbotSettingsCached retrieves chatbot settings from cache or database
func (a *App) getChatbotSettingsCached(orgID uuid.UUID, whatsAppAccount string) (*models.ChatbotSettings, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s:%s", settingsCachePrefix, orgID.String(), whatsAppAccount)

	var cacheData chatbotSettingsCache
	if a.cacheGetJSON(ctx, cacheKey, &cacheData) {
		// Restore the API key from the cache wrapper
		cacheData.AI.APIKey = cacheData.AIAPIKey
		cacheData.AI.TranscribeAPIKey = cacheData.TranscribeAPIKey
		a.decryptChatbotSettingsSecrets(&cacheData.ChatbotSettings)
		return &cacheData.ChatbotSettings, nil
	}

	// Cache miss - fetch from database
	var settings models.ChatbotSettings
	result := a.DB.Where("organization_id = ? AND (whats_app_account = ? OR whats_app_account = '')",
		orgID, whatsAppAccount).
		Order("CASE WHEN whats_app_account = '' THEN 1 ELSE 0 END"). // Prefer account-specific settings
		First(&settings)

	if result.Error != nil {
		return nil, result.Error
	}

	// Cache the result (include AI APIKey explicitly since it has json:"-" tag)
	a.cacheSetJSON(ctx, cacheKey, chatbotSettingsCache{
		ChatbotSettings:  settings,
		AIAPIKey:         settings.AI.APIKey,
		TranscribeAPIKey: settings.AI.TranscribeAPIKey,
	}, settingsCacheTTL)

	a.decryptChatbotSettingsSecrets(&settings)
	return &settings, nil
}

// decryptChatbotSettingsSecrets decrypts the stored API keys so callers can use
// them directly. The cache keeps the encrypted form.
func (a *App) decryptChatbotSettingsSecrets(s *models.ChatbotSettings) {
	if a.Config == nil || a.Config.App.EncryptionKey == "" {
		return
	}
	crypto.DecryptFields(a.Config.App.EncryptionKey, &s.AI.APIKey, &s.AI.ElevenLabsAPIKey, &s.AI.TranscribeAPIKey)
}

// getChatbotFlowsCached retrieves all enabled flows with steps from cache or database
func (a *App) getChatbotFlowsCached(orgID uuid.UUID) ([]models.ChatbotFlow, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", flowsCachePrefix, orgID.String())

	// Try cache first
	var flows []models.ChatbotFlow
	if a.cacheGetJSON(ctx, cacheKey, &flows) {
		return flows, nil
	}

	// Cache miss - fetch from database
	if err := a.DB.Where("organization_id = ? AND is_enabled = true", orgID).
		Find(&flows).Error; err != nil {
		return nil, err
	}

	// Cache the result
	a.cacheSetJSON(ctx, cacheKey, flows, flowsCacheTTL)

	return flows, nil
}

// getChatbotFlowByIDCached retrieves a specific flow by ID from the cached flows list
func (a *App) getChatbotFlowByIDCached(orgID uuid.UUID, flowID uuid.UUID) (*models.ChatbotFlow, error) {
	flows, err := a.getChatbotFlowsCached(orgID)
	if err != nil {
		return nil, err
	}

	for i := range flows {
		if flows[i].ID == flowID {
			return &flows[i], nil
		}
	}

	return nil, gorm.ErrRecordNotFound
}

// getKeywordRulesCached retrieves keyword rules from cache or database
func (a *App) getKeywordRulesCached(orgID uuid.UUID, whatsAppAccount string) ([]models.KeywordRule, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s:%s", keywordRulesCachePrefix, orgID.String(), whatsAppAccount)

	// Try cache first
	var rules []models.KeywordRule
	if a.cacheGetJSON(ctx, cacheKey, &rules) {
		return rules, nil
	}

	// Cache miss - fetch from database (account-specific + global)
	// Get account-specific rules
	var accountRules []models.KeywordRule
	if err := a.DB.Where("organization_id = ? AND whats_app_account = ? AND is_enabled = true",
		orgID, whatsAppAccount).
		Order("priority DESC").
		Find(&accountRules).Error; err != nil {
		a.Log.Error("Failed to fetch account keyword rules", "error", err, "org_id", orgID)
	}

	// Get global rules (whats_app_account = '')
	var globalRules []models.KeywordRule
	if err := a.DB.Where("organization_id = ? AND whats_app_account = '' AND is_enabled = true",
		orgID).
		Order("priority DESC").
		Find(&globalRules).Error; err != nil {
		a.Log.Error("Failed to fetch global keyword rules", "error", err, "org_id", orgID)
	}

	// Merge: account-specific first, then global
	rules = append(accountRules, globalRules...)

	// Cache the result
	a.cacheSetJSON(ctx, cacheKey, rules, keywordRulesCacheTTL)

	return rules, nil
}

// InvalidateChatbotSettingsCache invalidates the settings cache for an organization
func (a *App) InvalidateChatbotSettingsCache(orgID uuid.UUID) {
	ctx := context.Background()
	a.cacheDelPrefix(ctx, fmt.Sprintf("%s%s:", settingsCachePrefix, orgID.String()))
}

// InvalidateChatbotFlowsCache invalidates the flows cache for an organization
func (a *App) InvalidateChatbotFlowsCache(orgID uuid.UUID) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", flowsCachePrefix, orgID.String())
	a.cacheDel(ctx, cacheKey)
}

// InvalidateKeywordRulesCache invalidates the keyword rules cache for an organization
func (a *App) InvalidateKeywordRulesCache(orgID uuid.UUID) {
	ctx := context.Background()
	a.cacheDelPrefix(ctx, fmt.Sprintf("%s%s:", keywordRulesCachePrefix, orgID.String()))
}

// whatsAppAccountCache is used for caching since AccessToken, AppSecret, and Pin have json:"-" tag
type whatsAppAccountCache struct {
	models.WhatsAppAccount
	AccessToken string `json:"access_token"`
	AppSecret   string `json:"app_secret"`
	Pin         string `json:"pin"`
}

// getWhatsAppAccountCached retrieves WhatsApp account by phone_id from cache or database
func (a *App) getWhatsAppAccountCached(phoneID string) (*models.WhatsAppAccount, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", whatsappAccountCachePrefix, phoneID)

	// Try cache first
	var cacheData whatsAppAccountCache
	if a.cacheGetJSON(ctx, cacheKey, &cacheData) {
		cacheData.WhatsAppAccount.AccessToken = cacheData.AccessToken
		cacheData.WhatsAppAccount.AppSecret = cacheData.AppSecret
		cacheData.WhatsAppAccount.Pin = cacheData.Pin
		a.decryptAccountSecrets(&cacheData.WhatsAppAccount)
		return &cacheData.WhatsAppAccount, nil
	}

	// Cache miss - fetch from database
	var account models.WhatsAppAccount
	if err := a.DB.Where("phone_id = ?", phoneID).First(&account).Error; err != nil {
		return nil, err
	}

	// Cache the result (include AccessToken, AppSecret, and Pin explicitly since they have json:"-")
	a.cacheSetJSON(ctx, cacheKey, whatsAppAccountCache{
		WhatsAppAccount: account,
		AccessToken:     account.AccessToken,
		AppSecret:       account.AppSecret,
		Pin:             account.Pin,
	}, whatsappAccountCacheTTL)

	// Decrypt secrets before returning
	a.decryptAccountSecrets(&account)
	return &account, nil
}

// decryptAccountSecrets decrypts the encrypted secrets on a WhatsApp account.
// Handles both encrypted ("enc:" prefixed) and legacy unencrypted values transparently.
func (a *App) decryptAccountSecrets(account *models.WhatsAppAccount) {
	account.DecryptSecrets(a.Config.App.EncryptionKey)
}

// InvalidateWhatsAppAccountCache invalidates the WhatsApp account cache
func (a *App) InvalidateWhatsAppAccountCache(phoneID string) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", whatsappAccountCachePrefix, phoneID)
	a.cacheDel(ctx, cacheKey)
}

// getWebhooksCached retrieves active webhooks for an organization from cache or database
func (a *App) getWebhooksCached(orgID uuid.UUID) ([]models.Webhook, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", webhooksCachePrefix, orgID.String())

	// Try cache first
	var webhooks []models.Webhook
	if a.cacheGetJSON(ctx, cacheKey, &webhooks) {
		return webhooks, nil
	}

	// Cache miss - fetch from database
	if err := a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).Find(&webhooks).Error; err != nil {
		return nil, err
	}

	// Cache the result
	a.cacheSetJSON(ctx, cacheKey, webhooks, webhooksCacheTTL)

	return webhooks, nil
}

// InvalidateWebhooksCache invalidates the webhooks cache for an organization
func (a *App) InvalidateWebhooksCache(orgID uuid.UUID) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", webhooksCachePrefix, orgID.String())
	a.cacheDel(ctx, cacheKey)
}

// getSLAEnabledSettingsCached retrieves all SLA-enabled chatbot settings from cache or database
func (a *App) getSLAEnabledSettingsCached() ([]models.ChatbotSettings, error) {
	ctx := context.Background()

	// Try cache first
	var settings []models.ChatbotSettings
	if a.cacheGetJSON(ctx, slaSettingsCacheKey, &settings) {
		return settings, nil
	}

	// Cache miss - fetch from database
	if err := a.DB.Where("sla_enabled = ?", true).Find(&settings).Error; err != nil {
		return nil, err
	}

	// Cache the result
	a.cacheSetJSON(ctx, slaSettingsCacheKey, settings, slaSettingsCacheTTL)

	return settings, nil
}

// InvalidateSLASettingsCache invalidates the SLA settings cache
func (a *App) InvalidateSLASettingsCache() {
	ctx := context.Background()
	a.cacheDel(ctx, slaSettingsCacheKey)
}

// getAIContextsCached retrieves AI contexts from cache or database
func (a *App) getAIContextsCached(orgID uuid.UUID, whatsAppAccount string) ([]models.AIContext, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s:%s", aiContextsCachePrefix, orgID.String(), whatsAppAccount)

	// Try cache first
	var contexts []models.AIContext
	if a.cacheGetJSON(ctx, cacheKey, &contexts) {
		return contexts, nil
	}

	// Cache miss - fetch from database (account-specific + global)
	// Get account-specific contexts
	var accountContexts []models.AIContext
	if err := a.DB.Where("organization_id = ? AND whats_app_account = ? AND is_enabled = true",
		orgID, whatsAppAccount).
		Order("priority DESC").
		Find(&accountContexts).Error; err != nil {
		a.Log.Error("Failed to fetch account AI contexts", "error", err, "org_id", orgID)
	}

	// Get global contexts (whats_app_account = '')
	var globalContexts []models.AIContext
	if err := a.DB.Where("organization_id = ? AND whats_app_account = '' AND is_enabled = true",
		orgID).
		Order("priority DESC").
		Find(&globalContexts).Error; err != nil {
		a.Log.Error("Failed to fetch global AI contexts", "error", err, "org_id", orgID)
	}

	// Merge: account-specific first, then global
	contexts = append(accountContexts, globalContexts...)

	// Cache the result
	a.cacheSetJSON(ctx, cacheKey, contexts, aiContextsCacheTTL)

	return contexts, nil
}

// InvalidateAIContextsCache invalidates the AI contexts cache for an organization
func (a *App) InvalidateAIContextsCache(orgID uuid.UUID) {
	ctx := context.Background()
	a.cacheDelPrefix(ctx, fmt.Sprintf("%s%s:", aiContextsCachePrefix, orgID.String()))
}

// UserPermissions represents cached user permissions
type UserPermissions struct {
	RoleID       uuid.UUID `json:"role_id"`
	RoleName     string    `json:"role_name"`
	IsSystem     bool      `json:"is_system"`
	IsSuperAdmin bool      `json:"is_super_admin"`
	Permissions  []string  `json:"permissions"` // Format: "resource:action"
}

// getUserPermissionsCached retrieves user permissions from cache or database.
// When orgID is provided, it looks up the user's role from user_organizations for that org.
// When orgID is not provided, it falls back to the user's default RoleID.
func (a *App) getUserPermissionsCached(userID uuid.UUID, orgIDs ...uuid.UUID) (*UserPermissions, error) {
	ctx := context.Background()

	// Determine cache key based on whether orgID is provided
	var cacheKey string
	var orgID uuid.UUID
	if len(orgIDs) > 0 && orgIDs[0] != uuid.Nil {
		orgID = orgIDs[0]
		cacheKey = fmt.Sprintf("%s%s:%s", userPermissionsCachePrefix, userID.String(), orgID.String())
	} else {
		cacheKey = fmt.Sprintf("%s%s", userPermissionsCachePrefix, userID.String())
	}

	// Try cache first
	var perms UserPermissions
	if a.cacheGetJSON(ctx, cacheKey, &perms) {
		return &perms, nil
	}

	// Cache miss - fetch from database
	var user models.User
	if err := a.DB.Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}

	// Determine which role to use. For a specific org the role has to come from
	// that org's membership row — falling back to users.role_id would carry the
	// user's home-org role (possibly admin) into a tenant they only belong to as
	// a plain member. The fallback survives only for the user's own org, where
	// legacy rows may predate user_organizations.
	var roleID *uuid.UUID
	if orgID != uuid.Nil {
		var userOrg models.UserOrganization
		if err := a.DB.Where("user_id = ? AND organization_id = ?", userID, orgID).First(&userOrg).Error; err == nil {
			roleID = userOrg.RoleID
		}
		if roleID == nil && orgID == user.OrganizationID {
			roleID = user.RoleID
		}
	} else {
		roleID = user.RoleID
	}

	if roleID == nil {
		if !user.IsSuperAdmin {
			return nil, gorm.ErrRecordNotFound
		}
		// Super admin in an org they hold no membership in: the flag alone grants
		// access (HasPermission short-circuits on it), with no role-derived perms.
		perms = UserPermissions{IsSuperAdmin: true, Permissions: []string{}}
		a.cacheSetJSON(ctx, cacheKey, perms, userPermissionsCacheTTL)
		return &perms, nil
	}

	// Fetch role and load permissions via JOIN
	var role models.CustomRole
	if err := a.DB.Where("id = ?", roleID).First(&role).Error; err != nil {
		return nil, err
	}
	if err := a.loadRolePermissions(&role); err != nil {
		return nil, err
	}

	// Build permissions list
	perms = UserPermissions{
		RoleID:       role.ID,
		RoleName:     role.Name,
		IsSystem:     role.IsSystem,
		IsSuperAdmin: user.IsSuperAdmin,
		Permissions:  make([]string, 0, len(role.Permissions)),
	}

	for _, p := range role.Permissions {
		perms.Permissions = append(perms.Permissions, p.Resource+":"+p.Action)
	}

	// Cache the result
	a.cacheSetJSON(ctx, cacheKey, perms, userPermissionsCacheTTL)

	return &perms, nil
}

// HasPermission checks if a user has a specific permission.
// Super admins have all permissions automatically.
// Optional orgIDs parameter allows checking permissions for a specific org.
func (a *App) HasPermission(userID uuid.UUID, resource, action string, orgIDs ...uuid.UUID) bool {
	perms, err := a.getUserPermissionsCached(userID, orgIDs...)
	if err != nil {
		a.Log.Error("Failed to get user permissions", "error", err, "user_id", userID)
		return false
	}

	// Super admins have all permissions
	if perms.IsSuperAdmin {
		return true
	}

	permKey := resource + ":" + action
	for _, p := range perms.Permissions {
		if p == permKey {
			return true
		}
	}

	return false
}

// HasAnyPermission checks if a user has any of the specified permissions.
// Super admins have all permissions automatically.
// To check in a specific org, use HasAnyPermissionInOrg instead.
func (a *App) HasAnyPermission(userID uuid.UUID, permissions ...string) bool {
	perms, err := a.getUserPermissionsCached(userID)
	if err != nil {
		a.Log.Error("Failed to get user permissions", "error", err, "user_id", userID)
		return false
	}

	// Super admins have all permissions
	if perms.IsSuperAdmin {
		return true
	}

	permSet := make(map[string]bool)
	for _, p := range perms.Permissions {
		permSet[p] = true
	}

	for _, p := range permissions {
		if permSet[p] {
			return true
		}
	}

	return false
}

// IsSuperAdmin checks if a user is a super admin
func (a *App) IsSuperAdmin(userID uuid.UUID) bool {
	perms, err := a.getUserPermissionsCached(userID)
	if err != nil {
		return false
	}
	return perms.IsSuperAdmin
}

// ScopedQuery returns a gorm query scoped to the organization
// Always filters by organization - uuid.Nil is not allowed
func (a *App) ScopedQuery(userID, orgID uuid.UUID) *gorm.DB {
	return a.DB.Where("organization_id = ?", orgID)
}

// ScopeToOrg adds organization scoping to an existing query
// Always filters by organization - uuid.Nil is not allowed
func (a *App) ScopeToOrg(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	return query.Where("organization_id = ?", orgID)
}

// GetRolePermissionsCached retrieves role permissions from cache or database
func (a *App) GetRolePermissionsCached(roleID uuid.UUID) ([]string, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", rolePermissionsCachePrefix, roleID.String())

	// Try cache first
	var perms []string
	if a.cacheGetJSON(ctx, cacheKey, &perms) {
		return perms, nil
	}

	// Cache miss - fetch from database via JOIN
	var role models.CustomRole
	if err := a.DB.Where("id = ?", roleID).First(&role).Error; err != nil {
		return nil, err
	}
	if err := a.loadRolePermissions(&role); err != nil {
		return nil, err
	}

	// Build permissions list
	perms = make([]string, 0, len(role.Permissions))
	for _, p := range role.Permissions {
		perms = append(perms, p.Resource+":"+p.Action)
	}

	// Cache the result
	a.cacheSetJSON(ctx, cacheKey, perms, rolePermissionsCacheTTL)

	return perms, nil
}

// InvalidateUserPermissionsCache invalidates the permissions cache for a user
func (a *App) InvalidateUserPermissionsCache(userID uuid.UUID) {
	ctx := context.Background()
	base := fmt.Sprintf("%s%s", userPermissionsCachePrefix, userID.String())
	a.cacheDel(ctx, base)
	a.cacheDelPrefix(ctx, base+":")

	// Notify user via WebSocket to refresh their permissions
	a.notifyUserPermissionsChanged(userID)
}

// InvalidateRolePermissionsCache invalidates the permissions cache for a role and all users with that role
func (a *App) InvalidateRolePermissionsCache(roleID uuid.UUID) {
	ctx := context.Background()

	// Delete role cache
	roleCacheKey := fmt.Sprintf("%s%s", rolePermissionsCachePrefix, roleID.String())
	a.cacheDel(ctx, roleCacheKey)

	// Collect all user IDs that have this role (deduplicated)
	userIDs := make(map[uuid.UUID]bool)

	// Users with this role as their default role
	var users []models.User
	if err := a.DB.Select("id").Where("role_id = ?", roleID).Find(&users).Error; err != nil {
		a.Log.Error("Failed to find users for role permission cache invalidation", "error", err, "role_id", roleID)
	}
	for _, u := range users {
		userIDs[u.ID] = true
	}

	// Users with this role via org-specific assignment (user_organizations)
	var orgUserIDs []uuid.UUID
	if err := a.DB.Table("user_organizations").
		Select("user_id").
		Where("role_id = ? AND deleted_at IS NULL", roleID).
		Pluck("user_id", &orgUserIDs).Error; err != nil {
		a.Log.Error("Failed to find org users for role permission cache invalidation", "error", err, "role_id", roleID)
	}
	for _, uid := range orgUserIDs {
		userIDs[uid] = true
	}

	// Invalidate cache for all affected users (both base and org-specific keys)
	for uid := range userIDs {
		a.InvalidateUserPermissionsCache(uid)
	}
}

// InvalidateOrgPermissionsCache invalidates all permission caches for an organization
func (a *App) InvalidateOrgPermissionsCache(orgID uuid.UUID) {
	// Find all roles in this org
	var roles []models.CustomRole
	if err := a.DB.Select("id").Where("organization_id = ?", orgID).Find(&roles).Error; err != nil {
		a.Log.Error("Failed to find roles for org permission cache invalidation", "error", err, "org_id", orgID)
		return
	}

	for _, role := range roles {
		a.InvalidateRolePermissionsCache(role.ID)
	}
}

// notifyUserPermissionsChanged sends a WebSocket message to a user to refresh their permissions
func (a *App) notifyUserPermissionsChanged(userID uuid.UUID) {
	if a.WSHub == nil {
		return
	}

	// Get user's organization ID for the broadcast
	var user models.User
	if err := a.DB.Select("organization_id").Where("id = ?", userID).First(&user).Error; err != nil {
		a.Log.Error("Failed to find user for permissions notification", "error", err, "user_id", userID)
		return
	}

	a.WSHub.BroadcastToUser(user.OrganizationID, userID, websocket.WSMessage{
		Type:    websocket.TypePermissionsUpdated,
		Payload: map[string]string{"message": "Your permissions have been updated"},
	})
}

// getTagsCached retrieves tags for an organization from cache or database
func (a *App) getTagsCached(orgID uuid.UUID) ([]models.Tag, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", tagsCachePrefix, orgID.String())

	// Try cache first
	var tags []models.Tag
	if a.cacheGetJSON(ctx, cacheKey, &tags) {
		return tags, nil
	}

	// Cache miss - fetch from database
	if err := a.DB.Where("organization_id = ?", orgID).Order("name ASC").Find(&tags).Error; err != nil {
		return nil, err
	}

	// Cache the result
	a.cacheSetJSON(ctx, cacheKey, tags, tagsCacheTTL)

	return tags, nil
}

// InvalidateTagsCache invalidates the tags cache for an organization
func (a *App) InvalidateTagsCache(orgID uuid.UUID) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s%s", tagsCachePrefix, orgID.String())
	a.cacheDel(ctx, cacheKey)
}
