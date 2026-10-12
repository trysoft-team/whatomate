package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/database/dialect"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestBuildFilterSQL_BindsBooleanFieldsAsBool(t *testing.T) {
	d := dialect.Named("sqlite")
	cases := []struct {
		source string
		filter FilterInput
		want   []any
	}{
		{"webhooks", FilterInput{Field: "success", Operator: "equals", Value: "false"}, []any{false}},
		{"webhooks", FilterInput{Field: "success", Operator: "not_equals", Value: "true"}, []any{true}},
		{"contacts", FilterInput{Field: "is_read", Operator: "in", Value: "true, false"}, []any{true, false}},
		{"agents", FilterInput{Field: "is_available", Value: "1"}, []any{true}},
		{"sla", FilterInput{Field: "sla_breached", Operator: "equals", Value: "true"}, []any{true}},
		{"webhooks", FilterInput{Field: "success", Operator: "equals", Value: "maybe"}, []any{"maybe"}},
		{"webhooks", FilterInput{Field: "event", Operator: "equals", Value: "true"}, []any{"true"}},
	}
	for _, c := range cases {
		_, args, ok := buildFilterSQL(d, c.source, c.filter)
		assert.True(t, ok)
		assert.Equal(t, c.want, args, "%s.%s %s %q", c.source, c.filter.Field, c.filter.Operator, c.filter.Value)
	}
}

func TestGroupedData_BooleanLabelsAreTrueFalse(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	now := time.Now().UTC()
	for _, isRead := range []bool{true, false, false} {
		contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{"is_read": isRead, "last_message_at": now}).Error)
	}
	widget := models.Widget{OrganizationID: org.ID, DataSource: "contacts", GroupByField: "is_read"}
	start, end := now.Add(-time.Hour), now.Add(time.Hour)

	points := app.getGroupedData(org.ID, widget, nil, start, end)
	assert.Equal(t, []DataPoint{{Label: "false", Value: 2}, {Label: "true", Value: 1}}, points)

	series := app.getGroupedTimeSeriesData(org.ID, widget, nil, start, end)
	labels := make([]string, 0, len(series.Datasets))
	for _, ds := range series.Datasets {
		labels = append(labels, ds.Label)
	}
	assert.ElementsMatch(t, []string{"true", "false"}, labels)
}

func TestGetGroupedTimeSeriesData_RejectsUnknownGroupByField(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	require.NoError(t, app.DB.Create(&models.Message{
		OrganizationID: org.ID, WhatsAppAccount: account.Name, ContactID: contact.ID,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "hi",
	}).Error)

	now := time.Now().UTC()
	series := func(groupBy string) GroupedSeriesData {
		widget := models.Widget{OrganizationID: org.ID, DataSource: "messages", GroupByField: groupBy}
		return app.getGroupedTimeSeriesData(org.ID, widget, nil, now.Add(-time.Hour), now.Add(time.Hour))
	}

	assert.Len(t, series("direction").Datasets, 1, "allowed field is grouped")
	assert.Empty(t, series("organization_id").Datasets, "field outside the allow-list is ignored")
	assert.Empty(t, series("1) AS group_value, COUNT(*) AS count FROM users --").Datasets)
}
