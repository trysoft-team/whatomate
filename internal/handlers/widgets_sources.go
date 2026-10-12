package handlers

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/database/dialect"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
)

func validateIframeConfig(config map[string]any) string {
	if config == nil {
		return "iframe requires config.url"
	}
	raw, _ := config["url"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "iframe requires config.url"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "iframe url must be a valid https URL"
	}
	return ""
}

func (a *App) queryWidgetMetric(orgID uuid.UUID, widget models.Widget, filters []FilterInput, start, end, prevStart, prevEnd time.Time) (current, previous float64, stub bool) {
	metric := widget.Metric
	if metric == "" {
		metric = "count"
	}

	switch widget.DataSource {
	case "messages":
		current = a.queryMessages(orgID, metric, widget.Field, filters, start, end)
		previous = a.queryMessages(orgID, metric, widget.Field, filters, prevStart, prevEnd)
	case "contacts":
		current = a.queryContacts(orgID, metric, filters, start, end)
		previous = a.queryContacts(orgID, metric, filters, prevStart, prevEnd)
	case "campaigns":
		current = a.queryCampaigns(orgID, metric, widget.Field, filters, start, end)
		previous = a.queryCampaigns(orgID, metric, widget.Field, filters, prevStart, prevEnd)
	case "transfers":
		current = a.queryTransfers(orgID, metric, widget.Field, filters, start, end)
		previous = a.queryTransfers(orgID, metric, widget.Field, filters, prevStart, prevEnd)
	case "sessions":
		current = a.querySessions(orgID, metric, filters, start, end)
		previous = a.querySessions(orgID, metric, filters, prevStart, prevEnd)
	case "agents":
		current = a.queryAgents(orgID, metric, filters)
		previous = current // roster snapshot — no prior-period delta
	case "calls":
		current = a.queryCalls(orgID, metric, widget.Field, filters, start, end)
		previous = a.queryCalls(orgID, metric, widget.Field, filters, prevStart, prevEnd)
	case "templates":
		current = a.queryTemplates(orgID, metric, filters, start, end)
		previous = a.queryTemplates(orgID, metric, filters, prevStart, prevEnd)
	case "flows":
		current = a.queryFlows(orgID, metric, filters, start, end)
		previous = a.queryFlows(orgID, metric, filters, prevStart, prevEnd)
	case "tags":
		current = a.queryTags(orgID, metric, filters)
		previous = current
	case "opt_outs":
		current = a.queryOptOuts(orgID, metric, filters, start, end)
		previous = a.queryOptOuts(orgID, metric, filters, prevStart, prevEnd)
	case "sla":
		current = a.querySLA(orgID, metric, widget.Field, filters, start, end)
		previous = a.querySLA(orgID, metric, widget.Field, filters, prevStart, prevEnd)
	case "media":
		current = a.queryMedia(orgID, metric, filters, start, end)
		previous = a.queryMedia(orgID, metric, filters, prevStart, prevEnd)
	case "ai":
		current = a.queryAI(orgID, metric, widget.Field, filters, start, end)
		previous = a.queryAI(orgID, metric, widget.Field, filters, prevStart, prevEnd)
	case "webhooks":
		current = a.queryWebhooks(orgID, metric, widget.Field, filters, start, end)
		previous = a.queryWebhooks(orgID, metric, widget.Field, filters, prevStart, prevEnd)
	}

	if metric == "rate" || metric == "ratio" {
		current = a.queryRateOrRatio(orgID, widget, filters, start, end, metric)
		previous = a.queryRateOrRatio(orgID, widget, filters, prevStart, prevEnd, metric)
	}
	if metric == "delta" {
		// value is absolute delta; previous period unused for display value
		current = current - previous
		previous = 0
	}
	return current, previous, stub
}

func applyFilters(dataSource string, query *gorm.DB, filters []FilterInput) *gorm.DB {
	for _, f := range filters {
		query = applyFilter(dataSource, query, f)
	}
	return query
}

func (a *App) queryMessages(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.Message{}).Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("messages", query, filters)
	return a.aggregateQuery(query, "messages", metric, field, "id")
}

func (a *App) queryContacts(orgID uuid.UUID, metric string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.Contact{}).Where("organization_id = ? AND last_message_at >= ? AND last_message_at <= ?", orgID, start, end)
	query = applyFilters("contacts", query, filters)
	return a.aggregateQuery(query, "contacts", metric, "", "id")
}

func (a *App) queryCampaigns(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.BulkMessageCampaign{}).Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("campaigns", query, filters)
	return a.aggregateQuery(query, "campaigns", metric, field, "id")
}

func (a *App) queryTransfers(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.AgentTransfer{}).Where("organization_id = ? AND transferred_at >= ? AND transferred_at <= ?", orgID, start, end)
	query = applyFilters("transfers", query, filters)
	if metric == "avg" || metric == "avg_duration" || metric == "p50" || metric == "p95" || metric == "sum_duration" {
		if field == "" {
			field = "resolution_time"
		}
		return a.aggregateDuration(query, metric, field)
	}
	return a.aggregateQuery(query, "transfers", metric, field, "id")
}

