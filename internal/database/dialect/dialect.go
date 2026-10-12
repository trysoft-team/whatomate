// Package dialect returns SQL fragments for the database driver in use, so
// handlers don't hard-code PostgreSQL syntax.
//
// Column, key and unit arguments are pasted into the SQL unescaped: they must
// be constant identifiers or fields checked against an allow-list, never user
// input. Only the values bound to the returned placeholders may come from users.
package dialect

import (
	"encoding/json"
	"fmt"

	"github.com/trysoft/whatomate/internal/config"
	"gorm.io/gorm"
)

// Dialect builds SQL fragments for one driver. Its column and key arguments
// must never be user input (see the package doc).
type Dialect struct{ name string }

// Of returns the dialect of db's driver ("postgres", "sqlite" or "mysql",
// which MariaDB shares). It panics on a driver this package has no SQL for;
// Open rejects those first.
func Of(db *gorm.DB) Dialect { return Named(db.Dialector.Name()) }

func Named(name string) Dialect {
	switch name {
	case config.DriverPostgres, config.DriverSQLite, config.DriverMySQL:
		return Dialect{name: name}
	}
	panic(fmt.Sprintf("dialect: unsupported driver %q", name))
}

func (d Dialect) Name() string { return d.name }

// ILike is a case-insensitive LIKE condition with one placeholder. SQLite's
// LIKE folds ASCII letters only. MySQL lowers both sides so the result does
// not depend on the column collation.
func (d Dialect) ILike(col string) string {
	switch d.name {
	case config.DriverPostgres:
		return col + " ILIKE ?"
	case config.DriverMySQL:
		return "LOWER(" + col + ") LIKE LOWER(?)"
	default:
		return col + " LIKE ?"
	}
}

func (d Dialect) AsText(col string) string {
	switch d.name {
	case config.DriverPostgres:
		return col + "::text"
	case config.DriverMySQL:
		return "CAST(" + col + " AS CHAR)"
	default:
		return "CAST(" + col + " AS TEXT)"
	}
}

func (d Dialect) JSONText(col, key string) string {
	switch d.name {
	case config.DriverPostgres:
		return fmt.Sprintf("%s->>'%s'", col, key)
	case config.DriverMySQL:
		return fmt.Sprintf("JSON_UNQUOTE(JSON_EXTRACT(%s, '$.%s'))", col, key)
	default:
		return fmt.Sprintf("json_extract(%s, '$.%s')", col, key)
	}
}

// JSONArrayHas matches rows whose JSON array column contains value.
func (d Dialect) JSONArrayHas(col, value string) (string, any) {
	switch d.name {
	case config.DriverPostgres:
		b, _ := json.Marshal([]string{value})
		return col + " @> ?::jsonb", string(b)
	case config.DriverMySQL:
		return "JSON_CONTAINS(" + col + ", JSON_QUOTE(?))", value
	default:
		return "EXISTS (SELECT 1 FROM json_each(" + col + ") WHERE json_each.value = ?)", value
	}
}

func (d Dialect) MinutesBetween(from, to string) string {
	switch d.name {
	case config.DriverPostgres:
		return fmt.Sprintf("EXTRACT(EPOCH FROM (%s - %s))/60", to, from)
	case config.DriverMySQL:
		return fmt.Sprintf("(TIMESTAMPDIFF(MICROSECOND, %s, %s) / 60000000)", from, to)
	default:
		return fmt.Sprintf("((julianday(%s) - julianday(%s)) * 1440)", to, from)
	}
}

// DayOfWeek returns 0 (Sunday) to 6.
func (d Dialect) DayOfWeek(col string) string {
	switch d.name {
	case config.DriverPostgres:
		return fmt.Sprintf("EXTRACT(DOW FROM %s)::int", col)
	case config.DriverMySQL:
		return fmt.Sprintf("(DAYOFWEEK(%s) - 1)", col)
	default:
		return fmt.Sprintf("CAST(strftime('%%w', %s) AS INTEGER)", col)
	}
}

func (d Dialect) HourOf(col string) string {
	switch d.name {
	case config.DriverPostgres:
		return fmt.Sprintf("EXTRACT(HOUR FROM %s)::int", col)
	case config.DriverMySQL:
		return fmt.Sprintf("HOUR(%s)", col)
	default:
		return fmt.Sprintf("CAST(strftime('%%H', %s) AS INTEGER)", col)
	}
}

// DateTrunc truncates col to the start of its day, ISO week (Monday) or month.
// Scan the result into DBTime: SQLite returns text.
func (d Dialect) DateTrunc(unit, col string) string {
	switch d.name {
	case config.DriverPostgres:
		switch unit {
		case "day", "week", "month":
			return fmt.Sprintf("DATE_TRUNC('%s', %s)", unit, col)
		}
	case config.DriverMySQL:
		switch unit {
		case "day":
			return fmt.Sprintf("DATE(%s)", col)
		case "week":
			return fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", col, col)
		case "month":
			return fmt.Sprintf("CAST(DATE_FORMAT(%s, '%%Y-%%m-01') AS DATE)", col)
		}
	default:
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
	switch d.name {
	case config.DriverPostgres:
		return "NOW()"
	case config.DriverMySQL:
		return "UTC_TIMESTAMP(6)"
	default:
		return "CURRENT_TIMESTAMP"
	}
}

// DescNullsLast orders col descending with NULLs after values. MySQL already
// sorts NULLs last when descending and has no NULLS LAST syntax.
func (d Dialect) DescNullsLast(col string) string {
	if d.name == config.DriverMySQL {
		return col + " DESC"
	}
	return col + " DESC NULLS LAST"
}

// AscNullsFirst orders col ascending with NULLs first, MySQL's default.
func (d Dialect) AscNullsFirst(col string) string {
	if d.name == config.DriverMySQL {
		return col + " ASC"
	}
	return col + " ASC NULLS FIRST"
}

// SupportsSkipLocked reports whether SELECT ... FOR UPDATE SKIP LOCKED works.
// SQLite serialises writers, so a plain transaction is enough there.
func (d Dialect) SupportsSkipLocked() bool {
	return d.name == config.DriverPostgres || d.name == config.DriverMySQL
}

// SupportsPercentileCont reports whether the PERCENTILE_CONT ordered-set
// aggregate exists. Elsewhere callers compute it from the selected values.
func (d Dialect) SupportsPercentileCont() bool { return d.name == config.DriverPostgres }
