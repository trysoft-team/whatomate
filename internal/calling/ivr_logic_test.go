package calling

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchIVRCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  map[string]any
		left string
		want bool
	}{
		{"equals", map[string]any{"operator": "equals", "value": "abc"}, "abc", true},
		{"equals case insensitive", map[string]any{"operator": "equals", "value": "ABC"}, "abc", true},
		{"equals case sensitive", map[string]any{"operator": "equals", "value": "ABC", "case_sensitive": true}, "abc", false},
		{"not_equals", map[string]any{"operator": "not_equals", "value": "x"}, "y", true},
		{"contains", map[string]any{"operator": "contains", "value": "12"}, "00123", true},
		{"starts_with", map[string]any{"operator": "starts_with", "value": "00"}, "00123", true},
		{"ends_with", map[string]any{"operator": "ends_with", "value": "23"}, "00123", true},
		{"empty", map[string]any{"operator": "empty"}, "  ", true},
		{"not_empty", map[string]any{"operator": "not_empty"}, "a", true},
		{"regex", map[string]any{"operator": "regex", "value": `^\d{4}$`}, "1234", true},
		{"regex fail", map[string]any{"operator": "regex", "value": `^\d{4}$`}, "12", false},
		{"invalid regex", map[string]any{"operator": "regex", "value": "["}, "x", false},
		{"gt", map[string]any{"operator": "gt", "value": "10"}, "15", true},
		{"lt", map[string]any{"operator": "lt", "value": "10"}, "5", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, matchIVRCondition(tt.cfg, tt.left))
		})
	}
}

func TestResolveIVRLeftValue(t *testing.T) {
	t.Parallel()
	ctx := &IVRContext{Variables: map[string]string{"account": "42", "caller_phone": "1555"}}
	assert.Equal(t, "42", resolveIVRLeftValue(map[string]any{"field": "account"}, ctx))
	assert.Equal(t, "acct-42", resolveIVRLeftValue(map[string]any{"field": "acct-{{account}}"}, ctx))
	assert.Equal(t, "", resolveIVRLeftValue(map[string]any{"field": "missing"}, ctx))
}

func TestApplyIVRSetVariables(t *testing.T) {
	t.Parallel()
	ctx := &IVRContext{Variables: map[string]string{"account": "99"}}
	n := applyIVRSetVariables(map[string]any{
		"name":  "status",
		"value": "ok",
		"assignments": []any{
			map[string]any{"name": "label", "value": "acct-{{account}}"},
			map[string]any{"name": "", "value": "skip"},
		},
	}, ctx)
	require.Equal(t, 2, n)
	assert.Equal(t, "ok", ctx.Variables["status"])
	assert.Equal(t, "acct-99", ctx.Variables["label"])
}

func TestApplyIVRStartDefaults(t *testing.T) {
	t.Parallel()
	ctx := &IVRContext{Variables: map[string]string{
		"caller_phone": "+1555",
		"caller_name":  "Ada",
		"call_id":      "wacid.1",
	}}
	n := applyIVRStartDefaults(map[string]any{
		"assignments": []any{
			map[string]any{"name": "department", "value": "sales"},
			map[string]any{"name": "greet", "value": "Hi {{caller_name}}"},
			map[string]any{"name": "caller_phone", "value": "hijack"},
			map[string]any{"name": "caller_name", "value": "hijack"},
			map[string]any{"name": "call_id", "value": "hijack"},
			map[string]any{"name": "", "value": "skip"},
		},
	}, ctx)
	require.Equal(t, 2, n)
	assert.Equal(t, "sales", ctx.Variables["department"])
	assert.Equal(t, "Hi Ada", ctx.Variables["greet"])
	assert.Equal(t, "+1555", ctx.Variables["caller_phone"])
	assert.Equal(t, "Ada", ctx.Variables["caller_name"])
	assert.Equal(t, "wacid.1", ctx.Variables["call_id"])
}

func TestIVRConfirmPrompt(t *testing.T) {
	t.Parallel()
	out := ivrConfirmPrompt(map[string]any{
		"variable": "account",
	}, "1234")
	assert.Contains(t, out, "1234")
	assert.Contains(t, out, "1")
	assert.Contains(t, out, "2")
}
