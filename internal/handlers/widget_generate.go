package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	aiWidgetMaxTokens  = aiFlowGenerateMaxTokens // reasoning models spend part of this before answering
	aiWidgetMaxFilters = 5
	aiWidgetMaxKPIs    = 4
	aiWidgetMaxStages  = 6
)

// aiWidgetDisplayTypes are the display types the AI may pick. shortcuts is
// left out (it is a navigation widget, not a metric) and iframe needs a URL the
// user must supply.
var aiWidgetDisplayTypes = []string{
	"number", "percentage", "chart", "table", "sparkline", "gauge",
	"leaderboard", "heatmap", "funnel", "kpi_strip", "alert_list", "queue", "agent_status",
}

// aiWidgetFieldValues lists known values so generated filters match real data.
var aiWidgetFieldValues = map[string]string{
	"messages.status":       "pending, sent, delivered, read, failed, received",
	"messages.direction":    "incoming, outgoing",
	"messages.message_type": "text, image, video, audio, document, template, interactive, flow, reaction, location, contact",
	"media.direction":       "incoming, outgoing",
	"media.message_type":    "image, video, audio, document, sticker, voice",
	"campaigns.status":      "draft, scheduled, queued, processing, paused, completed, cancelled, failed",
	"transfers.status":      "active, resumed, expired",
	"transfers.source":      "manual, flow, keyword, chatbot_disabled",
	"sla.status":            "active, resumed, expired",
	"sla.sla_breached":      "true, false",
	"sessions.status":       "active, completed, cancelled, timeout",
	"calls.direction":       "incoming, outgoing",
	"calls.status":          "ringing, answered, completed, missed, rejected, failed, transferring, initiating, accepted",
	"templates.status":      "PENDING, APPROVED, REJECTED",
	"templates.category":    "MARKETING, UTILITY, AUTHENTICATION",
	"agents.is_available":   "true, false",
	"flows.is_enabled":      "true, false",
	"contacts.is_read":      "true, false",
	"ai.purpose":            "chatbot_reply, flow_generate, ai_complete, widget_generate, chat_assist, handoff_summary, transcribe, custom_action_generate, canned_response_generate, template_generate",
	"ai.provider":           "openai, anthropic, google, openrouter",
	"webhooks.success":      "true, false",
}

var aiWidgetDataSourceNotes = map[string]string{
	"messages":  "WhatsApp messages",
	"contacts":  "contacts active in the period (by last message time)",
	"campaigns": "bulk message campaigns",
	"transfers": "chats handed to human agents",
	"sessions":  "chatbot sessions",
	"agents":    "current agent roster (not period based)",
	"calls":     "WhatsApp calls (duration in seconds)",
	"templates": "message templates",
	"flows":     "chatbot flows",
	"tags":      "contact tags (not period based)",
	"opt_outs":  "contacts who opted out of marketing",
	"sla":       "agent transfers with SLA timings",
	"media":     "media messages",
	"ai":        "AI token usage",
	"webhooks":  "outbound webhook deliveries (success = delivered after retries)",
}

