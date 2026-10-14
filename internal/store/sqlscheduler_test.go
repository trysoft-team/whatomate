package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
	"gorm.io/gorm"
)

func TestSQLScheduler(t *testing.T) {
	storetest.RunScheduler(t, func(t *testing.T) store.Scheduler { return store.NewSQLScheduler(cleanDB(t)) })
}

func TestSQLScheduler_MixedTimeZones(t *testing.T) {
	s := store.NewSQLScheduler(cleanDB(t))
	ctx := context.Background()
	west := time.FixedZone("west", -10*3600)
	east := time.FixedZone("east", 10*3600)
	now := time.Now()

	require.NoError(t, s.Schedule(ctx, "tz", "future", now.Add(time.Hour).In(west)))
	require.NoError(t, s.Schedule(ctx, "tz", "late", now.Add(-time.Minute).In(east)))
	require.NoError(t, s.Schedule(ctx, "tz", "early", now.Add(-2*time.Minute).In(west)))

	got, err := s.PopDue(ctx, "tz", now.In(east), 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"early", "late"}, got)
}

func TestSQLScheduler_PopDueKeepsClaimedItemsOnError(t *testing.T) {
	db := cleanDB(t)
	s := store.NewSQLScheduler(db)
	now := time.Now()
	for _, item := range []string{"a", "b", "c"} {
		require.NoError(t, s.Schedule(context.Background(), "set", item, now.Add(-time.Minute)))
	}

	// Cancel the context once the first item has been claimed, so the next
	// DELETE fails.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const cb = "test:cancel_after_first_claim"
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Context == ctx && tx.RowsAffected == 1 {
			cancel()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(cb) })

	got, err := s.PopDue(ctx, "set", now, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, got)

	rest, err := s.PopDue(context.Background(), "set", now, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "c"}, rest)
}

func TestSQLScheduler_PopDueErrorWhenNothingClaimed(t *testing.T) {
	s := store.NewSQLScheduler(cleanDB(t))
	now := time.Now()
	require.NoError(t, s.Schedule(context.Background(), "set", "a", now.Add(-time.Minute)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := s.PopDue(ctx, "set", now, 50)
	assert.Error(t, err)
	assert.Empty(t, got)

	got, err = s.PopDue(context.Background(), "set", now, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, got)
}
