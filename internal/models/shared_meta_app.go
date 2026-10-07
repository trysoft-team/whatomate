package models

import (
	"github.com/google/uuid"
)

// Shared Meta App pool share modes.
const (
	MetaAppShareOff      = "off"
	MetaAppShareGlobal   = "global"
	MetaAppShareSelected = "selected"
)

// SharedMetaAppCredentials is the platform-level Meta App credential pool.
// Super admins manage a single row; orgs without a private override inherit
// when share_mode is global or the org is listed in SharedOrgIDs.
type SharedMetaAppCredentials struct {
	BaseModel
	AppID              string     `gorm:"size:128" json:"app_id"`
	ConfigID           string     `gorm:"size:128" json:"config_id"`
	AppSecretEncrypted string     `gorm:"type:text" json:"-"`
	ShareMode          string     `gorm:"size:32;not null;default:'off'" json:"share_mode"` // off | global | selected
	SharedOrgIDs       JSONBArray `gorm:"type:jsonb;default:'[]'" json:"shared_org_ids"`
	UpdatedByID        *uuid.UUID `gorm:"type:uuid" json:"updated_by_id,omitempty"`
}

func (SharedMetaAppCredentials) TableName() string {
	return "shared_meta_app_credentials"
}

// NormalizeMetaAppShareMode returns a valid share mode or "off".
func NormalizeMetaAppShareMode(s string) string {
	switch s {
	case MetaAppShareGlobal, MetaAppShareSelected, MetaAppShareOff:
		return s
	default:
		return MetaAppShareOff
	}
}

// SharedOrgIDStrings extracts UUID strings from SharedOrgIDs JSONB.
func (s *SharedMetaAppCredentials) SharedOrgIDStrings() []string {
	if s == nil || len(s.SharedOrgIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.SharedOrgIDs))
	for _, v := range s.SharedOrgIDs {
		switch t := v.(type) {
		case string:
			if t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// AppliesToOrg reports whether this shared pool should be used for orgID.
func (s *SharedMetaAppCredentials) AppliesToOrg(orgID uuid.UUID) bool {
	if s == nil {
		return false
	}
	switch NormalizeMetaAppShareMode(s.ShareMode) {
	case MetaAppShareGlobal:
		return true
	case MetaAppShareSelected:
		id := orgID.String()
		for _, oid := range s.SharedOrgIDStrings() {
			if oid == id {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// HasCredentials is true when at least one credential field is set.
func (s *SharedMetaAppCredentials) HasCredentials() bool {
	if s == nil {
		return false
	}
	return s.AppID != "" || s.ConfigID != "" || s.AppSecretEncrypted != ""
}
