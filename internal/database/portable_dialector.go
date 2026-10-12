package database

import (
	"database/sql"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// portableDialector maps the PostgreSQL column types used in model tags to
// the wrapped driver's equivalents, so models stay unchanged. Types are
// matched on their base name, so "decimal(3,2)" matches "decimal".
type portableDialector struct {
	gorm.Dialector
	types map[string]string
	// columnSuffixes are appended to the type of individual "table.column"s.
	columnSuffixes map[string]string
	// pool, if set, is closed by Open when it fails after the dialector opened it.
	pool *sql.DB
}

// SQLite ignores decimal precision, but the migrator reads "decimal(3,2)" back
// as length 3, never equal to a float field's size of 64, so the table would
// be rebuilt on every migration; plain numeric has the same affinity.
var sqliteTypes = map[string]string{"uuid": "text", "jsonb": "text", "text": "text", "decimal": "numeric"}

func (d portableDialector) DataTypeOf(field *schema.Field) string {
	t := d.baseDataTypeOf(field)
	if field.Schema != nil {
		t += d.columnSuffixes[field.Schema.Table+"."+field.DBName]
	}
	return t
}

func (d portableDialector) baseDataTypeOf(field *schema.Field) string {
	base, _, _ := strings.Cut(strings.ToLower(string(field.DataType)), "(")
	if t, ok := d.types[strings.TrimSpace(base)]; ok {
		return t
	}
	return d.Dialector.DataTypeOf(field)
}

// Migrator points the wrapped migrator back at d; otherwise it would call the
// unwrapped dialector's DataTypeOf when creating and comparing columns.
func (d portableDialector) Migrator(db *gorm.DB) gorm.Migrator {
	switch m := d.Dialector.Migrator(db).(type) {
	case sqlite.Migrator:
		m.Dialector = d
		return m
	case mysql.Migrator:
		// mysql.Migrator has its own Dialector field (mysql.Dialector); the
		// one DataTypeOf goes through is migrator.Config's.
		m.Migrator.Dialector = d
		return mysqlMigrator{m}
	default:
		return m
	}
}

// SavePoint, RollbackTo and Translate are optional interfaces GORM detects by
// type assertion, so embedding alone would hide them.

func (d portableDialector) SavePoint(tx *gorm.DB, name string) error {
	if sp, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return sp.SavePoint(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}

func (d portableDialector) RollbackTo(tx *gorm.DB, name string) error {
	if sp, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return sp.RollbackTo(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}

func (d portableDialector) Translate(err error) error {
	if tr, ok := d.Dialector.(gorm.ErrorTranslator); ok {
		return tr.Translate(err)
	}
	return err
}
