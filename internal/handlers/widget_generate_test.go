package handlers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAIWidgetDraft_CleansMessyOutput(t *testing.T) {
	t.Parallel()
	raw := "Here you go:\n```json\n" + `{
		"name": "Failed outgoing messages this period and a very long title that goes on",
		"data_source": "Messages",
		"metric": "COUNT",
		"display_type": "number",
		"filters": [
			{"field": "status", "operator": "equals", "value": "failed"},
			{"field": "direction", "value": "outgoing"},
			{"field": "password; DROP TABLE users", "operator": "equals", "value": "x"},
			{"field": "status", "operator": "regex", "value": ".*"},
			{"field": "message_type", "operator": "in", "value": ["text", "image"]},
			{"field": "status", "operator": "equals", "value": ""}
		],
		"group_by_field": "status",
		"color": "neon",
		"show_change": false
	}` + "\n```"

	d, err := parseAIWidgetDraft(raw)
	require.NoError(t, err)
	assert.LessOrEqual(t, len([]rune(d.Name)), 60)
	assert.Equal(t, "messages", d.DataSource)
	assert.Equal(t, "count", d.Metric)
	assert.Equal(t, "number", d.DisplayType)
	assert.Equal(t, []FilterInput{
		{Field: "status", Operator: "equals", Value: "failed"},
		{Field: "direction", Operator: "equals", Value: "outgoing"},
		{Field: "message_type", Operator: "in", Value: "text,image"},
	}, d.Filters)
	assert.Empty(t, d.GroupByField, "group by only applies to chart/table")
	assert.Equal(t, "blue", d.Color)
	assert.False(t, d.ShowChange)
	assert.Equal(t, "small", d.Size)
}

func TestParseAIWidgetDraft_ChartAndAggregates(t *testing.T) {
	t.Parallel()
	d, err := parseAIWidgetDraft(`{"name":"Calls by status","data_source":"calls","metric":"avg","field":"duration","display_type":"chart","chart_type":"pie","group_by_field":"status"}`)
	require.NoError(t, err)
	assert.Equal(t, "avg", d.Metric)
	assert.Equal(t, "duration", d.Field)
	assert.Equal(t, "pie", d.ChartType)
	assert.Equal(t, "status", d.GroupByField)
	assert.Equal(t, "medium", d.Size)

	// Pie without a breakdown falls back to a line chart; bad aggregate field drops to count.
	d, err = parseAIWidgetDraft(`{"name":"Msgs","data_source":"messages","metric":"sum","field":"content","display_type":"chart","chart_type":"pie"}`)
	require.NoError(t, err)
	assert.Equal(t, "count", d.Metric)
	assert.Empty(t, d.Field)
	assert.Equal(t, "line", d.ChartType)
}

func TestParseAIWidgetDraft_DisplayConfigs(t *testing.T) {
	t.Parallel()

	d, err := parseAIWidgetDraft(`{"name":"Webhooks","data_source":"webhooks","display_type":"kpi_strip","config":{"kpis":[
		{"label":"Delivered","filters":[{"field":"success","operator":"equals","value":true}]},
		{"label":"Failed","filters":[{"field":"success","operator":"equals","value":"false"},{"field":"is_active","value":"true"}]},
		{"label":"Success rate","metric":"rate"},
		{"label":"Avg time","metric":"avg","field":"duration_ms"},
		{"label":"Too many"}
	]}}`)
	require.NoError(t, err)
	kpis := d.Config["kpis"].([]any)
	require.Len(t, kpis, 4)
	assert.Equal(t, []any{map[string]any{"field": "success", "operator": "equals", "value": "true"}}, kpis[0].(map[string]any)["filters"])
	assert.Len(t, kpis[1].(map[string]any)["filters"], 1, "unknown KPI filter field dropped")
	assert.Equal(t, "rate", kpis[2].(map[string]any)["metric"])
	assert.Equal(t, "duration_ms", kpis[3].(map[string]any)["field"])

	d, err = parseAIWidgetDraft(`{"name":"Resolution","data_source":"transfers","display_type":"gauge","config":{"target":"80"}}`)
	require.NoError(t, err)
	assert.Equal(t, 80.0, d.Config["target"])

	// Leaderboard on a source without agent ranking becomes a bar chart.
	d, err = parseAIWidgetDraft(`{"name":"Top","data_source":"templates","display_type":"leaderboard","group_by_field":"category"}`)
	require.NoError(t, err)
	assert.Equal(t, "chart", d.DisplayType)
	assert.Equal(t, "bar", d.ChartType)
	assert.Equal(t, "category", d.GroupByField)

	// Funnel needs stages outside campaigns.
	d, err = parseAIWidgetDraft(`{"name":"Journey","data_source":"messages","display_type":"funnel","config":{"stages":[
		{"label":"Sent","filters":[{"field":"status","operator":"in","value":"sent,delivered,read"}]},
		{"stage":"Read","filters":[{"field":"status","operator":"equals","value":"read"}]}
	]}}`)
	require.NoError(t, err)
	assert.Equal(t, "funnel", d.DisplayType)
	assert.Len(t, d.Config["stages"], 2)

	// Rate needs a numerator (except webhooks, which default to success rate).
	d, err = parseAIWidgetDraft(`{"name":"Read rate","data_source":"messages","metric":"rate","display_type":"percentage","config":{"numerator_filter":[{"field":"status","operator":"equals","value":"read"}]}}`)
	require.NoError(t, err)
	assert.Equal(t, "rate", d.Metric)
	assert.NotEmpty(t, d.Config["numerator_filter"])
	d, err = parseAIWidgetDraft(`{"name":"Rate","data_source":"messages","metric":"rate","display_type":"number"}`)
	require.NoError(t, err)
	assert.Equal(t, "count", d.Metric)
	d, err = parseAIWidgetDraft(`{"name":"Webhook rate","data_source":"webhooks","metric":"rate","display_type":"percentage"}`)
	require.NoError(t, err)
	assert.Equal(t, "rate", d.Metric)

	// Live list types pin their data source.
	d, err = parseAIWidgetDraft(`{"name":"Waiting","data_source":"messages","display_type":"queue"}`)
	require.NoError(t, err)
	assert.Equal(t, "transfers", d.DataSource)
}

func TestParseAIWidgetDraft_Rejects(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"no json here",
		`{"name":"x","data_source":"users_secret"}`,
		`{"name":"x"}`,
	} {
		_, err := parseAIWidgetDraft(raw)
		assert.Error(t, err, raw)
	}
	// shortcuts / iframe are not offered to the AI.
	d, err := parseAIWidgetDraft(`{"name":"x","data_source":"messages","display_type":"iframe","config":{"url":"https://evil.example"}}`)
	require.NoError(t, err)
	assert.Equal(t, "number", d.DisplayType)
	assert.Empty(t, d.Config)
}

func TestBuildAIWidgetSystemPrompt_ListsCatalog(t *testing.T) {
	t.Parallel()
	p := buildAIWidgetSystemPrompt()
	for src, fields := range widgetDataSources {
		assert.Contains(t, p, "- "+src+" (", src)
		assert.Contains(t, p, strings.Join(fields, ", "), src)
	}
	assert.Contains(t, p, "duration_ms")
	assert.NotContains(t, p, "shortcuts")
}
