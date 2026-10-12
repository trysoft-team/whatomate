package handlers

import (
	"context"
	"time"
)

// PruneWebhookDeliveriesForTest exposes pruneWebhookDeliveries for unit tests.
func (a *App) PruneWebhookDeliveriesForTest(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return a.pruneWebhookDeliveries(ctx, cutoff, batchSize)
}
