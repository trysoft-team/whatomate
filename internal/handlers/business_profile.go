package handlers

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// businessProfileHTTPTimeout bounds calls to Meta's profile endpoints. We use a
// detached context (not r.RequestCtx) so a client disconnect mid-call doesn't
// cancel an in-flight Meta update — and so that fasthttp.RequestCtx, which has
// no ctx.Done() implementation outside the server, doesn't crash the http client.
const businessProfileHTTPTimeout = 30 * time.Second

// GetBusinessProfile returns the business profile for a WhatsApp account
func (a *App) GetBusinessProfile(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}

	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessProfileHTTPTimeout)
	defer cancel()

	profile, err := a.WhatsApp.GetBusinessProfile(ctx, a.toWhatsAppAccount(account))
	if err != nil {
		a.Log.Error("Failed to get business profile", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to get business profile", nil, "")
	}

	return r.SendEnvelope(profile)
}

// UpdateBusinessProfile updates the business profile for a WhatsApp account
func (a *App) UpdateBusinessProfile(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}

	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}

	var input whatsapp.BusinessProfileInput
	if err := a.decodeRequest(r, &input); err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessProfileHTTPTimeout)
	defer cancel()
	waAccount := a.toWhatsAppAccount(account)

	if err := a.WhatsApp.UpdateBusinessProfile(ctx, waAccount, input); err != nil {
		a.Log.Error("Failed to update business profile", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update business profile", nil, "")
	}

	// Re-fetch to ensure we have the latest state
	profile, err := a.WhatsApp.GetBusinessProfile(ctx, waAccount)
	if err != nil {
		// If re-fetch fails, just return success message
		return r.SendEnvelope(map[string]string{"message": "Profile updated successfully"})
	}

	return r.SendEnvelope(profile)
}

// displayNameMaxRunes is a sanity cap; Meta's guidelines decide what's acceptable.
const displayNameMaxRunes = 100

type displayNameResponse struct {
	whatsapp.DisplayNameStatus
	// CanReregister is false for coexistence numbers, which are registered
	// through the WhatsApp Business app rather than the registration API.
	CanReregister bool `json:"can_reregister"`
	HasStoredPin  bool `json:"has_stored_pin"`
}

func (a *App) displayNameStatusResponse(ctx context.Context, account *models.WhatsAppAccount) (*displayNameResponse, error) {
	status, err := a.WhatsApp.GetDisplayNameStatus(ctx, a.toWhatsAppAccount(account))
	if err != nil {
		return nil, err
	}
	return &displayNameResponse{
		DisplayNameStatus: *status,
		CanReregister:     !account.IsSMB,
		HasStoredPin:      account.Pin != "",
	}, nil
}

// GetDisplayName returns the account's live display name and any pending change.
func (a *App) GetDisplayName(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionRead)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessProfileHTTPTimeout)
	defer cancel()
	resp, err := a.displayNameStatusResponse(ctx, account)
	if err != nil {
		a.Log.Error("Failed to get display name", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, err.Error(), nil, "")
	}
	return r.SendEnvelope(resp)
}

// RequestDisplayNameChange submits a new display name for Meta's review.
func (a *App) RequestDisplayNameChange(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	name := strings.TrimSpace(req.DisplayName)
	if name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Display name is required", nil, "")
	}
	if utf8.RuneCountInString(name) > displayNameMaxRunes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Display name is too long", nil, "")
	}
	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessProfileHTTPTimeout)
	defer cancel()
	if err := a.WhatsApp.RequestDisplayNameChange(ctx, a.toWhatsAppAccount(account), name); err != nil {
		a.Log.Error("Failed to request display name change", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	a.logAudit(orgID, userID, "account", account.ID, models.AuditActionUpdated,
		map[string]any{"display_name_request": nil}, map[string]any{"display_name_request": name})

	resp, err := a.displayNameStatusResponse(ctx, account)
	if err != nil {
		return r.SendEnvelope(map[string]any{"new_display_name": name, "new_name_status": "PENDING_REVIEW"})
	}
	return r.SendEnvelope(resp)
}

// ApplyDisplayName re-registers the phone number so an approved display name
// takes effect. Meta ignores re-registration before the name is approved.
func (a *App) ApplyDisplayName(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	var req struct {
		Pin string `json:"pin"`
	}
	_ = r.Decode(&req, "json")
	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}
	if account.IsSMB {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"Coexistence numbers are registered through the WhatsApp Business app and can't be re-registered here", nil, "")
	}

	pin := strings.TrimSpace(req.Pin)
	if pin == "" {
		pin = account.Pin
	}
	if len(pin) != 6 || strings.Trim(pin, "0123456789") != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"Enter the number's 6-digit two-step verification PIN", nil, "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessProfileHTTPTimeout)
	defer cancel()
	if err := a.WhatsApp.RegisterPhoneNumber(ctx, account.PhoneID, pin, account.AccessToken, account.APIVersion); err != nil {
		a.Log.Error("Re-registration for display name failed", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}

	if pin != account.Pin {
		account.Pin = pin
		if err := a.encryptAccountSecrets(account); err != nil {
			a.Log.Error("Failed to encrypt account secrets", "error", err)
		} else if err := a.DB.Model(account).Update("pin", account.Pin).Error; err != nil {
			a.Log.Error("Failed to store PIN", "error", err)
		}
		a.InvalidateWhatsAppAccountCache(account.PhoneID)
	}
	a.logAudit(orgID, userID, "account", account.ID, models.AuditActionUpdated,
		nil, map[string]any{"display_name_applied": true})

	return r.SendEnvelope(map[string]any{"success": true})
}

// UpdateProfilePicture handles the profile picture upload
func (a *App) UpdateProfilePicture(r *fastglue.Request) error {
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}

	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}

	// 1. Get the file from request
	fileHeader, err := r.RequestCtx.FormFile("file")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Missing file", nil, "")
	}

	// 2. Open and read file
	file, err := fileHeader.Open()
	if err != nil {
		a.Log.Error("Failed to open file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to open file", nil, "")
	}
	defer file.Close() //nolint:errcheck

	fileSize := fileHeader.Size
	fileContent := make([]byte, fileSize)
	_, err = file.Read(fileContent)
	if err != nil {
		a.Log.Error("Failed to read file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read file", nil, "")
	}

	// Use a longer timeout for upload — profile pictures can be a few MB.
	ctx, cancel := context.WithTimeout(context.Background(), 2*businessProfileHTTPTimeout)
	defer cancel()
	waAccount := a.toWhatsAppAccount(account)

	// Upload to Meta to get handle
	handle, err := a.WhatsApp.UploadProfilePicture(ctx, waAccount, fileContent, fileHeader.Header.Get("Content-Type"))
	if err != nil {
		a.Log.Error("Failed to upload profile picture", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to upload profile picture", nil, "")
	}

	// Update Business Profile with the handle
	input := whatsapp.BusinessProfileInput{
		MessagingProduct:     "whatsapp",
		ProfilePictureHandle: handle,
	}

	err = a.WhatsApp.UpdateBusinessProfile(ctx, waAccount, input)

	if err != nil {
		a.Log.Error("Failed to update profile request", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Uploaded but failed to set profile picture", nil, "")
	}

	return r.SendEnvelope(map[string]string{
		"message": "Profile picture updated successfully",
		"handle":  handle,
	})
}
