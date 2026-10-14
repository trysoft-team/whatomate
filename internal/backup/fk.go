package backup

import (
	"database/sql"
	"fmt"

	"github.com/trysoft/whatomate/internal/config"
	"gorm.io/gorm"
)

// foreignKey is one foreign-key column as the database itself reports it.
type foreignKey struct {
	Table, Column, RefTable, RefColumn string
}

// foreignKeys reads the foreign keys the database enforces.
func foreignKeys(db *gorm.DB) ([]foreignKey, error) {
	switch db.Name() {
	case config.DriverPostgres:
		return scanForeignKeys(db, `SELECT kcu.table_name, kcu.column_name, ccu.table_name, ccu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema
JOIN information_schema.constraint_column_usage ccu
  ON ccu.constraint_name = tc.constraint_name AND ccu.table_schema = tc.table_schema
WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = current_schema()`)
	case config.DriverMySQL:
		return scanForeignKeys(db, `SELECT TABLE_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
FROM information_schema.KEY_COLUMN_USAGE
WHERE TABLE_SCHEMA = DATABASE() AND REFERENCED_TABLE_NAME IS NOT NULL`)
	case config.DriverSQLite:
		tables, err := db.Migrator().GetTables()
		if err != nil {
			return nil, err
		}
		var out []foreignKey
		for _, t := range tables {
			rows, err := db.Raw(`SELECT "table", "from", "to" FROM pragma_foreign_key_list(?)`, t).Rows()
			if err != nil {
				return nil, fmt.Errorf("foreign keys of %s: %w", t, err)
			}
			for rows.Next() {
				var ref, col string
				var to sql.NullString
				if err := rows.Scan(&ref, &col, &to); err != nil {
					_ = rows.Close()
					return nil, err
				}
				fk := foreignKey{Table: t, Column: col, RefTable: ref, RefColumn: to.String}
				if fk.RefColumn == "" {
					fk.RefColumn = "id"
				}
				out = append(out, fk)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("foreign keys of %s: %w", t, err)
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("backup does not support database driver %q", db.Name())
	}
}

func scanForeignKeys(db *gorm.DB, query string) ([]foreignKey, error) {
	rows, err := db.Raw(query).Rows()
	if err != nil {
		return nil, fmt.Errorf("read foreign keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []foreignKey
	for rows.Next() {
		var fk foreignKey
		if err := rows.Scan(&fk.Table, &fk.Column, &fk.RefTable, &fk.RefColumn); err != nil {
			return nil, err
		}
		out = append(out, fk)
	}
	return out, rows.Err()
}