func (a *App) querySessions(orgID uuid.UUID, metric string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.ChatbotSession{}).Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("sessions", query, filters)
	return a.aggregateQuery(query, "sessions", metric, "", "id")
}

func (a *App) queryAgents(orgID uuid.UUID, metric string, filters []FilterInput) float64 {
	query := a.DB.Model(&models.User{}).
		Joins("JOIN team_members ON team_members.user_id = users.id").
		Joins("JOIN teams ON teams.id = team_members.team_id").
		Where("users.organization_id = ? AND team_members.role = ?", orgID, models.TeamRoleAgent).
		Distinct("users.id")

	for _, f := range filters {
		switch f.Field {
		case "is_available":
			query = applyFilter("agents", query, FilterInput{Field: "is_available", Operator: f.Operator, Value: f.Value})
		case "team_id":
			query = query.Where("teams.id = ?", f.Value)
		}
	}

	if metric == "unique" || metric == "count" || metric == "" {
		var count int64
		query.Count(&count)
		return float64(count)
	}
	return 0
}

func (a *App) queryCalls(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.CallLog{}).Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("calls", query, filters)
	if field == "" && (metric == "sum" || metric == "avg" || metric == "sum_duration" || metric == "avg_duration" || metric == "p50" || metric == "p95") {
		field = "duration"
	}
	if metric == "sum_duration" {
		metric = "sum"
	}
	if metric == "avg_duration" {
		metric = "avg"
	}
	return a.aggregateQuery(query, "calls", metric, field, "id")
}

func (a *App) queryTemplates(orgID uuid.UUID, metric string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.Template{}).Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("templates", query, filters)
	return a.aggregateQuery(query, "templates", metric, "", "id")
}

func (a *App) queryFlows(orgID uuid.UUID, metric string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.ChatbotFlow{}).Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("flows", query, filters)
	return a.aggregateQuery(query, "flows", metric, "", "id")
}

func (a *App) queryTags(orgID uuid.UUID, metric string, filters []FilterInput) float64 {
	query := a.DB.Model(&models.Tag{}).Where("organization_id = ?", orgID)
	query = applyFilters("tags", query, filters)
	var count int64
	query.Count(&count)
	_ = metric
	return float64(count)
}

func (a *App) queryOptOuts(orgID uuid.UUID, metric string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.Contact{}).
		Where("organization_id = ? AND marketing_opt_out = true AND updated_at >= ? AND updated_at <= ?", orgID, start, end)
	query = applyFilters("opt_outs", query, filters)
	return a.aggregateQuery(query, "opt_outs", metric, "", "id")
}

func (a *App) querySLA(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.AgentTransfer{}).Where("organization_id = ? AND transferred_at >= ? AND transferred_at <= ?", orgID, start, end)
	query = applyFilters("sla", query, filters)
	if metric == "avg" || metric == "avg_duration" || metric == "p50" || metric == "p95" || metric == "sum_duration" {
		if field == "" {
			field = "resolution_time"
		}
		return a.aggregateDuration(query, metric, field)
	}
	return a.aggregateQuery(query, "sla", metric, field, "id")
}

func (a *App) queryMedia(orgID uuid.UUID, metric string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end).
		Where("message_type IN ? OR (media_url IS NOT NULL AND media_url != '')",
			[]string{"image", "audio", "video", "document", "sticker", "voice"})
	query = applyFilters("media", query, filters)
	return a.aggregateQuery(query, "media", metric, "", "id")
}

