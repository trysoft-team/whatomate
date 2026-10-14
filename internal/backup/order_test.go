package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlanLoad_ParentsFirstAndSelfReference(t *testing.T) {
	fks := []foreignKey{
		{Table: "contacts", Column: "organization_id", RefTable: "organizations", RefColumn: "id"},
		{Table: "messages", Column: "contact_id", RefTable: "contacts", RefColumn: "id"},
		{Table: "messages", Column: "reply_to_message_id", RefTable: "messages", RefColumn: "id"},
		{Table: "elsewhere", Column: "x_id", RefTable: "organizations", RefColumn: "id"},
	}
	p := planLoad([]string{"messages", "contacts", "organizations"}, fks)
	assert.Equal(t, []string{"organizations", "contacts", "messages"}, p.Order)
	assert.Equal(t, map[string][]string{"messages": {"reply_to_message_id"}}, p.Deferred)
}

func TestPlanLoad_BreaksCycles(t *testing.T) {
	fks := []foreignKey{
		{Table: "a", Column: "b_id", RefTable: "b", RefColumn: "id"},
		{Table: "b", Column: "a_id", RefTable: "a", RefColumn: "id"},
	}
	p := planLoad([]string{"a", "b"}, fks)
	assert.Equal(t, []string{"a", "b"}, p.Order)
	assert.Equal(t, map[string][]string{"a": {"b_id"}}, p.Deferred)
}
