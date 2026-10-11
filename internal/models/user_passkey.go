package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserPasskey is a WebAuthn credential a user registered to sign in without a
// password. Credential holds the library's credential record as JSON (public
// key, sign count, flags); it contains no secrets.
type UserPasskey struct {
	ID           uuid.UUID  `gorm:"type:uuid;primary_key" json:"id"`
	UserID       uuid.UUID  `gorm:"type:uuid;index;not null" json:"-"`
	Name         string     `gorm:"size:100;not null" json:"name"`
	CredentialID string     `gorm:"size:1400;uniqueIndex;not null" json:"-"` // base64url
	RPID         string     `gorm:"size:255;not null" json:"rp_id"`
	Credential   string     `gorm:"type:text;not null" json:"-"`
	CreatedAt    time.Time  `gorm:"autoCreateTime" json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
}

func (UserPasskey) TableName() string {
	return "user_passkeys"
}

// BeforeCreate assigns a UUID in Go so every database driver gets one.
func (p *UserPasskey) BeforeCreate(*gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}
