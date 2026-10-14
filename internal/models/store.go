package models

import "time"

// KVEntry is a durable key/value row used by the embedded store.
type KVEntry struct {
	Key       string     `gorm:"column:kv_key;primaryKey;size:255"`
	Value     []byte     `gorm:"column:kv_value"`
	ExpiresAt *time.Time `gorm:"index"`
}

func (KVEntry) TableName() string { return "kv_entries" }

// ScheduledItem is an item due at a time (chatbot delays, agent session timeouts).
type ScheduledItem struct {
	SetName string    `gorm:"primaryKey;size:64"`
	Item    string    `gorm:"primaryKey;size:255"`
	DueAt   time.Time `gorm:"index;not null"`
}

func (ScheduledItem) TableName() string { return "scheduled_items" }

// CampaignJob is a queued campaign job in the database queue.
type CampaignJob struct {
	ID          int64      `gorm:"primaryKey;autoIncrement"`
	JobType     string     `gorm:"size:32;not null"`
	Payload     string     `gorm:"type:text;not null"`
	LockedBy    string     `gorm:"size:255"`
	LockedUntil *time.Time `gorm:"index"`
	Attempts    int        `gorm:"not null;default:0"`
	CreatedAt   time.Time
}

func (CampaignJob) TableName() string { return "campaign_jobs" }
