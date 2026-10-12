package dialect

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// DBTime scans timestamps returned as time.Time (PostgreSQL) or text (SQLite).
type DBTime struct{ time.Time }

var dbTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

func (t *DBTime) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		t.Time = time.Time{}
		return nil
	case time.Time:
		t.Time = v
		return nil
	case []byte:
		return t.parse(string(v))
	case string:
		return t.parse(v)
	}
	return fmt.Errorf("dialect: cannot scan %T into DBTime", value)
}

// Value makes GORM treat DBTime fields as time columns rather than relations.
func (t DBTime) Value() (driver.Value, error) { return t.Time, nil }

func (t *DBTime) parse(s string) error {
	for _, layout := range dbTimeLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("dialect: unrecognised time %q", s)
}
