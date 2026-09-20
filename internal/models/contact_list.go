package models

import "github.com/google/uuid"

// ContactList is an org-scoped phonebook / audience list for chatbot flows
// and (later) campaigns.
type ContactList struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name           string    `gorm:"size:255;not null" json:"name"`
	Description    string    `gorm:"size:500" json:"description,omitempty"`

	Members []ContactListMember `gorm:"foreignKey:ListID" json:"members,omitempty"`
}

func (ContactList) TableName() string {
	return "contact_lists"
}

// ContactListMember is a phone number on a contact list. ContactID is optional
// when the number is not yet a WhatsApp contact in this org.
type ContactListMember struct {
	BaseModel
	ListID         uuid.UUID  `gorm:"type:uuid;index;not null;uniqueIndex:idx_list_phone" json:"list_id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	PhoneNumber    string     `gorm:"size:50;not null;uniqueIndex:idx_list_phone" json:"phone_number"`
	Name           string     `gorm:"size:255" json:"name,omitempty"`
	ContactID      *uuid.UUID `gorm:"type:uuid;index" json:"contact_id,omitempty"`
	Vars           JSONB      `gorm:"type:jsonb;default:'{}'" json:"vars,omitempty"`
}

func (ContactListMember) TableName() string {
	return "contact_list_members"
}
