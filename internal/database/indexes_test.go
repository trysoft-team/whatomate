package database

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIndexesFor_PostgresOnlyStatements(t *testing.T) {
	pgOnly := func(stmt string) bool {
		return strings.Contains(stmt, "USING GIN") || strings.Contains(stmt, "ALTER COLUMN")
	}

	pg := indexesFor("postgres")
	sqlite := indexesFor("sqlite")
	var pgOnlyCount int
	for _, s := range pg {
		if pgOnly(s.sql) {
			pgOnlyCount++
		}
	}
	assert.Equal(t, 5, pgOnlyCount, "GIN index and four phone_number ALTERs")
	assert.Len(t, sqlite, len(pg)-pgOnlyCount)
	for _, s := range sqlite {
		assert.False(t, pgOnly(s.sql), s.sql)
	}

	var columns int
	for _, s := range indexesFor("mysql") {
		assert.False(t, pgOnly(s.sql), s.sql)
		if s.isColumn {
			columns++
		}
	}
	assert.Equal(t, 3, columns, "one generated column per partial unique index")
	assert.Len(t, indexesFor("mysql"), len(sqlite)+columns)
}

func TestIndexes_PostgresAndSQLiteUnchanged(t *testing.T) {
	for _, driver := range []string{"postgres", "sqlite"} {
		want, err := os.ReadFile("testdata/indexes_" + driver + ".golden")
		require.NoError(t, err)
		var got []string
		for _, s := range indexesFor(driver) {
			got = append(got, s.sql)
		}
		assert.Equal(t, strings.TrimSpace(string(want)), strings.Join(got, "\n"), driver)
	}
}

func TestIndexes_MySQL(t *testing.T) {
	stmts := indexesFor("mysql")
	byName := map[string]indexStmt{}
	for _, s := range stmts {
		assert.NotContains(t, s.sql, "IF NOT EXISTS")
		assert.NotContains(t, s.sql, " WHERE ")
		assert.NotContains(t, s.sql, "GIN")
		assert.NotContains(t, s.sql, "ALTER COLUMN")
		assert.NotEmpty(t, s.name, s.sql)
		assert.NotEmpty(t, s.table, s.sql)
		byName[s.name] = s
	}
	col := byName["idx_team_members_unique_active"]
	assert.Equal(t, "team_members", col.table)
	assert.True(t, col.isColumn)
	assert.Equal(t, "ALTER TABLE team_members ADD COLUMN idx_team_members_unique_active TINYINT AS (CASE WHEN deleted_at IS NULL THEN 1 END) VIRTUAL", col.sql)
	assert.Equal(t, "CREATE UNIQUE INDEX idx_team_members_unique ON team_members(team_id, user_id, idx_team_members_unique_active)", byName["idx_team_members_unique"].sql)
	assert.False(t, byName["idx_team_members_unique"].isColumn)
	assert.Equal(t, "CREATE INDEX idx_agent_transfers_agent_active ON agent_transfers(agent_id, status)", byName["idx_agent_transfers_agent_active"].sql)
	assert.Contains(t, byName["idx_ivr_flows_org_call_start_active"].sql, "CASE WHEN is_call_start = true AND is_active = true AND deleted_at IS NULL THEN 1 END")
	assert.Contains(t, byName["idx_user_org_unique"].sql, "(user_id, organization_id, idx_user_org_unique_active)")
}
