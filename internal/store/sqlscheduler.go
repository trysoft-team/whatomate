package store

import (
	"context"
	"time"

	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SQLScheduler is a Scheduler stored in the scheduled_items table.
type SQLScheduler struct{ db *gorm.DB }

func NewSQLScheduler(db *gorm.DB) *SQLScheduler { return &SQLScheduler{db: db} }

func (s *SQLScheduler) Schedule(ctx context.Context, set, item string, at time.Time) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "set_name"}, {Name: "item"}},
		DoUpdates: clause.AssignmentColumns([]string{"due_at"}),
	}).Create(&models.ScheduledItem{SetName: set, Item: item, DueAt: at}).Error
}

func (s *SQLScheduler) Cancel(ctx context.Context, set string, items ...string) error {
	if len(items) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("set_name = ? AND item IN ?", set, items).
		Delete(&models.ScheduledItem{}).Error
}

func (s *SQLScheduler) PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	var due []models.ScheduledItem
	if err := s.db.WithContext(ctx).
		Where("set_name = ? AND due_at <= ?", set, now).
		Order("due_at, item").Limit(limit).
		Find(&due).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(due))
	for _, d := range due {
		// Delete first so concurrent pollers don't both take the item; the
		// due_at condition skips items rescheduled into the future meanwhile.
		res := s.db.WithContext(ctx).
			Where("set_name = ? AND item = ? AND due_at <= ?", set, d.Item, now).
			Delete(&models.ScheduledItem{})
		if res.Error != nil {
			// Claimed items are already deleted; returning an error would lose
			// them. Rows not yet deleted stay for the next poll.
			if len(out) > 0 {
				return out, nil
			}
			return nil, res.Error
		}
		if res.RowsAffected == 1 {
			out = append(out, d.Item)
		}
	}
	return out, nil
}
