package handlers

import (
	"context"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database/dialect"
)

// webhookDeliveryPruneBatch caps rows per DELETE so a large backlog never
// holds a long lock on webhook_deliveries.
const webhookDeliveryPruneBatch = 5000

// WebhookDeliveryPruner deletes webhook_deliveries rows older than the
// configured retention window.
type WebhookDeliveryPruner struct {
	app       *App
	retention time.Duration
	interval  time.Duration
	stopCh    chan struct{}
}

// NewWebhookDeliveryPruner creates a pruner that keeps retentionDays of
// delivery history and sweeps every interval. retentionDays <= 0 disables it.
func NewWebhookDeliveryPruner(app *App, retentionDays int, interval time.Duration) *WebhookDeliveryPruner {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &WebhookDeliveryPruner{
		app:       app,
		retention: time.Duration(retentionDays) * 24 * time.Hour,
		interval:  interval,
		stopCh:    make(chan struct{}),
	}
}

// Start prunes once immediately, then on every tick until stopped.
func (p *WebhookDeliveryPruner) Start(ctx context.Context) {
	if p.retention <= 0 {
		p.app.Log.Info("Webhook delivery pruning disabled")
		return
	}
	p.app.Log.Info("Webhook delivery pruner started", "retention", p.retention, "interval", p.interval)
	p.prune(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.app.Log.Info("Webhook delivery pruner stopped by context")
			return
		case <-p.stopCh:
			p.app.Log.Info("Webhook delivery pruner stopped")
			return
		case <-ticker.C:
			p.prune(ctx)
		}
	}
}

// Stop stops the pruner.
func (p *WebhookDeliveryPruner) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

func (p *WebhookDeliveryPruner) prune(ctx context.Context) {
	cutoff := time.Now().Add(-p.retention)
	deleted, err := p.app.pruneWebhookDeliveries(ctx, cutoff, webhookDeliveryPruneBatch)
	if err != nil {
		p.app.Log.Warn("Failed to prune webhook deliveries", "error", err, "deleted", deleted)
		return
	}
	if deleted > 0 {
		p.app.Log.Info("Pruned webhook deliveries", "deleted", deleted, "older_than", cutoff)
	}
}

// pruneWebhookDeliveries hard-deletes deliveries created before cutoff in
// batches of batchSize and returns the number of rows removed.
func (a *App) pruneWebhookDeliveries(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	query := `DELETE FROM webhook_deliveries WHERE id IN (
				SELECT id FROM webhook_deliveries WHERE created_at < ? LIMIT ?
			)`
	if dialect.Of(a.DB).Name() == config.DriverMySQL {
		// MySQL rejects LIMIT inside an IN subquery (error 1235).
		query = `DELETE FROM webhook_deliveries WHERE created_at < ? LIMIT ?`
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res := a.DB.WithContext(ctx).Exec(query, cutoff, batchSize)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batchSize) {
			return total, nil
		}
	}
}
