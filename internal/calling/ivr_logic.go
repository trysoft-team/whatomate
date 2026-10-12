package calling

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// resolveIVRLeftValue returns the left-hand value for a condition node.
// Config field may be a bare variable name or a {{template}}.
func resolveIVRLeftValue(cfg map[string]any, ctx *IVRContext) string {
	field, _ := cfg["field"].(string)
	field = strings.TrimSpace(field)
	if field == "" {
		field, _ = cfg["variable"].(string)
		field = strings.TrimSpace(field)
	}
	if field == "" {
		return ""
	}
	vars := map[string]string{}
	if ctx != nil && ctx.Variables != nil {
		vars = ctx.Variables
	}
	if strings.Contains(field, "{{") {
		return interpolateTemplate(field, vars)
	}
	if v, ok := vars[field]; ok {
		return v
	}
	return ""
}

func matchIVRCondition(cfg map[string]any, left string) bool {
	op, _ := cfg["operator"].(string)
	op = strings.ToLower(strings.TrimSpace(op))
	if op == "" {
		op = "equals"
	}
	right, _ := cfg["value"].(string)
	caseSensitive, _ := cfg["case_sensitive"].(bool)
	leftCmp, rightCmp := left, right
	if !caseSensitive {
		leftCmp = strings.ToLower(left)
		rightCmp = strings.ToLower(right)
	}

	switch op {
	case "equals", "eq", "exact", "text_exact":
		return leftCmp == rightCmp
	case "not_equals", "neq", "not_eq":
		return leftCmp != rightCmp
	case "contains", "text_contains":
		return strings.Contains(leftCmp, rightCmp)
	case "not_contains":
		return !strings.Contains(leftCmp, rightCmp)
	case "starts_with", "startswith", "text_starts_with":
		return strings.HasPrefix(leftCmp, rightCmp)
	case "ends_with", "endswith", "text_ends_with":
		return strings.HasSuffix(leftCmp, rightCmp)
	case "empty", "is_empty":
		return strings.TrimSpace(left) == ""
	case "not_empty", "is_not_empty":
		return strings.TrimSpace(left) != ""
	case "regex", "matches":
		pattern := right
		if !caseSensitive && pattern != "" && !strings.HasPrefix(pattern, "(?i)") {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(left)
	case "gt", "number_gt", "number_greater":
		ln, lok := parseIVRNumber(left)
		rn, rok := parseIVRNumber(right)
		return lok && rok && ln > rn
	case "lt", "number_lt", "number_less":
		ln, lok := parseIVRNumber(left)
		rn, rok := parseIVRNumber(right)
		return lok && rok && ln < rn
	case "gte", "number_gte":
		ln, lok := parseIVRNumber(left)
		rn, rok := parseIVRNumber(right)
		return lok && rok && ln >= rn
	case "lte", "number_lte":
		ln, lok := parseIVRNumber(left)
		rn, rok := parseIVRNumber(right)
		return lok && rok && ln <= rn
	default:
		return leftCmp == rightCmp
	}
}

func parseIVRNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// ivrProtectedStartVars cannot be overwritten by Start-node default assignments.
var ivrProtectedStartVars = map[string]struct{}{
	"caller_phone": {},
	"caller_name":  {},
	"call_id":      {},
}

// applyIVRSetVariables applies set_variable assignments into ctx.Variables.
// Each assignment is {name, value}; value is template-interpolated.
func applyIVRSetVariables(cfg map[string]any, ctx *IVRContext) int {
	return applyIVRAssignments(cfg, ctx, nil)
}

// applyIVRStartDefaults applies Start-node default variable assignments.
// Values are template-interpolated against existing session vars (builtins first).
// Built-ins caller_phone / caller_name / call_id are never overwritten.
func applyIVRStartDefaults(cfg map[string]any, ctx *IVRContext) int {
	return applyIVRAssignments(cfg, ctx, ivrProtectedStartVars)
}

func applyIVRAssignments(cfg map[string]any, ctx *IVRContext, skip map[string]struct{}) int {
	if ctx == nil {
		return 0
	}
	if ctx.Variables == nil {
		ctx.Variables = map[string]string{}
	}
	applied := 0

	// Single assignment shorthand
	if name, _ := cfg["name"].(string); strings.TrimSpace(name) != "" {
		name = strings.TrimSpace(name)
		if _, blocked := skip[name]; !blocked {
			val, _ := cfg["value"].(string)
			ctx.Variables[name] = interpolateTemplate(val, ctx.Variables)
			applied++
		}
	}

	raw, ok := cfg["assignments"].([]any)
	if !ok {
		return applied
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, blocked := skip[name]; blocked {
			continue
		}
		val, _ := m["value"].(string)
		ctx.Variables[name] = interpolateTemplate(val, ctx.Variables)
		applied++
	}
	return applied
}

func ivrConfirmPrompt(cfg map[string]any, value string) string {
	tmpl, _ := cfg["prompt_template"].(string)
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		tmpl = "You entered {{value}}. Press {{confirm_digit}} to confirm, or {{retry_digit}} to re-enter."
	}
	confirmDigit := ivrConfirmDigit(cfg, "confirm_digit", "1")
	retryDigit := ivrConfirmDigit(cfg, "retry_digit", "2")
	vars := map[string]string{
		"value":         value,
		"confirm_digit": confirmDigit,
		"retry_digit":   retryDigit,
	}
	// Also allow {{variable}} style if prompt uses the store name
	if varName, _ := cfg["variable"].(string); varName != "" {
		vars[varName] = value
	}
	return interpolateTemplate(tmpl, vars)
}

func ivrConfirmDigit(cfg map[string]any, key, fallback string) string {
	if v, ok := cfg[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if n, ok := cfg[key].(float64); ok {
		return fmt.Sprintf("%.0f", n)
	}
	if n, ok := cfg[key].(int); ok {
		return strconv.Itoa(n)
	}
	return fallback
}
