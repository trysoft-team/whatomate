package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestAgentTransfer_TimeRangeAcrossZones(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))

	plus2 := time.FixedZone("UTC+2", 2*60*60)
	minus5 := time.FixedZone("UTC-5", -5*60*60)
	at := time.Date(2026, 10, 5, 10, 0, 0, 0, plus2) // 08:00 UTC
	transfer := &models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, PhoneNumber: contact.PhoneNumber,
		Status: models.TransferStatusActive, TransferredAt: at,
	}
	require.NoError(t, app.DB.Create(transfer).Error)

	count := func(from, to time.Time) int64 {
		var n int64
		require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
			Where("organization_id = ? AND transferred_at >= ? AND transferred_at <= ?", org.ID, from, to).
			Count(&n).Error)
		return n
	}
	// 02:59-03:01 in UTC-5 is 07:59-08:01 UTC.
	assert.EqualValues(t, 1, count(time.Date(2026, 10, 5, 2, 59, 0, 0, minus5), time.Date(2026, 10, 5, 3, 1, 0, 0, minus5)))
	assert.EqualValues(t, 0, count(time.Date(2026, 10, 5, 3, 1, 0, 0, minus5), time.Date(2026, 10, 5, 4, 0, 0, 0, minus5)))
	assert.EqualValues(t, 1, app.queryTransfers(org.ID, "count", "", nil,
		time.Date(2026, 10, 5, 2, 59, 0, 0, minus5), time.Date(2026, 10, 5, 3, 1, 0, 0, minus5)))

	var stored models.AgentTransfer
	require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
	assert.True(t, at.Equal(stored.TransferredAt), "stored %v, want %v", stored.TransferredAt, at)
}
