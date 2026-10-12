package handlers

import (
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/database/dialect"
	"github.com/trysoft/whatomate/internal/models"
)

func (a *App) getQueueItems(orgID uuid.UUID, filters []FilterInput) []QueueItem {
	d := dialect.Of(a.DB)
	query := a.DB.Table("agent_transfers t").
		Select(`t.id, t.contact_id, COALESCE(c.profile_name, c.phone_number) as label, t.source,
			`+d.MinutesBetween("t.transferred_at", d.Now())+` as wait_mins, t.transferred_at`).
		Joins("LEFT JOIN contacts c ON c.id = t.contact_id").
		Where("t.organization_id = ? AND t.status = ?", orgID, models.TransferStatusActive).
		Order("t.transferred_at ASC").
		Limit(50)

	for _, f := range filters {
		if f.Field == "team_id" || f.Field == "agent_id" || f.Field == "source" {
			condition, values, ok := buildFilterSQL(d, "transfers", f)
			if ok {
				query = query.Where("t."+condition, values...)
			}
		}
	}

	type row struct {
		ID            string
		ContactID     string
		Label         string
		Source        string
		WaitMins      float64
		TransferredAt time.Time
	}
	var rows []row
	query.Scan(&rows)

	out := make([]QueueItem, len(rows))
	for i, r := range rows {
		out[i] = QueueItem{
			ID:            r.ID,
			ContactID:     r.ContactID,
			Label:         r.Label,
			Source:        r.Source,
			WaitMins:      r.WaitMins,
			TransferredAt: r.TransferredAt.Format(time.RFC3339),
		}
	}
	return out
}

func (a *App) getAgentStatusItems(orgID uuid.UUID, filters []FilterInput) []AgentStatusItem {
	query := a.DB.Table("users u").
		Select(`u.id, u.full_name as name, u.is_available,
			(SELECT COUNT(*) FROM agent_transfers t WHERE t.agent_id = u.id AND t.status = 'active') as active_transfers`).
		Joins("JOIN team_members tm ON tm.user_id = u.id").
		Joins("JOIN teams te ON te.id = tm.team_id").
		Where("u.organization_id = ? AND tm.role = ?", orgID, models.TeamRoleAgent).
		Group("u.id, u.full_name, u.is_available").
		Order("u.full_name ASC")

	for _, f := range filters {
		switch f.Field {
		case "is_available":
			query = query.Where("u.is_available = ?", f.Value)
		case "team_id":
			query = query.Where("te.id = ?", f.Value)
		}
	}

	type row struct {
		ID              string
		Name            string
		IsAvailable     bool
		ActiveTransfers int64
	}
	var rows []row
	query.Scan(&rows)

	out := make([]AgentStatusItem, len(rows))
	for i, r := range rows {
		out[i] = AgentStatusItem(r)
	}
	return out
}

func (a *App) getAlertItems(orgID uuid.UUID, start, end time.Time) []AlertItem {
	alerts := make([]AlertItem, 0)

	type msgRow struct {
		ID        string
		Label     string
		Status    string
		CreatedAt time.Time
		ContactID string
	}
	var failed []msgRow
	a.DB.Raw(`
		SELECT m.id, COALESCE(c.profile_name, c.phone_number) as label, m.status, m.created_at, m.contact_id
		FROM messages m LEFT JOIN contacts c ON c.id = m.contact_id
		WHERE m.organization_id = ? AND m.status = ? AND m.created_at >= ? AND m.created_at <= ?
		ORDER BY m.created_at DESC LIMIT 20
	`, orgID, models.MessageStatusFailed, start, end).Scan(&failed)

	for _, r := range failed {
		alerts = append(alerts, AlertItem{
			Severity: "error",
			Title:    "Failed message",
			Subtitle: r.Label,
			At:       r.CreatedAt.Format(time.RFC3339),
			Href:     "/chat/" + r.ContactID,
		})
	}

	type slaRow struct {
		ID        string
		Label     string
		CreatedAt time.Time
		ContactID string
	}
	var breaches []slaRow
	a.DB.Raw(`
		SELECT t.id, COALESCE(c.profile_name, c.phone_number) as label, t.transferred_at as created_at, t.contact_id
		FROM agent_transfers t LEFT JOIN contacts c ON c.id = t.contact_id
		WHERE t.organization_id = ? AND t.sla_breached = true AND t.transferred_at >= ? AND t.transferred_at <= ?
		ORDER BY t.transferred_at DESC LIMIT 20
	`, orgID, start, end).Scan(&breaches)

	for _, r := range breaches {
		alerts = append(alerts, AlertItem{
			Severity: "warning",
			Title:    "SLA breach",
			Subtitle: r.Label,
			At:       r.CreatedAt.Format(time.RFC3339),
			Href:     "/chat/" + r.ContactID,
		})
	}

	if len(alerts) > 25 {
		alerts = alerts[:25]
	}
	return alerts
}

