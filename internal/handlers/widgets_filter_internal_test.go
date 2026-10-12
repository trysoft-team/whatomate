package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/trysoft/whatomate/internal/database/dialect"
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
