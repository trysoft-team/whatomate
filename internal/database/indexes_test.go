package database

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIndexesFor_PostgresOnlyStatements(t *testing.T) {
	pgOnly := func(stmt string) bool {
		return strings.Contains(stmt, "USING GIN") || strings.Contains(stmt, "ALTER COLUMN")
	}

	pg := indexesFor("postgres")
	sqlite := indexesFor("sqlite")
	var pgOnlyCount int
	for _, s := range pg {
		if pgOnly(s) {
			pgOnlyCount++
		}
	}
	assert.Equal(t, 5, pgOnlyCount, "GIN index and four phone_number ALTERs")
	assert.Len(t, sqlite, len(pg)-pgOnlyCount)
	for _, s := range sqlite {
		assert.False(t, pgOnly(s), s)
	}
}
