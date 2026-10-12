package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestPercentileCont(t *testing.T) {
	assert.Equal(t, 0.0, percentileCont(nil, 0.5))
	assert.Equal(t, 7.0, percentileCont([]float64{7}, 0.95))
	assert.Equal(t, 30.0, percentileCont([]float64{40, 10, 30, 100, 20}, 0.5))
	assert.InDelta(t, 88.0, percentileCont([]float64{40, 10, 30, 100, 20}, 0.95), 1e-9)
	assert.Equal(t, 25.0, percentileCont([]float64{10, 20, 30, 40}, 0.5))
}

func TestAggregatePercentiles(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	webhook := &models.Webhook{OrganizationID: org.ID, Name: "p", URL: "https://example.com/p", IsActive: true}
	require.NoError(t, app.DB.Create(webhook).Error)

	now := time.Now().UTC()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	for _, ms := range []int{40, 10, 30, 100, 20} {
		require.NoError(t, app.DB.Create(&models.WebhookDelivery{
			OrganizationID: org.ID, WebhookID: webhook.ID, Event: "message.incoming", DurationMs: ms,
		}).Error)
	}
	assert.InDelta(t, 30.0, app.queryWebhooks(org.ID, "p50", "", nil, start, end), 1e-9)
	assert.InDelta(t, 88.0, app.queryWebhooks(org.ID, "p95", "", nil, start, end), 1e-9)

	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	for _, mins := range []int{10, 40, 20} {
		resumed := now.Add(time.Duration(mins) * time.Minute)
		require.NoError(t, app.DB.Create(&models.AgentTransfer{
			OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, PhoneNumber: contact.PhoneNumber,
			Status: models.TransferStatusResumed, TransferredAt: now, ResumedAt: &resumed,
		}).Error)
	}
	assert.InDelta(t, 20.0, app.queryTransfers(org.ID, "p50", "", nil, start, end), 0.001)
	assert.InDelta(t, 38.0, app.queryTransfers(org.ID, "p95", "", nil, start, end), 0.001)
}