func buildAIWidgetSystemPrompt() string {
	var b strings.Builder
	b.WriteString(`You design dashboard widgets for Whatomate, a WhatsApp business platform.

Return ONLY valid JSON (no markdown fences, no commentary) with this shape:
{
  "name": "Failed messages",
  "description": "Outgoing messages that failed this period",
  "data_source": "messages",
  "metric": "count",
  "field": "",
  "display_type": "number",
  "chart_type": "",
  "group_by_field": "",
  "filters": [{"field": "status", "operator": "equals", "value": "failed"}],
  "show_change": true,
  "color": "red",
  "config": {}
}

Data sources and their filter / group-by fields:
`)
	sources := make([]string, 0, len(widgetDataSources))
	for s := range widgetDataSources {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	for _, s := range sources {
		fmt.Fprintf(&b, "- %s (%s): fields %s", s, aiWidgetDataSourceNotes[s], strings.Join(widgetDataSources[s], ", "))
		agg := make([]string, 0)
		for f := range allowedAggregateFields[s] {
			agg = append(agg, f)
		}
		sort.Strings(agg)
		if len(agg) > 0 {
			fmt.Fprintf(&b, "; numeric fields for sum/avg/p50/p95: %s", strings.Join(agg, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nKnown field values:\n")
	keys := make([]string, 0, len(aiWidgetFieldValues))
	for k := range aiWidgetFieldValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %s\n", k, aiWidgetFieldValues[k])
	}
	ops := make([]string, 0, len(widgetOperatorDefs))
	for _, o := range widgetOperatorDefs {
		ops = append(ops, o["value"])
	}
	fmt.Fprintf(&b, `
Rules:
- metric is one of: %s. "count" counts rows; "unique" counts distinct rows; "sum"/"avg"/"p50"/"p95" need a numeric "field"; "rate" is numerator/denominator x 100 and needs config.numerator_filter (a filters array), optionally config.denominator_filter; "delta" is current minus previous period.
- display_type is one of: %s.
  - number / percentage: one big value with change vs the previous period.
  - chart: set chart_type to line, bar or pie. Without group_by_field it is a daily time series; with group_by_field it breaks down by that field (use bar or pie).
  - table: recent rows, or grouped counts when group_by_field is set.
  - sparkline: a number with a small daily trend line.
  - gauge: config {"target": <number>} and the value is shown against the target.
  - leaderboard: top agents; use data_source transfers or calls.
  - heatmap: activity by weekday and hour.
  - funnel: data_source campaigns (message status funnel), or config {"stages": [{"label": "...", "filters": [...]}]} with 2-6 stages on the same data source.
  - kpi_strip: config {"kpis": [{"label": "...", "metric": "count", "field": "", "filters": [...]}]} with 2-4 KPIs on the widget's data_source. KPI metric may be "rate" (a KPI rate on webhooks defaults to success rate).
  - alert_list / queue / agent_status: live lists (alerts, waiting transfers, agent availability); use data_source transfers for queue and agents for agent_status.
- filters: at most %d, each {"field","operator","value"} where field is one of the data source's fields and operator is one of: %s. Values are strings; for in / not_in use comma-separated values; for between use "a,b".
- group_by_field must be one of the data source's fields (only for chart or table).
- color is one of: %s.
- name is short (max 60 chars). description is one sentence.
- Pick the single widget that best answers the request. Never invent data sources, fields or values that are not listed.`,
		strings.Join(widgetMetrics, ", "),
		strings.Join(aiWidgetDisplayTypes, ", "),
		aiWidgetMaxFilters,
		strings.Join(ops, ", "),
		strings.Join(namedWidgetColorList(), ", "),
	)
	return b.String()
}

// GenerateWidget drafts a dashboard widget from a plain-language description.
// The draft is not saved; the dashboard opens it in the widget dialog.
func (a *App) GenerateWidget(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAnalytics, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "prompt is required", nil, "")
	}
	if utf8.RuneCountInString(prompt) > aiFlowGenerateMaxPromptRunes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "prompt is too long", nil, "")
	}

	settings, err := a.getChatbotSettingsCached(orgID, "")
	if err != nil || settings == nil {
		a.Log.Error("GenerateWidget: load settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load AI settings", nil, "")
	}
	if !settings.AI.Enabled || settings.AI.Provider == "" || settings.AI.APIKey == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"AI is not configured. Enable AI and add an API key in Settings → AI.", nil, "")
	}
	if settings.AI.Model == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "AI model is not configured in Settings → AI", nil, "")
	}

	userMsg := "Design a dashboard widget for this request:\n\n" + prompt
	raw, err := a.completeAIChat(settings, buildAIWidgetSystemPrompt(), userMsg, aiWidgetMaxTokens, models.AIUsagePurposeWidgetGenerate)
	if err != nil {
		a.Log.Error("GenerateWidget: LLM call failed", "error", err, "provider", settings.AI.Provider)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "AI generation failed: "+err.Error(), nil, "")
	}

	draft, err := parseAIWidgetDraft(raw)
	if err != nil {
		a.Log.Error("GenerateWidget: parse draft", "error", err, "raw_len", len(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway,
			"AI returned an invalid widget. Try again with a clearer description.", nil, "")
	}
	return r.SendEnvelope(draft)
}

