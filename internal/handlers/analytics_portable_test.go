package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/database/dialect"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func TestAgentAnalytics_AvgPickupMinutes(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	picked := start.Add(10 * time.Minute)
	resumed := start.Add(30 * time.Minute)
	require.NoError(t, app.DB.Create(&models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "t", PhoneNumber: contact.PhoneNumber,
		Status: models.TransferStatusResumed, Source: models.TransferSourceManual,
		TransferredAt: start, ResumedAt: &resumed, SLA: models.SLATracking{PickedUpAt: &picked},
	}).Error)

	var avg float64
	d := dialect.Of(app.DB)
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
		Select("AVG("+d.MinutesBetween("transferred_at", "picked_up_at")+")").
		Where("organization_id = ?", org.ID).Row().Scan(&avg))
	assert.InDelta(t, 10, avg, 0.01)

	// No matching rows: AVG is NULL and must scan as zero, as the handlers expect.
	type AvgResult struct {
		Avg float64
	}
	var none AvgResult
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
		Select("AVG("+d.MinutesBetween("transferred_at", "picked_up_at")+") as avg").
		Where("organization_id = ?", uuid.New()).Scan(&none).Error)
	assert.Zero(t, none.Avg)
}

func TestDateTrunc_GroupsByDay(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, h := range []int{1, 5, 23} {
		require.NoError(t, app.DB.Create(&models.AgentTransfer{
			OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "t", PhoneNumber: contact.PhoneNumber,
			Status: models.TransferStatusResumed, Source: models.TransferSourceManual,
			TransferredAt: day.Add(time.Duration(h) * time.Hour),
		}).Error)
	}
	d := dialect.Of(app.DB)
	var rows []struct {
		Date  dialect.DBTime
		Count int64
	}
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
		Select(d.DateTrunc("day", "transferred_at")+" AS date, COUNT(*) AS count").
		Where("organization_id = ?", org.ID).
		Group(d.DateTrunc("day", "transferred_at")).Scan(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "2026-10-05", rows[0].Date.Format("2006-01-02"))
	assert.EqualValues(t, 3, rows[0].Count)
}

// createPortableTransfers creates three resumed transfers at fixed UTC times
// (the PostgreSQL test database runs with timezone UTC):
//
//	Mon 2026-10-05 01:00, manual: pickup 10m, first response 4m, resolution 30m
//	Mon 2026-10-05 23:00, flow:   pickup 20m, first response 6m, resolution 50m
//	Wed 2026-10-07 02:00, manual: pickup 30m, first response 8m, resolution 40m
//
// Averages: pickup 20m, first response 6m, resolution 40m.
func createPortableTransfers(t *testing.T, app *handlers.App, orgID, contactID uuid.UUID, phone string, agentID *uuid.UUID) {
	t.Helper()
	items := []struct {
		at                         time.Time
		source                     models.TransferSource
		pickup, response, resolved time.Duration
	}{
		{time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC), models.TransferSourceManual, 10 * time.Minute, 4 * time.Minute, 30 * time.Minute},
		{time.Date(2026, 10, 5, 23, 0, 0, 0, time.UTC), models.TransferSourceFlow, 20 * time.Minute, 6 * time.Minute, 50 * time.Minute},
		{time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC), models.TransferSourceManual, 30 * time.Minute, 8 * time.Minute, 40 * time.Minute},
	}
	for _, it := range items {
		picked := it.at.Add(it.pickup)
		responded := it.at.Add(it.response)
		resumed := it.at.Add(it.resolved)
		require.NoError(t, app.DB.Create(&models.AgentTransfer{
			OrganizationID: orgID, ContactID: contactID, WhatsAppAccount: "t", PhoneNumber: phone,
			Status: models.TransferStatusResumed, Source: it.source, AgentID: agentID,
			TransferredAt: it.at, ResumedAt: &resumed,
			SLA: models.SLATracking{PickedUpAt: &picked, FirstResponseAt: &responded},
		}).Error)
	}
}