func (a *App) getLeaderboard(orgID uuid.UUID, widget models.Widget, filters []FilterInput, start, end time.Time) []LeaderboardEntry {
	source := widget.DataSource
	if source == "" {
		source = "transfers"
	}

	var query string
	var args []any

	switch source {
	case "agents", "transfers", "sla":
		query = `
			SELECT COALESCE(u.full_name, 'Unassigned') as label, COUNT(*) as value
			FROM agent_transfers t
			LEFT JOIN users u ON u.id = t.agent_id
			WHERE t.organization_id = ? AND t.transferred_at >= ? AND t.transferred_at <= ?
			  AND t.agent_id IS NOT NULL`
		args = []any{orgID, start, end}
		query, args = appendFilterSQL(dialect.Of(a.DB), "transfers", query, args, filters)
		query += ` GROUP BY u.full_name ORDER BY value DESC LIMIT 10`
	case "calls":
		query = `
			SELECT COALESCE(u.full_name, 'Unassigned') as label, COUNT(*) as value
			FROM call_logs c
			LEFT JOIN users u ON u.id = c.agent_id
			WHERE c.organization_id = ? AND c.created_at >= ? AND c.created_at <= ?
			  AND c.agent_id IS NOT NULL`
		args = []any{orgID, start, end}
		query, args = appendFilterSQL(dialect.Of(a.DB), "calls", query, args, filters)
		query += ` GROUP BY u.full_name ORDER BY value DESC LIMIT 10`
	default:
		points := a.getGroupedData(orgID, widget, filters, start, end)
		out := make([]LeaderboardEntry, 0, len(points))
		for i, p := range points {
			if i >= 10 {
				break
			}
			out = append(out, LeaderboardEntry{Rank: i + 1, Label: p.Label, Value: p.Value})
		}
		return out
	}

	type row struct {
		Label string
		Value int64
	}
	var rows []row
	a.DB.Raw(query, args...).Scan(&rows)
	out := make([]LeaderboardEntry, len(rows))
	for i, r := range rows {
		out[i] = LeaderboardEntry{Rank: i + 1, Label: r.Label, Value: float64(r.Value)}
	}
	return out
}

func (a *App) getHeatmapData(orgID uuid.UUID, widget models.Widget, filters []FilterInput, start, end time.Time) HeatmapData {
	xLabels := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	yLabels := make([]string, 24)
	for h := 0; h < 24; h++ {
		yLabels[h] = fmt.Sprintf("%02d", h)
	}
	cells := make([][]float64, 24)
	for i := range cells {
		cells[i] = make([]float64, 7)
	}

	tableName, dateField, ok := resolveDataSourceTable(widget.DataSource)
	if !ok {
		return HeatmapData{XLabels: xLabels, YLabels: yLabels, Cells: cells}
	}

	d := dialect.Of(a.DB)
	query := fmt.Sprintf(`
		SELECT %s as dow, %s as hour, COUNT(*) as count
		FROM %s
		WHERE organization_id = ? AND %s >= ? AND %s <= ?
	`, d.DayOfWeek(dateField), d.HourOf(dateField), tableName, dateField, dateField)
	args := []any{orgID, start, end}
	query, args = appendFilterSQL(d, widget.DataSource, query, args, filters)
	if widget.DataSource == "media" {
		query += ` AND (message_type IN ('image','audio','video','document','sticker','voice') OR (media_url IS NOT NULL AND media_url != ''))`
	}
	query += fmt.Sprintf(` GROUP BY %s, %s`, d.DayOfWeek(dateField), d.HourOf(dateField))

	type row struct {
		Dow   int
		Hour  int
		Count int64
	}
	var rows []row
	a.DB.Raw(query, args...).Scan(&rows)
	for _, r := range rows {
		if r.Hour >= 0 && r.Hour < 24 && r.Dow >= 0 && r.Dow < 7 {
			cells[r.Hour][r.Dow] = float64(r.Count)
		}
	}
	return HeatmapData{XLabels: xLabels, YLabels: yLabels, Cells: cells}
}