// aiWidgetDraft mirrors the widget dialog form.
type aiWidgetDraft struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	DataSource   string         `json:"data_source"`
	Metric       string         `json:"metric"`
	Field        string         `json:"field"`
	Filters      []FilterInput  `json:"filters"`
	DisplayType  string         `json:"display_type"`
	ChartType    string         `json:"chart_type"`
	GroupByField string         `json:"group_by_field"`
	ShowChange   bool           `json:"show_change"`
	Color        string         `json:"color"`
	Size         string         `json:"size"`
	Config       map[string]any `json:"config"`
}

func parseAIWidgetDraft(raw string) (*aiWidgetDraft, error) {
	cleaned := extractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("no JSON object in response")
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(cleaned), &in); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	if w, ok := in["widget"].(map[string]any); ok {
		in = w
	}

	d := &aiWidgetDraft{
		Name:        truncateRunes(strings.TrimSpace(stringField(in, "name")), 60),
		Description: truncateRunes(strings.TrimSpace(stringField(in, "description")), 300),
		DataSource:  strings.ToLower(strings.TrimSpace(stringField(in, "data_source"))),
		Metric:      strings.ToLower(strings.TrimSpace(stringField(in, "metric"))),
		DisplayType: strings.ToLower(strings.TrimSpace(stringField(in, "display_type"))),
		ShowChange:  true,
		Config:      map[string]any{},
	}
	if _, ok := widgetDataSources[d.DataSource]; !ok {
		return nil, fmt.Errorf("unknown data source %q", d.DataSource)
	}
	if !contains(aiWidgetDisplayTypes, d.DisplayType) {
		d.DisplayType = "number"
	}
	if !contains(widgetMetrics, d.Metric) {
		d.Metric = "count"
	}
	if f := strings.TrimSpace(stringField(in, "field")); allowedAggregateFields[d.DataSource][f] {
		d.Field = f
	}
	if (d.Metric == "sum" || d.Metric == "avg" || d.Metric == "p50" || d.Metric == "p95") && d.Field == "" {
		d.Metric = "count"
	}
	d.Filters = sanitizeAIWidgetFilters(d.DataSource, in["filters"])

	if gb := strings.TrimSpace(stringField(in, "group_by_field")); isAllowedGroupByField(d.DataSource, gb) {
		d.GroupByField = gb
	}
	cfg, _ := in["config"].(map[string]any)
	d.applyDisplayRules(strings.ToLower(strings.TrimSpace(stringField(in, "chart_type"))), cfg)

	if v, ok := in["show_change"].(bool); ok {
		d.ShowChange = v
	}
	if c := strings.ToLower(strings.TrimSpace(stringField(in, "color"))); c != "" && widgetNamedColors[c] {
		d.Color = c
	} else {
		d.Color = "blue"
	}
	if d.Name == "" {
		d.Name = truncateRunes(strings.ToUpper(d.DataSource[:1])+d.DataSource[1:], 60)
	}
	return d, nil
}

// applyDisplayRules keeps chart/group-by/config consistent with the display
// type, falling back to a chart when a display can't be served by the source.
func (d *aiWidgetDraft) applyDisplayRules(chartType string, cfg map[string]any) {
	switch d.DisplayType {
	case "leaderboard":
		if d.DataSource != "transfers" && d.DataSource != "calls" && d.DataSource != "agents" && d.DataSource != "sla" {
			d.DisplayType = "chart"
			chartType = "bar"
		}
	case "funnel":
		if d.DataSource != "campaigns" {
			stages := sanitizeAIWidgetStages(d.DataSource, cfg["stages"])
			if len(stages) < 2 {
				d.DisplayType = "chart"
				chartType = "bar"
			} else {
				d.Config["stages"] = stages
			}
		}
	case "kpi_strip":
		kpis := sanitizeAIWidgetKPIs(d.DataSource, cfg["kpis"])
		if len(kpis) == 0 {
			d.DisplayType = "number"
		} else {
			d.Config["kpis"] = kpis
		}
	case "gauge":
		target := 100.0
		if t, ok := numberField(cfg, "target"); ok && t > 0 {
			target = t
		}
		d.Config["target"] = target
	case "queue":
		d.DataSource = "transfers"
		d.Metric = "count"
	case "agent_status":
		d.DataSource = "agents"
		d.Metric = "count"
	}

	if d.DisplayType == "chart" {
		switch chartType {
		case "line", "bar", "pie":
			d.ChartType = chartType
		default:
			d.ChartType = "line"
		}
		if d.ChartType == "pie" && d.GroupByField == "" {
			d.ChartType = "line"
		}
	}
	if d.DisplayType != "chart" && d.DisplayType != "table" {
		d.GroupByField = ""
	}
	if d.Metric == "rate" || d.Metric == "ratio" {
		if nf := sanitizeAIWidgetFilters(d.DataSource, cfg["numerator_filter"]); len(nf) > 0 {
			d.Config["numerator_filter"] = filtersToConfig(nf)
		} else if d.DataSource != "webhooks" {
			d.Metric = "count"
		}
		if df := sanitizeAIWidgetFilters(d.DataSource, cfg["denominator_filter"]); len(df) > 0 && (d.Metric == "rate" || d.Metric == "ratio") {
			d.Config["denominator_filter"] = filtersToConfig(df)
		}
	}

	switch d.DisplayType {
	case "chart", "leaderboard", "heatmap", "funnel", "kpi_strip", "alert_list", "queue", "agent_status":
		d.Size = "medium"
	case "table":
		d.Size = "large"
	default:
		d.Size = "small"
	}
}