func getAgentAnalytics(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, params map[string]string) handlers.AgentAnalyticsResponse {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	for k, v := range params {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, app.GetAgentAnalytics(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data handlers.AgentAnalyticsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func TestGetAgentAnalytics_PortableDateMath(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Analytics Portable", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("analytics-portable")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)
	createTestTeamWithAgent(t, app, org.ID, user.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	createPortableTransfers(t, app, org.ID, contact.ID, contact.PhoneNumber, &user.ID)

	period := map[string]string{"from": "2026-10-05", "to": "2026-10-11"}

	// Organization-wide summary, per-agent stats and daily trend.
	data := getAgentAnalytics(t, app, org.ID, user.ID, period)
	assert.EqualValues(t, 3, data.Summary.TotalTransfersHandled)
	assert.InDelta(t, 20, data.Summary.AvgQueueTimeMins, 0.01)
	assert.InDelta(t, 6, data.Summary.AvgFirstResponseMins, 0.01)
	assert.InDelta(t, 40, data.Summary.AvgResolutionMins, 0.01)
	assert.Equal(t, []handlers.TrendPoint{
		{Date: "2026-10-05", TransfersHandled: 2},
		{Date: "2026-10-07", TransfersHandled: 1},
	}, data.TrendData)
	require.NotNil(t, data.MyStats)
	assert.EqualValues(t, 3, data.MyStats.TransfersHandled)
	assert.InDelta(t, 6, data.MyStats.AvgFirstResponseMins, 0.01)
	assert.InDelta(t, 40, data.MyStats.AvgResolutionMins, 0.01)
	require.Len(t, data.AgentStats, 1)
	assert.InDelta(t, 40, data.AgentStats[0].AvgResolutionMins, 0.01)

	// Weekly trend: 2026-10-05 is a Monday, so all three land in one week.
	weekly := map[string]string{"from": "2026-10-05", "to": "2026-10-11", "group_by": "week"}
	data = getAgentAnalytics(t, app, org.ID, user.ID, weekly)
	assert.Equal(t, []handlers.TrendPoint{{Date: "2026-10-05", TransfersHandled: 3}}, data.TrendData)

	// Single-agent summary.
	single := map[string]string{"from": "2026-10-05", "to": "2026-10-11", "agent_id": user.ID.String()}
	data = getAgentAnalytics(t, app, org.ID, user.ID, single)
	assert.EqualValues(t, 3, data.Summary.TotalTransfersHandled)
	assert.InDelta(t, 6, data.Summary.AvgFirstResponseMins, 0.01)
	assert.InDelta(t, 40, data.Summary.AvgResolutionMins, 0.01)

	// A period without transfers: the AVG queries return NULL.
	empty := map[string]string{"from": "2025-01-01", "to": "2025-01-02"}
	data = getAgentAnalytics(t, app, org.ID, user.ID, empty)
	assert.Zero(t, data.Summary.TotalTransfersHandled)
	assert.Zero(t, data.Summary.AvgQueueTimeMins)
	assert.Zero(t, data.Summary.AvgResolutionMins)
	assert.Empty(t, data.TrendData)
}

func createWidgetForTest(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, w models.Widget) *models.Widget {
	t.Helper()
	w.ID = uuid.New()
	w.OrganizationID = orgID
	w.UserID = &userID
	w.Color = "blue"
	w.Size = "medium"
	w.GridW = 6
	w.GridH = 4
	require.NoError(t, app.DB.Create(&w).Error)
	return &w
}

func getWidgetDataFor(t *testing.T, app *handlers.App, orgID, userID, widgetID uuid.UUID, from, to string) handlers.WidgetDataResponse {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", widgetID.String())
	testutil.SetQueryParam(req, "from", from)
	testutil.SetQueryParam(req, "to", to)
	require.NoError(t, app.GetWidgetData(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data handlers.WidgetDataResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func newWidgetTestUser(t *testing.T, app *handlers.App, orgID uuid.UUID, name string) *models.User {
	t.Helper()
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, orgID, name, false, false, perms)
	return testutil.CreateTestUser(t, app.DB, orgID,
		testutil.WithEmail(testutil.UniqueEmail("widget-portable")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)
}

func TestGetWidgetData_TrendWidgetsByDay(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := newWidgetTestUser(t, app, org.ID, "Widget Trend")
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	createPortableTransfers(t, app, org.ID, contact.ID, contact.PhoneNumber, nil)

	line := createWidgetForTest(t, app, org.ID, user.ID, models.Widget{
		Name: "Transfers per day", DataSource: "transfers", Metric: "count", DisplayType: "chart", ChartType: "line",
	})
	data := getWidgetDataFor(t, app, org.ID, user.ID, line.ID, "2026-10-05", "2026-10-11")
	assert.Equal(t, []handlers.ChartPoint{{Label: "Oct 05", Value: 2}, {Label: "Oct 07", Value: 1}}, data.ChartData)

	grouped := createWidgetForTest(t, app, org.ID, user.ID, models.Widget{
		Name: "Transfers by source", DataSource: "transfers", Metric: "count", DisplayType: "chart", ChartType: "line",
		GroupByField: "source",
	})
	data = getWidgetDataFor(t, app, org.ID, user.ID, grouped.ID, "2026-10-05", "2026-10-11")
	require.NotNil(t, data.GroupedSeries)
	assert.Equal(t, []string{"Oct 05", "Oct 07"}, data.GroupedSeries.Labels)
	series := map[string][]float64{}
	for _, ds := range data.GroupedSeries.Datasets {
		series[ds.Label] = ds.Data
	}
	assert.Equal(t, map[string][]float64{"manual": {1, 1}, "flow": {1, 0}}, series)

	campaign := createAnalyticsTestCampaign(t, app, org.ID, user.ID, "completed", time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	require.NoError(t, app.DB.Model(campaign).Updates(map[string]any{"sent_count": 5, "delivered_count": 4}).Error)
	status := createWidgetForTest(t, app, org.ID, user.ID, models.Widget{
		Name: "Campaign status", DataSource: "campaigns", Metric: "count", DisplayType: "chart", ChartType: "line",
		GroupByField: "message_status",
	})
	data = getWidgetDataFor(t, app, org.ID, user.ID, status.ID, "2026-10-05", "2026-10-11")
	require.NotNil(t, data.GroupedSeries)
	assert.Equal(t, []string{"Oct 05"}, data.GroupedSeries.Labels)
	require.Len(t, data.GroupedSeries.Datasets, 4)
	assert.Equal(t, "sent", data.GroupedSeries.Datasets[0].Label)
	assert.Equal(t, []float64{5}, data.GroupedSeries.Datasets[0].Data)
	assert.Equal(t, []float64{4}, data.GroupedSeries.Datasets[1].Data)

	avgQueue := createWidgetForTest(t, app, org.ID, user.ID, models.Widget{
		Name: "Avg queue time", DataSource: "sla", Metric: "avg", Field: "queue_time", DisplayType: "number",
	})
	data = getWidgetDataFor(t, app, org.ID, user.ID, avgQueue.ID, "2026-10-05", "2026-10-11")
	assert.InDelta(t, 20, data.Value, 0.01)
}

func TestGetWidgetData_HeatmapByDayAndHour(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := newWidgetTestUser(t, app, org.ID, "Widget Heatmap")
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	createPortableTransfers(t, app, org.ID, contact.ID, contact.PhoneNumber, nil)

	heatmap := createWidgetForTest(t, app, org.ID, user.ID, models.Widget{
		Name: "Transfers heatmap", DataSource: "transfers", Metric: "count", DisplayType: "heatmap",
	})
	data := getWidgetDataFor(t, app, org.ID, user.ID, heatmap.ID, "2026-10-05", "2026-10-11")
	require.NotNil(t, data.Heatmap)
	cells := data.Heatmap.Cells
	require.Len(t, cells, 24)
	// cells[hour][day of week], Sunday = 0.
	assert.Equal(t, float64(1), cells[1][1], "Mon 01:00")
	assert.Equal(t, float64(1), cells[23][1], "Mon 23:00")
	assert.Equal(t, float64(1), cells[2][3], "Wed 02:00")
	var total float64
	for _, row := range cells {
		for _, v := range row {
			total += v
		}
	}
	assert.Equal(t, float64(3), total)
}