func (a *App) queryAI(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.AIUsageLog{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("ai", query, filters)
	if field == "" && (metric == "sum" || metric == "avg" || metric == "p50" || metric == "p95") {
		field = "total_tokens"
	}
	return a.aggregateQuery(query, "ai", metric, field, "id")
}

func (a *App) queryWebhooks(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	query := a.DB.Model(&models.WebhookDelivery{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, start, end)
	query = applyFilters("webhooks", query, filters)
	if field == "" && (metric == "sum" || metric == "avg" || metric == "p50" || metric == "p95") {
		field = "duration_ms"
	}
	return a.aggregateQuery(query, "webhooks", metric, field, "id")
}

func (a *App) aggregateQuery(query *gorm.DB, dataSource, metric, field, uniqueCol string) float64 {
	switch metric {
	case "count", "":
		var count int64
		query.Count(&count)
		return float64(count)
	case "unique":
		var count int64
		col := uniqueCol
		if col == "" {
			col = "id"
		}
		query.Distinct(col).Count(&count)
		return float64(count)
	case "sum", "avg":
		if field == "" || !allowedAggregateFields[dataSource][field] {
			return 0
		}
		var val float64
		if metric == "sum" {
			query.Select("COALESCE(SUM(" + field + "), 0)").Scan(&val)
		} else {
			query.Select("COALESCE(AVG(" + field + "), 0)").Scan(&val)
		}
		return val
	case "p50", "p95":
		if field == "" || !allowedAggregateFields[dataSource][field] {
			return 0
		}
		frac := percentileFraction(metric)
		if !dialect.Of(query).SupportsPercentileCont() {
			return percentileOf(query, field, frac)
		}
		var val float64
		query.Select(fmt.Sprintf("COALESCE(PERCENTILE_CONT(%f) WITHIN GROUP (ORDER BY %s), 0)", frac, field)).Scan(&val)
		return val
	case "sum_duration", "avg_duration":
		return 0
	default:
		var count int64
		query.Count(&count)
		return float64(count)
	}
}

func (a *App) aggregateDuration(query *gorm.DB, metric, field string) float64 {
	d := dialect.Of(a.DB)
	expr := ""
	switch field {
	case "resolution_time":
		expr = d.MinutesBetween("transferred_at", "resumed_at")
		query = query.Where("resumed_at IS NOT NULL")
	case "queue_time":
		expr = d.MinutesBetween("transferred_at", "picked_up_at")
		query = query.Where("picked_up_at IS NOT NULL")
	case "first_response_time":
		expr = d.MinutesBetween("transferred_at", "first_response_at")
		query = query.Where("first_response_at IS NOT NULL")
	default:
		return 0
	}

	if (metric == "p50" || metric == "p95") && !d.SupportsPercentileCont() {
		return percentileOf(query, expr, percentileFraction(metric))
	}
	var val float64
	switch metric {
	case "avg", "avg_duration":
		query.Select("COALESCE(AVG(" + expr + "), 0)").Scan(&val)
	case "sum", "sum_duration":
		query.Select("COALESCE(SUM(" + expr + "), 0)").Scan(&val)
	case "p50":
		query.Select("COALESCE(PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY " + expr + "), 0)").Scan(&val)
	case "p95":
		query.Select("COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY " + expr + "), 0)").Scan(&val)
	}
	return val
}

// percentileOf computes PERCENTILE_CONT(frac) over expr in Go, for drivers
// without the SQL aggregate. NULLs are ignored and no rows give 0, as in SQL.
func percentileOf(query *gorm.DB, expr string, frac float64) float64 {
	var values []float64
	query.Where(expr+" IS NOT NULL").Pluck(expr, &values)
	return percentileCont(values, frac)
}

// percentileCont interpolates linearly between the two values closest to
// frac, exactly as PostgreSQL's PERCENTILE_CONT does.
func percentileCont(values []float64, frac float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	pos := frac * float64(len(values)-1)
	lo, hi := math.Floor(pos), math.Ceil(pos)
	first, second := values[int(lo)], values[int(hi)]
	return first + (second-first)*(pos-lo)
}

func percentileFraction(metric string) float64 {
	if metric == "p95" {
		return 0.95
	}
	return 0.5
}

// queryRateOrRatio computes numerator/denominator from widget.config.
// config.numerator_filter / config.denominator_filter: [{field,operator,value}]
func (a *App) queryRateOrRatio(orgID uuid.UUID, widget models.Widget, baseFilters []FilterInput, start, end time.Time, metric string) float64 {
	cfg := map[string]any(widget.Config)
	numFilters := append([]FilterInput{}, baseFilters...)
	denFilters := append([]FilterInput{}, baseFilters...)
	numFilters = append(numFilters, parseConfigFilters(cfg, "numerator_filter")...)
	denFilters = append(denFilters, parseConfigFilters(cfg, "denominator_filter")...)
	if widget.DataSource == "webhooks" && len(numFilters) == len(baseFilters) {
		// Webhook rate defaults to success rate.
		numFilters = append(numFilters, FilterInput{Field: "success", Operator: "equals", Value: "true"})
	}

	numWidget := widget
	numWidget.Metric = "count"
	denWidget := widget
	denWidget.Metric = "count"

	num, _, _ := a.queryWidgetMetricOnce(orgID, numWidget, numFilters, start, end)
	den, _, _ := a.queryWidgetMetricOnce(orgID, denWidget, denFilters, start, end)
	if den == 0 {
		return 0
	}
	if metric == "rate" {
		return (num / den) * 100
	}
	return num / den
}

func (a *App) queryWidgetMetricOnce(orgID uuid.UUID, widget models.Widget, filters []FilterInput, start, end time.Time) (float64, float64, bool) {
	return a.queryWidgetMetric(orgID, widget, filters, start, end, start, start)
}

func parseConfigFilters(cfg map[string]any, key string) []FilterInput {
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]FilterInput, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, FilterInput{
			Field:    widgetGetString(m, "field"),
			Operator: widgetGetString(m, "operator"),
			Value:    widgetGetString(m, "value"),
		})
	}
	return out
}
