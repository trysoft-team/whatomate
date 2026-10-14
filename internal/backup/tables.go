// Package backup writes the whole database (and optionally local files) to a
// portable zip and restores it into any supported database.
package backup

import (
	"fmt"

	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// storeTableNames hold the embedded store's short-lived state. They are not
// backed up and a restore empties them.
var storeTableNames = []string{"kv_entries", "scheduled_items", "campaign_jobs"}

// table is one table a backup holds, with its columns in schema order.
type table struct {
	Name    string
	Schema  *schema.Schema
	Columns []*schema.Field
}

func isStoreTable(name string) bool {
	for _, s := range storeTableNames {
		if s == name {
			return true
		}
	}
	return false
}

// backupTables lists every table a backup holds: all migrated models except
// the store tables, plus the role_permissions join table.
func backupTables(db *gorm.DB) ([]table, error) {
	var out []table
	add := func(model any) error {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return fmt.Errorf("parse %T: %w", model, err)
		}
		s := stmt.Schema
		if isStoreTable(s.Table) {
			return nil
		}
		t := table{Name: s.Table, Schema: s}
		for _, name := range s.DBNames {
			if f := s.FieldsByDBName[name]; !f.IgnoreMigration {
				t.Columns = append(t.Columns, f)
			}
		}
		out = append(out, t)
		return nil
	}
	for _, m := range database.GetMigrationModels() {
		if err := add(m.Model); err != nil {
			return nil, err
		}
	}
	if err := add(&models.RolePermission{}); err != nil {
		return nil, err
	}
	return out, nil
}

func columnsByName(t table) map[string]*schema.Field {
	m := make(map[string]*schema.Field, len(t.Columns))
	for _, f := range t.Columns {
		m[f.DBName] = f
	}
	return m
}
