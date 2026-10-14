package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestStoreModels_RoundTrip(t *testing.T) {
	db := testutil.SetupTestDB(t)
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	kv := models.KVEntry{Key: "rt:" + testutil.RandomUUID().String(), Value: []byte{0, 1, 2, 255}, ExpiresAt: &exp}
	require.NoError(t, db.Create(&kv).Error)
	var gotKV models.KVEntry
	require.NoError(t, db.Where("kv_key = ?", kv.Key).Take(&gotKV).Error)
	assert.Equal(t, kv.Value, gotKV.Value)
	assert.True(t, gotKV.ExpiresAt.Equal(exp))

	item := models.ScheduledItem{SetName: "rt", Item: testutil.RandomUUID().String(), DueAt: exp}
	require.NoError(t, db.Create(&item).Error)

	job := models.CampaignJob{JobType: "recipient", Payload: `{"a":1}`}
	require.NoError(t, db.Create(&job).Error)
	assert.NotZero(t, job.ID)

	db.Where("kv_key = ?", kv.Key).Delete(&models.KVEntry{})
	db.Where("set_name = ? AND item = ?", item.SetName, item.Item).Delete(&models.ScheduledItem{})
	db.Delete(&job)
}
