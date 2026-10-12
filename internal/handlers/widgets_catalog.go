package handlers

import (
	"regexp"
	"strconv"
	"strings"
)

// Unified catalog: discovery fields == filter fields == allowed group-by (except special cases).

var widgetDataSources = map[string][]string{
	"messages":  {"status", "direction", "message_type", "whatsapp_account", "contact_id", "sent_by_user_id", "template_name", "conversation_id"},
	"contacts":  {"whatsapp_account", "is_read", "assigned_user_id", "marketing_opt_out"},
	"campaigns": {"status", "message_status", "template_name", "created_by_id", "whatsapp_account"},
	"transfers": {"status", "source", "team_id", "agent_id"},
	"sessions":  {"status", "current_flow_id", "whatsapp_account"},
	"agents":    {"is_available", "team_id"},
	"calls":     {"direction", "status", "agent_id", "whatsapp_account"},
	"templates": {"status", "category", "language", "whatsapp_account"},
	"flows":     {"is_enabled", "whatsapp_account"},
	"tags":      {"name", "color"},
	"opt_outs":  {"whatsapp_account", "assigned_user_id"},
	"sla":       {"status", "source", "agent_id", "team_id", "sla_breached", "escalation_level"},
	"media":     {"status", "direction", "message_type", "whatsapp_account"},
	"ai":        {"provider", "purpose", "model", "whatsapp_account"},
	"webhooks":  {"success", "event", "status_code", "webhook_id"},
}

var widgetMetrics = []string{
	"count", "sum", "avg", "unique", "rate", "ratio",
	"sum_duration", "avg_duration", "delta", "p50", "p95",
}

var widgetDisplayTypes = []string{
	"number", "percentage", "chart", "table", "shortcuts",
	"sparkline", "gauge", "leaderboard", "heatmap", "funnel",
	"kpi_strip", "alert_list", "queue", "agent_status", "iframe",
}

var staticDisplayTypes = map[string]bool{
	"shortcuts": true,
	"iframe":    true,
}

var liveDisplayTypes = map[string]bool{
	"queue":        true,
	"agent_status": true,
	"alert_list":   true,
}

var widgetNamedColors = map[string]bool{
	"blue": true, "green": true, "purple": true, "orange": true,
	"red": true, "cyan": true, "amber": true, "pink": true,
	"slate": true, "teal": true, "indigo": true, "lime": true, "rose": true,
}

var hexColorRe = regexp.MustCompile(`(?i)^#[0-9a-f]{6}$`)

func isValidWidgetColor(color string) bool {
	if color == "" {
		return true
	}
	if widgetNamedColors[color] {
		return true
	}
	return hexColorRe.MatchString(color)
}

// allowedFilterFields is derived from widgetDataSources so discovery and runtime stay aligned.
var allowedFilterFields = func() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(widgetDataSources))
	for source, fields := range widgetDataSources {
		m := make(map[string]bool, len(fields))
		for _, f := range fields {
			m[f] = true
		}
		out[source] = m
	}
	return out
}()

// booleanFilterFields are filter fields backed by boolean columns. Their values
// are bound as Go bools: SQLite stores booleans as 0/1, not 'true'/'false'.
var booleanFilterFields = map[string]map[string]bool{
	"contacts": {"is_read": true, "marketing_opt_out": true},
	"agents":   {"is_available": true},
	"flows":    {"is_enabled": true},
	"sla":      {"sla_breached": true},
	"webhooks": {"success": true},
}

var allowedAggregateFields = map[string]map[string]bool{
	"messages":  {},
	"calls":     {"duration": true},
	"transfers": {"resolution_time": true},
	"sla":       {"resolution_time": true, "queue_time": true, "first_response_time": true},
	"sessions":  {},
	"contacts":  {},
	"campaigns": {"sent_count": true, "delivered_count": true, "read_count": true, "failed_count": true},
	"agents":    {},
	"templates": {},
	"flows":     {},
	"tags":      {},
	"opt_outs":  {},
	"media":     {},
	"ai":        {"prompt_tokens": true, "completion_tokens": true, "total_tokens": true},
	"webhooks":  {"duration_ms": true, "attempts": true},
}

var widgetOperatorDefs = []map[string]string{
	{"value": "equals", "label": "Equals"},
	{"value": "not_equals", "label": "Not Equals"},
	{"value": "contains", "label": "Contains"},
	{"value": "starts_with", "label": "Starts With"},
	{"value": "gt", "label": "Greater Than"},
	{"value": "lt", "label": "Less Than"},
	{"value": "gte", "label": "Greater Than or Equal"},
	{"value": "lte", "label": "Less Than or Equal"},
	{"value": "in", "label": "In"},
	{"value": "not_in", "label": "Not In"},
	{"value": "is_null", "label": "Is Null"},
	{"value": "is_not_null", "label": "Is Not Null"},
	{"value": "between", "label": "Between"},
}

func isAllowedGroupByField(dataSource, field string) bool {
	if field == "" {
		return false
	}
	if dataSource == "campaigns" && field == "message_status" {
		return true
	}
	return allowedFilterFields[dataSource][field]
}

// filterArg returns the bind value for a filter on field.
func filterArg(dataSource, field, value string) any {
	if booleanFilterFields[dataSource][field] {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return value
}

// groupLabel returns the chart label for a grouped value. Booleans read back
// as "1"/"0" on SQLite and "true"/"false" on PostgreSQL; both become "true"/"false".
func groupLabel(dataSource, field, value string) string {
	if booleanFilterFields[dataSource][field] {
		switch value {
		case "1":
			return "true"
		case "0":
			return "false"
		}
	}
	if value == "" {
		return "(empty)"
	}
	return value
}

func parseCSVValues(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func namedWidgetColorList() []string {
	return []string{"blue", "green", "purple", "orange", "red", "cyan", "amber", "pink", "slate", "teal", "indigo", "lime", "rose"}
}
