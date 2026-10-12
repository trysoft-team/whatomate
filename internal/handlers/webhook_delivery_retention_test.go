package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestPruneWebhookDeliveries(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	webhookID := uuid.New()

	now := time.Now()
	create := func(age time.Duration) uuid.UUID {
		row := models.WebhookDelivery{
			BaseModel:      models.BaseModel{ID: uuid.New(), CreatedAt: now.Add(-age)},
			OrganizationID: org.ID,
			WebhookID:      webhookID,
			Event:          "message.incoming",
			Success:        true,
		}
		require.NoError(t, app.DB.Create(&row).Error)
		return row.ID
	}
	for i := 0; i < 5; i++ {
		create(100 * 24 * time.Hour)
	}
	keepRecent := create(time.Hour)
	keepEdge := create(89 * 24 * time.Hour)

	// Batch of 2 forces several DELETE rounds for the 5 old rows.
	deleted, err := app.PruneWebhookDeliveriesForTest(context.Background(), now.Add(-90*24*time.Hour), 2)
	require.NoError(t, err)
	assert.EqualValues(t, 5, deleted)

	var remaining []uuid.UUID
	require.NoError(t, app.DB.Model(&models.WebhookDelivery{}).
		Where("organization_id = ?", org.ID).Pluck("id", &remaining).Error)
	assert.ElementsMatch(t, []uuid.UUID{keepRecent, keepEdge}, remaining)

	deleted, err = app.PruneWebhookDeliveriesForTest(context.Background(), now.Add(-90*24*time.Hour), 2)
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestPruneWebhookDeliveries_CancelledContext(t *testing.T) {
	app := newTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := app.PruneWebhookDeliveriesForTest(ctx, time.Now(), 10)
	assert.ErrorIs(t, err, context.Canceled)
}
