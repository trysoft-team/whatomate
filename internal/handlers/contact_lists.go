package handlers

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/crypto"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type contactListResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	MemberCount int64     `json:"member_count"`
}

// ListContactLists returns org contact lists.
func (a *App) ListContactLists(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	var lists []models.ContactList
	if err := a.DB.Where("organization_id = ?", orgID).Order("name asc").Find(&lists).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list contact lists", nil, "")
	}

	out := make([]contactListResponse, 0, len(lists))
	for _, list := range lists {
		var count int64
		a.DB.Model(&models.ContactListMember{}).Where("list_id = ?", list.ID).Count(&count)
		out = append(out, contactListResponse{
			ID: list.ID, Name: list.Name, Description: list.Description, MemberCount: count,
		})
	}
	return r.SendEnvelope(out)
}

// CreateContactList creates a named list.
func (a *App) CreateContactList(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil || strings.TrimSpace(req.Name) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}
	list := models.ContactList{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		Name:           strings.TrimSpace(req.Name),
		Description:    req.Description,
	}
	if err := a.DB.Create(&list).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create list", nil, "")
	}
	return r.SendEnvelope(contactListResponse{ID: list.ID, Name: list.Name, Description: list.Description})
}

// UpdateContactList renames a list.
func (a *App) UpdateContactList(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := uuid.Parse(r.RequestCtx.UserValue("id").(string))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid id", nil, "")
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid body", nil, "")
	}
	var list models.ContactList
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&list).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "List not found", nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load list", nil, "")
	}
	updates := map[string]any{}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if len(updates) > 0 {
		if err := a.DB.Model(&list).Updates(updates).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update list", nil, "")
		}
	}
	return r.SendEnvelope(map[string]any{"id": list.ID, "ok": true})
}

// DeleteContactList removes a list and its members.
func (a *App) DeleteContactList(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := uuid.Parse(r.RequestCtx.UserValue("id").(string))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid id", nil, "")
	}
	tx := a.DB.Begin()
	if err := tx.Where("list_id = ? AND organization_id = ?", id, orgID).Delete(&models.ContactListMember{}).Error; err != nil {
		tx.Rollback()
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete members", nil, "")
	}
	res := tx.Where("id = ? AND organization_id = ?", id, orgID).Delete(&models.ContactList{})
	if res.Error != nil {
		tx.Rollback()
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete list", nil, "")
	}
	if res.RowsAffected == 0 {
		tx.Rollback()
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "List not found", nil, "")
	}
	tx.Commit()
	return r.SendEnvelope(map[string]any{"ok": true})
}

// --- Google credentials ---

type googleCredResponse struct {
	ID          uuid.UUID `json:"id"`
	Label       string    `json:"label"`
	ClientEmail string    `json:"client_email,omitempty"`
	HasJSON     bool      `json:"has_json"`
	AuthType    string    `json:"auth_type"`
	Scopes      []string  `json:"scopes,omitempty"`
}

func toGoogleCredResponse(c models.GoogleCredential) googleCredResponse {
	authType := c.AuthType
	if authType == "" {
		authType = models.GoogleAuthServiceAccount
	}
	return googleCredResponse{
		ID:          c.ID,
		Label:       c.Label,
		ClientEmail: c.ClientEmail,
		HasJSON:     c.EncryptedJSON != "",
		AuthType:    authType,
		Scopes:      strings.Fields(c.Scopes),
	}
}

// ListGoogleCredentials lists org Google service-account credentials (no secrets).
func (a *App) ListGoogleCredentials(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	var creds []models.GoogleCredential
	if err := a.DB.Where("organization_id = ?", orgID).Order("label asc").Find(&creds).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list credentials", nil, "")
	}
	out := make([]googleCredResponse, 0, len(creds))
	for _, c := range creds {
		out = append(out, toGoogleCredResponse(c))
	}
	return r.SendEnvelope(out)
}

// CreateGoogleCredential stores an encrypted service-account JSON.
func (a *App) CreateGoogleCredential(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	var req struct {
		Label       string `json:"label"`
		ServiceJSON string `json:"service_json"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid body", nil, "")
	}
	if strings.TrimSpace(req.Label) == "" || strings.TrimSpace(req.ServiceJSON) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "label and service_json are required", nil, "")
	}
	var parsed struct {
		ClientEmail string `json:"client_email"`
		Type        string `json:"type"`
	}
	if err := json.Unmarshal([]byte(req.ServiceJSON), &parsed); err != nil || parsed.Type != "service_account" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "service_json must be a Google service account JSON", nil, "")
	}
	enc, err := crypto.Encrypt(req.ServiceJSON, a.Config.App.EncryptionKey)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to encrypt credential", nil, "")
	}
	cred := models.GoogleCredential{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		Label:          strings.TrimSpace(req.Label),
		EncryptedJSON:  enc,
		ClientEmail:    parsed.ClientEmail,
		AuthType:       models.GoogleAuthServiceAccount,
	}
	if err := a.DB.Create(&cred).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save credential", nil, "")
	}
	return r.SendEnvelope(toGoogleCredResponse(cred))
}

// DeleteGoogleCredential removes a stored credential.
func (a *App) DeleteGoogleCredential(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := uuid.Parse(r.RequestCtx.UserValue("id").(string))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid id", nil, "")
	}
	res := a.DB.Where("id = ? AND organization_id = ?", id, orgID).Delete(&models.GoogleCredential{})
	if res.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete", nil, "")
	}
	if res.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Not found", nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}
