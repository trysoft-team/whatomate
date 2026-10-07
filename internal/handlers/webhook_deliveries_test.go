package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func createDeliveryTestWebhook(t *testing.T, app *handlers.App, orgID uuid.UUID, url string) *models.Webhook {
	t.Helper()
	wh := &models.Webhook{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		Name:           "Delivery test",
		URL:            url,
		Events:         models.StringArray{string(models.WebhookEventMessageIncoming)},
		IsActive:       true,
	}
	require.NoError(t, app.DB.Create(wh).Error)
	return wh
}

func waitForDelivery(t *testing.T, app *handlers.App, webhookID uuid.UUID) models.WebhookDelivery {
	t.Helper()
	var d models.WebhookDelivery
	require.Eventually(t, func() bool {
		return app.DB.Where("webhook_id = ?", webhookID).First(&d).Error == nil
	}, 10*time.Second, 50*time.Millisecond, "no webhook delivery recorded")
	return d
}

func TestDispatchWebhook_RecordsSuccessfulDelivery(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	wh := createDeliveryTestWebhook(t, app, org.ID, srv.URL)

	app.DispatchWebhook(org.ID, models.WebhookEventMessageIncoming, map[string]string{"k": "v"})

	d := waitForDelivery(t, app, wh.ID)
	assert.Equal(t, org.ID, d.OrganizationID)
	assert.Equal(t, string(models.WebhookEventMessageIncoming), d.Event)
	assert.True(t, d.Success)
	assert.Equal(t, http.StatusAccepted, d.StatusCode)
	assert.Equal(t, 1, d.Attempts)
	assert.Empty(t, d.Error)
}

func TestDispatchWebhook_RecordsRetriesThenSuccess(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	wh := createDeliveryTestWebhook(t, app, org.ID, srv.URL)

	app.DispatchWebhook(org.ID, models.WebhookEventMessageIncoming, nil)

	d := waitForDelivery(t, app, wh.ID)
	assert.True(t, d.Success)
	assert.Equal(t, http.StatusOK, d.StatusCode)
	assert.Equal(t, 2, d.Attempts)
	assert.GreaterOrEqual(t, d.DurationMs, 1000, "duration should include the backoff")

	var count int64
	app.DB.Model(&models.WebhookDelivery{}).Where("webhook_id = ?", wh.ID).Count(&count)
	assert.Equal(t, int64(1), count, "one row per delivery, not per attempt")
}

func TestGetWidgetData_WebhookDeliveryStats(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Widget Webhooks", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("widget-webhooks")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)
	wh := createDeliveryTestWebhook(t, app, org.ID, "https://example.com/hook")

	now := time.Now().UTC()
	fromDay := now.AddDate(0, 0, -1)
	from := fromDay.Format("2006-01-02")
	to := now.AddDate(0, 0, 1).Format("2006-01-02")
	prevAt := time.Date(fromDay.Year(), fromDay.Month(), fromDay.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -1)

	addDelivery := func(success bool, at time.Time) {
		d := &models.WebhookDelivery{
			BaseModel:      models.BaseModel{ID: uuid.New(), CreatedAt: at, UpdatedAt: at},
			OrganizationID: org.ID,
			WebhookID:      wh.ID,
			Event:          string(models.WebhookEventMessageIncoming),
			Success:        success,
			StatusCode:     200,
			Attempts:       1,
		}
		if !success {
			d.StatusCode = 500
			d.Attempts = 3
		}
		require.NoError(t, app.DB.Create(d).Error)
	}
	// Current period: 3 delivered, 1 failed. Previous period: 1 delivered, 1 failed.
	addDelivery(true, now)
	addDelivery(true, now)
	addDelivery(true, now)
	addDelivery(false, now)
	addDelivery(true, prevAt)
	addDelivery(false, prevAt)

	newWidget := func(name, metric, display string, filters models.JSONBArray, cfg models.JSONB) *models.Widget {
		w := &models.Widget{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			UserID:         &user.ID,
			Name:           name,
			DataSource:     "webhooks",
			Metric:         metric,
			DisplayType:    display,
			Filters:        filters,
			Config:         cfg,
			ShowChange:     true,
			Color:          "blue",
			Size:           "small",
			GridW:          3,
			GridH:          3,
		}
		require.NoError(t, app.DB.Create(w).Error)
		return w
	}
	fetch := func(w *models.Widget) handlers.WidgetDataResponse {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", w.ID.String())
		req.RequestCtx.QueryArgs().Set("from", from)
		req.RequestCtx.QueryArgs().Set("to", to)
		require.NoError(t, app.GetWidgetData(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		var resp struct {
			Data handlers.WidgetDataResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		return resp.Data
	}

	total := fetch(newWidget("All", "count", "number", nil, nil))
	assert.False(t, total.Stub)
	assert.Equal(t, float64(4), total.Value)
	assert.Equal(t, float64(2), total.PrevValue)

	failed := fetch(newWidget("Failed", "count", "number",
		models.JSONBArray{map[string]any{"field": "success", "operator": "equals", "value": "false"}}, nil))
	assert.Equal(t, float64(1), failed.Value)
	assert.Equal(t, float64(1), failed.PrevValue)

	rate := fetch(newWidget("Rate", "rate", "number", nil, nil))
	assert.InDelta(t, 75.0, rate.Value, 0.01, "rate defaults to success rate")
	assert.InDelta(t, 50.0, rate.PrevValue, 0.01)

	strip := fetch(newWidget("Strip", "count", "kpi_strip", nil, models.JSONB{
		"kpis": []any{
			map[string]any{"label": "Delivered", "filters": []any{map[string]any{"field": "success", "operator": "equals", "value": "true"}}},
			map[string]any{"label": "Success rate", "metric": "rate"},
		},
	}))
	require.Len(t, strip.KPIs, 2)
	assert.Equal(t, float64(3), strip.KPIs[0].Value)
	assert.Empty(t, strip.KPIs[0].Unit)
	assert.InDelta(t, 75.0, strip.KPIs[1].Value, 0.01)
	assert.Equal(t, "%", strip.KPIs[1].Unit)
	require.NotNil(t, strip.KPIs[1].Change)
	assert.InDelta(t, 25.0, *strip.KPIs[1].Change, 0.01, "rate change is in percentage points")
}
