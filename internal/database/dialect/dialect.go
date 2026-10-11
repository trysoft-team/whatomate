// Package dialect returns SQL fragments for the database driver in use, so
// handlers don't hard-code PostgreSQL syntax.
package dialect

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

type Dialect struct{ name string }

// Of returns the dialect of db's driver ("postgres", "sqlite", ...).
func Of(db *gorm.DB) Dialect { return Dialect{name: db.Dialector.Name()} }

func Named(name string) Dialect { return Dialect{name: name} }

func (d Dialect) Name() string { return d.name }

func (d Dialect) isPostgres() bool { return d.name == "postgres" }

// ILike is a case-insensitive LIKE condition with one placeholder. SQLite's
// LIKE folds ASCII letters only.
func (d Dialect) ILike(col string) string {
	if d.isPostgres() {
		return col + " ILIKE ?"
	}
	return col + " LIKE ?"
}

func (d Dialect) AsText(col string) string {
	if d.isPostgres() {
		return col + "::text"
	}
	return "CAST(" + col + " AS TEXT)"
}

func (d Dialect) JSONText(col, key string) string {
	if d.isPostgres() {
		return fmt.Sprintf("%s->>'%s'", col, key)
	}
	return fmt.Sprintf("json_extract(%s, '$.%s')", col, key)
}

// JSONArrayHas matches rows whose JSON array column contains value.
func (d Dialect) JSONArrayHas(col, value string) (string, any) {
	if d.isPostgres() {
		b, _ := json.Marshal([]string{value})
		return col + " @> ?::jsonb", string(b)
	}
	return "EXISTS (SELECT 1 FROM json_each(" + col + ") WHERE json_each.value = ?)", value
}

func (d Dialect) MinutesBetween(from, to string) string {
	if d.isPostgres() {
		return fmt.Sprintf("EXTRACT(EPOCH FROM (%s - %s))/60", to, from)
	}
	return fmt.Sprintf("((julianday(%s) - julianday(%s)) * 1440)", to, from)
}

// DayOfWeek returns 0 (Sunday) to 6.
func (d Dialect) DayOfWeek(col string) string {
	if d.isPostgres() {
		return fmt.Sprintf("EXTRACT(DOW FROM %s)::int", col)
	}
	return fmt.Sprintf("CAST(strftime('%%w', %s) AS INTEGER)", col)
}

func (d Dialect) HourOf(col string) string {
	if d.isPostgres() {
		return fmt.Sprintf("EXTRACT(HOUR FROM %s)::int", col)
	}
	return fmt.Sprintf("CAST(strftime('%%H', %s) AS INTEGER)", col)
}

// DateTrunc truncates col to the start of its day, ISO week (Monday) or month.
// Scan the result into DBTime: SQLite returns text.
func (d Dialect) DateTrunc(unit, col string) string {
	if d.isPostgres() {
		switch unit {
		case "day", "week", "month":
			return fmt.Sprintf("DATE_TRUNC('%s', %s)", unit, col)
		}
	} else {
		switch unit {
		case "day":
			return fmt.Sprintf("date(%s)", col)
		case "week":
			return fmt.Sprintf("date(%s, '-6 days', 'weekday 1')", col)
		case "month":
			return fmt.Sprintf("strftime('%%Y-%%m-01', %s)", col)
		}
	}
	panic("dialect: unsupported DateTrunc unit " + unit)
}

func (d Dialect) Now() string {
	if d.isPostgres() {
		return "NOW()"
	}
	return "CURRENT_TIMESTAMP"
}

// SupportsSkipLocked reports whether SELECT ... FOR UPDATE SKIP LOCKED works.
// SQLite serialises writers, so a plain transaction is enough there.
func (d Dialect) SupportsSkipLocked() bool { return d.isPostgres() }
