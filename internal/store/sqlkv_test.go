package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

func cleanDB(t *testing.T) *gorm.DB {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.Exec("DELETE FROM kv_entries").Error)
	require.NoError(t, db.Exec("DELETE FROM scheduled_items").Error)
	require.NoError(t, db.Exec("DELETE FROM campaign_jobs").Error)
	return db
}

func newSQLKV(t *testing.T) *store.SQLKV {
	kv := store.NewSQLKV(cleanDB(t))
	t.Cleanup(kv.Close)
	return kv
}

func TestSQLKV(t *testing.T) {
	storetest.RunKV(t, func(t *testing.T) store.KV { return newSQLKV(t) })
}

func TestSQLKV_SurvivesNewInstance(t *testing.T) {
	db := cleanDB(t)
	ctx := context.Background()
	first := store.NewSQLKV(db)
	require.NoError(t, first.Set(ctx, "refresh:jti-1", []byte("user-1"), time.Hour))
	first.Close()

	second := store.NewSQLKV(db) // simulates a restart
	defer second.Close()
	v, err := second.GetDel(ctx, "refresh:jti-1")
	require.NoError(t, err)
	assert.Equal(t, "user-1", string(v))
}

func TestSQLKV_PurgeExpired(t *testing.T) {
	db := cleanDB(t)
	ctx := context.Background()
	kv := store.NewSQLKV(db)
	defer kv.Close()
	require.NoError(t, kv.Set(ctx, "gone", []byte("x"), time.Millisecond))
	require.NoError(t, kv.Set(ctx, "kept", []byte("x"), 0))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, kv.PurgeExpired(ctx))
	var n int64
	db.Model(&models.KVEntry{}).Count(&n)
	assert.Equal(t, int64(1), n)
}
