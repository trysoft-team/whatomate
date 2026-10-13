package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupTables_CoverSchemaWithoutStoreTables(t *testing.T) {
	db := newSQLiteDB(t)
	tbls, err := backupTables(db)
	require.NoError(t, err)

	names := map[string]bool{}
	for _, tb := range tbls {
		names[tb.Name] = true
		assert.True(t, db.Migrator().HasTable(tb.Name), tb.Name)
		for _, f := range tb.Columns {
			assert.True(t, db.Migrator().HasColumn(tb.Name, f.DBName), "%s.%s", tb.Name, f.DBName)
		}
	}
	for _, want := range []string{"organizations", "users", "messages", "role_permissions", "tags"} {
		assert.True(t, names[want], want)
	}
	for _, s := range storeTableNames {
		assert.False(t, names[s], s)
	}
}

func TestForeignKeys_OrderLoadsParentsFirst(t *testing.T) {
	db := newSQLiteDB(t)
	tbls, err := backupTables(db)
	require.NoError(t, err)
	fks, err := foreignKeys(db)
	require.NoError(t, err)
	require.NotEmpty(t, fks)

	var names []string
	for _, tb := range tbls {
		names = append(names, tb.Name)
	}
	p := planLoad(names, fks)
	require.Len(t, p.Order, len(names))
	pos := map[string]int{}
	for i, n := range p.Order {
		pos[n] = i
	}
	deferred := func(table, col string) bool {
		for _, c := range p.Deferred[table] {
			if c == col {
				return true
			}
		}
		return false
	}
	for _, fk := range fks {
		if _, ok := pos[fk.Table]; !ok || deferred(fk.Table, fk.Column) {
			continue
		}
		if _, ok := pos[fk.RefTable]; !ok {
			continue
		}
		assert.Less(t, pos[fk.RefTable], pos[fk.Table], "%s.%s -> %s", fk.Table, fk.Column, fk.RefTable)
	}
	assert.Contains(t, p.Deferred["messages"], "reply_to_message_id")
}
