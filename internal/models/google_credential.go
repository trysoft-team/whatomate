package models

import "github.com/google/uuid"

// GoogleCredential stores an encrypted Google service-account JSON for an org.
// The plaintext JSON is never returned by the API — only label + has_json.
type GoogleCredential struct {
	BaseModel
	OrganizationID   uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	Label            string    `gorm:"size:255;not null" json:"label"`
	EncryptedJSON    string    `gorm:"type:text;not null" json:"-"`
	ClientEmail      string    `gorm:"size:255" json:"client_email,omitempty"`
}

func (GoogleCredential) TableName() string {
	return "google_credentials"
}
