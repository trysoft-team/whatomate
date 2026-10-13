package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
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