func (a *App) getFunnelData(orgID uuid.UUID, widget models.Widget, filters []FilterInput, start, end time.Time) []FunnelStage {
	if widget.DataSource == "campaigns" || widget.GroupByField == "message_status" {
		points := a.getCampaignMessageStatusData(orgID, filters, start, end)
		out := make([]FunnelStage, len(points))
		for i, p := range points {
			out[i] = FunnelStage{Stage: p.Label, Value: p.Value}
		}
		return out
	}

	cfg := map[string]any(widget.Config)
	if stages, ok := cfg["stages"].([]any); ok && len(stages) > 0 {
		out := make([]FunnelStage, 0, len(stages))
		for _, s := range stages {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			label := widgetGetString(m, "label")
			if label == "" {
				label = widgetGetString(m, "stage")
			}
			stageFilters := append([]FilterInput{}, filters...)
			stageFilters = append(stageFilters, parseConfigFilters(m, "filters")...)
			w := widget
			w.Metric = "count"
			val, _, _ := a.queryWidgetMetricOnce(orgID, w, stageFilters, start, end)
			out = append(out, FunnelStage{Stage: label, Value: val})
		}
		return out
	}

	points := a.getGroupedData(orgID, widget, filters, start, end)
	out := make([]FunnelStage, len(points))
	for i, p := range points {
		out[i] = FunnelStage{Stage: p.Label, Value: p.Value}
	}
	return out
}

func (a *App) getKPIStrip(orgID uuid.UUID, widget models.Widget, filters []FilterInput, start, end, prevStart, prevEnd time.Time) []KPIItem {
	cfg := map[string]any(widget.Config)
	raw, ok := cfg["kpis"].([]any)
	if !ok || len(raw) == 0 {
		cur, prev, _ := a.queryWidgetMetric(orgID, widget, filters, start, end, prevStart, prevEnd)
		ch := calculatePercentageChange(int64(prev), int64(cur))
		return []KPIItem{{Label: widget.Name, Value: cur, Change: &ch}}
	}

	out := make([]KPIItem, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label := widgetGetString(m, "label")
		w := widget
		if ds := widgetGetString(m, "data_source"); ds != "" {
			w.DataSource = ds
		}
		if met := widgetGetString(m, "metric"); met != "" {
			w.Metric = met
		}
		if fld := widgetGetString(m, "field"); fld != "" {
			w.Field = fld
		}
		kpiFilters := append([]FilterInput{}, filters...)
		kpiFilters = append(kpiFilters, parseConfigFilters(m, "filters")...)
		cur, prev, _ := a.queryWidgetMetric(orgID, w, kpiFilters, start, end, prevStart, prevEnd)
		if w.Metric == "rate" {
			ch := math.Round((cur-prev)*10) / 10
			out = append(out, KPIItem{Label: label, Value: math.Round(cur*10) / 10, Change: &ch, Unit: "%"})
			continue
		}
		ch := calculatePercentageChange(int64(prev), int64(cur))
		out = append(out, KPIItem{Label: label, Value: cur, Change: &ch})
	}
	return out
}
