package models

import "github.com/google/uuid"

// Google credential auth types.
const (
	GoogleAuthServiceAccount = "service_account"
	GoogleAuthOAuth          = "oauth"
)

// GoogleCredential stores an encrypted Google credential for an org: either a
// service-account JSON or an "authorized_user" JSON from Connect Google account.
// The plaintext JSON is never returned by the API — only label + has_json.
type GoogleCredential struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	Label          string    `gorm:"size:255;not null" json:"label"`
	EncryptedJSON  string    `gorm:"type:text;not null" json:"-"`
	ClientEmail    string    `gorm:"size:255" json:"client_email,omitempty"`
	AuthType       string    `gorm:"size:20;not null;default:'service_account'" json:"auth_type"`
	// Scopes is the space-separated list of scopes the user granted (OAuth only).
	Scopes string `gorm:"type:text" json:"scopes,omitempty"`
}

func (GoogleCredential) TableName() string {
	return "google_credentials"
}