func sanitizeAIWidgetFilters(dataSource string, raw any) []FilterInput {
	arr, _ := raw.([]any)
	out := make([]FilterInput, 0, len(arr))
	validOps := make(map[string]bool, len(widgetOperatorDefs))
	for _, o := range widgetOperatorDefs {
		validOps[o["value"]] = true
	}
	for _, item := range arr {
		if len(out) >= aiWidgetMaxFilters {
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		field := strings.TrimSpace(stringField(m, "field"))
		if !allowedFilterFields[dataSource][field] {
			continue
		}
		op := strings.ToLower(strings.TrimSpace(stringField(m, "operator")))
		if op == "" {
			op = "equals"
		}
		if !validOps[op] {
			continue
		}
		value := truncateRunes(strings.TrimSpace(scalarString(m["value"])), 200)
		if value == "" && op != "is_null" && op != "is_not_null" {
			continue
		}
		out = append(out, FilterInput{Field: field, Operator: op, Value: value})
	}
	return out
}

func sanitizeAIWidgetKPIs(dataSource string, raw any) []any {
	arr, _ := raw.([]any)
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		if len(out) >= aiWidgetMaxKPIs {
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label := truncateRunes(strings.TrimSpace(stringField(m, "label")), 40)
		if label == "" {
			continue
		}
		kpi := map[string]any{"label": label}
		metric := strings.ToLower(strings.TrimSpace(stringField(m, "metric")))
		field := strings.TrimSpace(stringField(m, "field"))
		switch {
		case metric == "rate" && dataSource == "webhooks":
			kpi["metric"] = "rate"
		case (metric == "sum" || metric == "avg" || metric == "p50" || metric == "p95") && allowedAggregateFields[dataSource][field]:
			kpi["metric"] = metric
			kpi["field"] = field
		case metric == "unique":
			kpi["metric"] = "unique"
		default:
			kpi["metric"] = "count"
		}
		if f := sanitizeAIWidgetFilters(dataSource, m["filters"]); len(f) > 0 {
			kpi["filters"] = filtersToConfig(f)
		}
		out = append(out, kpi)
	}
	return out
}

func sanitizeAIWidgetStages(dataSource string, raw any) []any {
	arr, _ := raw.([]any)
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		if len(out) >= aiWidgetMaxStages {
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label := truncateRunes(strings.TrimSpace(firstNonEmpty(stringField(m, "label"), stringField(m, "stage"))), 40)
		if label == "" {
			continue
		}
		stage := map[string]any{"label": label}
		if f := sanitizeAIWidgetFilters(dataSource, m["filters"]); len(f) > 0 {
			stage["filters"] = filtersToConfig(f)
		}
		out = append(out, stage)
	}
	return out
}

func filtersToConfig(filters []FilterInput) []any {
	out := make([]any, len(filters))
	for i, f := range filters {
		out[i] = map[string]any{"field": f.Field, "operator": f.Operator, "value": f.Value}
	}
	return out
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, p := range t {
			if s := scalarString(p); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

func numberField(m map[string]any, key string) (float64, bool) {
	switch t := m[key].(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
