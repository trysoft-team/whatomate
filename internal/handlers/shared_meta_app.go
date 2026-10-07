package handlers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// SharedMetaAppResponse is returned by GET /api/admin/meta-app (no secret).
type SharedMetaAppResponse struct {
	AppID        string   `json:"app_id"`
	ConfigID     string   `json:"config_id"`
	ShareMode    string   `json:"share_mode"`
	SharedOrgIDs []string `json:"shared_org_ids"`
	HasAppSecret bool     `json:"has_app_secret"`
	UpdatedByID  *string  `json:"updated_by_id,omitempty"`
}

// GetSharedMetaApp returns the platform shared Meta App credential pool.
// Super admin only.
func (a *App) GetSharedMetaApp(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}

	row, err := a.loadSharedMetaApp()
	if err != nil {
		a.Log.Error("Failed to load shared meta app", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load shared Meta App credentials", nil, "")
	}

	return r.SendEnvelope(sharedMetaAppResponse(row))
}

// UpdateSharedMetaApp upserts the platform shared Meta App credential pool.
// Super admin only.
func (a *App) UpdateSharedMetaApp(r *fastglue.Request) error {
	_, userID, err := a.requireSuperAdmin(r)
	if err != nil {
		return nil
	}

	var req struct {
		AppID        *string   `json:"app_id"`
		ConfigID     *string   `json:"config_id"`
		AppSecret    *string   `json:"app_secret"`
		ShareMode    *string   `json:"share_mode"`
		SharedOrgIDs *[]string `json:"shared_org_ids"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	row, err := a.loadSharedMetaApp()
	if err != nil {
		a.Log.Error("Failed to load shared meta app", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load shared Meta App credentials", nil, "")
	}
	if row == nil {
		row = &models.SharedMetaAppCredentials{
			ShareMode:    models.MetaAppShareOff,
			SharedOrgIDs: models.JSONBArray{},
		}
	}

	if req.AppID != nil {
		row.AppID = strings.TrimSpace(*req.AppID)
	}
	if req.ConfigID != nil {
		row.ConfigID = strings.TrimSpace(*req.ConfigID)
	}
	if req.AppSecret != nil && strings.TrimSpace(*req.AppSecret) != "" {
		enc, errEnc := crypto.Encrypt(strings.TrimSpace(*req.AppSecret), a.Config.App.EncryptionKey)
		if errEnc != nil {
			a.Log.Error("Failed to encrypt shared meta app secret", "error", errEnc)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to encrypt app secret", nil, "")
		}
		row.AppSecretEncrypted = enc
	}
	if req.ShareMode != nil {
		mode := models.NormalizeMetaAppShareMode(*req.ShareMode)
		row.ShareMode = mode
	} else if row.ShareMode == "" {
		row.ShareMode = models.MetaAppShareOff
	}
	if req.SharedOrgIDs != nil {
		arr := models.JSONBArray{}
		for _, id := range *req.SharedOrgIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, err := uuid.Parse(id); err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "shared_org_ids must be valid UUIDs", nil, "")
			}
			arr = append(arr, id)
		}
		row.SharedOrgIDs = arr
	}
	if row.ShareMode == models.MetaAppShareSelected && len(row.SharedOrgIDStrings()) == 0 {
		// Allow empty selected list (inherits nothing until orgs are chosen).
	}

	uid := userID
	row.UpdatedByID = &uid

	if row.ID == uuid.Nil {
		if err := a.DB.Create(row).Error; err != nil {
			a.Log.Error("Failed to create shared meta app", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save shared Meta App credentials", nil, "")
		}
	} else {
		if err := a.DB.Save(row).Error; err != nil {
			a.Log.Error("Failed to update shared meta app", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save shared Meta App credentials", nil, "")
		}
	}

	return r.SendEnvelope(map[string]any{
		"message":  "Shared Meta App credentials updated",
		"settings": sharedMetaAppResponse(row),
	})
}

func (a *App) loadSharedMetaApp() (*models.SharedMetaAppCredentials, error) {
	var row models.SharedMetaAppCredentials
	err := a.DB.Order("created_at ASC").First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func sharedMetaAppResponse(row *models.SharedMetaAppCredentials) SharedMetaAppResponse {
	resp := SharedMetaAppResponse{
		ShareMode:    models.MetaAppShareOff,
		SharedOrgIDs: []string{},
	}
	if row == nil {
		return resp
	}
	resp.AppID = row.AppID
	resp.ConfigID = row.ConfigID
	resp.ShareMode = models.NormalizeMetaAppShareMode(row.ShareMode)
	resp.SharedOrgIDs = row.SharedOrgIDStrings()
	if resp.SharedOrgIDs == nil {
		resp.SharedOrgIDs = []string{}
	}
	resp.HasAppSecret = row.AppSecretEncrypted != ""
	if row.UpdatedByID != nil {
		s := row.UpdatedByID.String()
		resp.UpdatedByID = &s
	}
	return resp
}

// orgHasMetaOverride is true when the org set any private Meta App field.
func orgHasMetaOverride(settings models.JSONB) bool {
	if settings == nil {
		return false
	}
	if v, ok := settings["meta_app_id"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := settings["meta_config_id"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := settings["meta_app_secret_encrypted"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}

// metaSourceForOrg returns org | shared | config for UI hints (no secrets).
func (a *App) metaSourceForOrg(orgID uuid.UUID, settings models.JSONB) string {
	if orgHasMetaOverride(settings) {
		return "org"
	}
	row, err := a.loadSharedMetaApp()
	if err == nil && row != nil && row.AppliesToOrg(orgID) && row.HasCredentials() {
		return "shared"
	}
	return "config"
}
