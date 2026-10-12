package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/websocket"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

func TestGetWidgetDataSources_ExpandedCatalog(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Widget Catalog", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("widget-cat")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.GetWidgetDataSources(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data struct {
			DataSources []struct {
				Name string `json:"name"`
			} `json:"data_sources"`
			Metrics      []string `json:"metrics"`
			DisplayTypes []string `json:"display_types"`
			Operators    []struct {
				Value string `json:"value"`
			} `json:"operators"`
			Colors []string `json:"colors"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))

	names := map[string]bool{}
	for _, s := range resp.Data.DataSources {
		names[s.Name] = true
	}
	for _, want := range []string{"messages", "calls", "agents", "sla", "media", "ai", "webhooks", "opt_outs", "flows", "templates", "tags"} {
		assert.True(t, names[want], "missing source %s", want)
	}
	assert.Contains(t, resp.Data.Metrics, "rate")
	assert.Contains(t, resp.Data.Metrics, "unique")
	assert.Contains(t, resp.Data.DisplayTypes, "sparkline")
	assert.Contains(t, resp.Data.DisplayTypes, "queue")
	assert.Contains(t, resp.Data.DisplayTypes, "iframe")
	ops := map[string]bool{}
	for _, o := range resp.Data.Operators {
		ops[o.Value] = true
	}
	assert.True(t, ops["in"])
	assert.True(t, ops["between"])
	assert.Contains(t, resp.Data.Colors, "amber")
}

func TestGetWidgetData_MessagesCountAndQueue(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Widget Data", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("widget-data")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	msg := &models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		ContactID:       contact.ID,
		WhatsAppAccount: "test",
		Direction:       models.DirectionOutgoing,
		MessageType:     models.MessageTypeText,
		Content:         "hi",
		Status:          models.MessageStatusSent,
	}
	require.NoError(t, app.DB.Create(msg).Error)

	waiting := &models.AgentTransfer{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		ContactID:       contact.ID,
		WhatsAppAccount: "test",
		PhoneNumber:     contact.PhoneNumber,
		Status:          models.TransferStatusActive,
		Source:          models.TransferSourceManual,
		TransferredAt:   time.Now().Add(-5 * time.Minute),
	}
	require.NoError(t, app.DB.Create(waiting).Error)

	numberWidget := &models.Widget{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		UserID:         &user.ID,
		Name:           "Msgs",
		DataSource:     "messages",
		Metric:         "count",
		DisplayType:    "number",
		ShowChange:     true,
		Color:          "blue",
		Size:           "small",
		GridW:          3,
		GridH:          3,
	}
	require.NoError(t, app.DB.Create(numberWidget).Error)

	queueWidget := &models.Widget{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		UserID:         &user.ID,
		Name:           "Queue",
		DataSource:     "transfers",
		Metric:         "count",
		DisplayType:    "queue",
		ShowChange:     false,
		Color:          "amber",
		Size:           "medium",
		GridW:          6,
		GridH:          5,
	}
	require.NoError(t, app.DB.Create(queueWidget).Error)

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -1).Format("2006-01-02")
	to := now.AddDate(0, 0, 1).Format("2006-01-02")

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", numberWidget.ID.String())
	req.RequestCtx.QueryArgs().Set("from", from)
	req.RequestCtx.QueryArgs().Set("to", to)
	require.NoError(t, app.GetWidgetData(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var numResp struct {
		Data handlers.WidgetDataResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &numResp))
	assert.GreaterOrEqual(t, numResp.Data.Value, float64(1))

	req2 := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req2, org.ID, user.ID)
	testutil.SetPathParam(req2, "id", queueWidget.ID.String())
	require.NoError(t, app.GetWidgetData(req2))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req2))

	var qResp struct {
		Data handlers.WidgetDataResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req2), &qResp))
	assert.True(t, qResp.Data.Live)
	require.Len(t, qResp.Data.QueueItems, 1)
	assert.Equal(t, waiting.ID.String(), qResp.Data.QueueItems[0].ID)
	assert.InDelta(t, 5, qResp.Data.QueueItems[0].WaitMins, 1)
	assert.Equal(t, float64(1), qResp.Data.Value)
}

func TestWidgetLiveUpdateTypeConstant(t *testing.T) {
	assert.Equal(t, "widget_live_update", websocket.TypeWidgetLiveUpdate)
}

func TestCreateWidget_InvalidColorRejected(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Widget Color", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("widget-color")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name":         "Bad Color",
		"data_source":  "messages",
		"metric":       "count",
		"display_type": "number",
		"color":        "neon",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateWidget(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestCreateWidget_IframeRequiresHTTPS(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	perms := getAnalyticsPermissions(t, app)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Widget Iframe", false, false, perms)
	user := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("widget-iframe")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name":         "Embed",
		"display_type": "iframe",
		"config":       map[string]any{"url": "http://insecure.example"},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateWidget(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}
