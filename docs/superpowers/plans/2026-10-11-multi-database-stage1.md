# Multi-database Stage 1 (groundwork + SQLite) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Whatomate runs on PostgreSQL (unchanged) or SQLite, chosen with `[database] driver`, with the full Go test suite green on both.

**Architecture:** Keep GORM. `database.Open` picks the dialector from config. Non-PostgreSQL dialectors are wrapped so the existing `type:uuid` / `type:jsonb` / `type:text` model tags map to portable column types. PostgreSQL-only SQL in handlers moves behind `internal/database/dialect` helpers that read the driver name from `db.Dialector.Name()`. IDs are generated in Go. SQLite connections convert outgoing `time.Time` arguments to UTC.

**Tech Stack:** Go 1.25, GORM v1.25 (`gorm.io/gorm`), `gorm.io/driver/postgres` (existing), `github.com/glebarez/sqlite` (pure Go, modernc), testify.

**Spec:** `docs/superpowers/specs/2026-10-11-multi-database-design.md`

Branch: `feat/multi-database-stage1` (off `feat/multi-database`). Merge back into `feat/multi-database` when Task 11 is green.

## Global Constraints

- `[database] driver` values: `postgres` (default), `mysql`, `sqlite`, `sqlserver`. Stage 1 implements `postgres` and `sqlite`; `mysql` and `sqlserver` must fail at startup with `database driver "<name>" is not supported yet`.
- `[database] path` is sqlite-only, default `./whatomate.db`.
- Release binaries stay `CGO_ENABLED=0`: never import `gorm.io/driver/sqlite` or `github.com/mattn/go-sqlite3`.
- Existing PostgreSQL installs: no column type change, no data change. `type:uuid`/`type:jsonb`/`type:text` tags stay in the models.
- Go stays at 1.25 (`go.mod`: `go 1.25.0`).
- Never point tests at the app database. PostgreSQL test command:
  `set -a && . ./.env && set +a; TEST_DATABASE_URL="postgres://whatomate:${POSTGRES_PASSWORD}@127.0.0.1:5433/whatomate_test?sslmode=disable" TEST_REDIS_URL="redis://127.0.0.1:6379/15" go test -p 1 -count=1 ./...`
- SQLite test command: `TEST_DB_DRIVER=sqlite TEST_REDIS_URL="redis://127.0.0.1:6379/15" go test -p 1 -count=1 ./...`
- Commit after each task on `feat/multi-database-stage1`. Local git only, never push.

## Review Focus

1. **Times compared across zones on SQLite**: a handler passes `time.Now()` (local zone) and rows were stored in UTC; filters like `created_at >= ?` must still match. Pinned in Task 4 (`TestOpenSQLite_TimeArgsAreUTC`).
2. **Case-insensitive search on SQLite with non-ASCII**: searching `"émile"` should still find `"Émile"`. SQLite `LIKE` only folds ASCII; accepted limitation, pinned as a documented test in Task 6 so behaviour can't silently change.
3. **Tag names with quotes or `%`**: renaming tag `vip"1` or filtering by `50%` must not break SQL or match wrong contacts. Pinned in Task 7.
4. **Existing PostgreSQL database upgraded in place**: AutoMigrate on a database created by the old code must not alter `id` defaults or column types. Pinned in Task 4 (`TestPostgresSchemaUnchanged`).
5. **Concurrent writes on SQLite** (webhook bursts, parallel tests): must wait, not fail with `database is locked`. Pinned in Task 4 (`TestOpenSQLite_ConcurrentWrites`).

---

### Task 1: `driver` and `path` config

**Files:**
- Modify: `internal/config/config.go` (`DatabaseConfig` at ~145, `setDefaults`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.DatabaseConfig.Driver string`, `config.DatabaseConfig.Path string`; constants `config.DriverPostgres = "postgres"`, `DriverMySQL = "mysql"`, `DriverSQLite = "sqlite"`, `DriverSQLServer = "sqlserver"`.

- [ ] **Step 1: Write the failing test** (append to `config_test.go`)

```go
func TestLoad_DatabaseDriverDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""))
	require.NoError(t, err)
	assert.Equal(t, config.DriverPostgres, cfg.Database.Driver)
	assert.Equal(t, 5432, cfg.Database.Port)

	cfg, err = config.Load(writeConfig(t, "[database]\ndriver = \"SQLite\"\n"))
	require.NoError(t, err)
	assert.Equal(t, config.DriverSQLite, cfg.Database.Driver, "driver is case-insensitive")
	assert.Equal(t, "./whatomate.db", cfg.Database.Path)

	cfg, err = config.Load(writeConfig(t, "[database]\ndriver = \"mysql\"\n"))
	require.NoError(t, err)
	assert.Equal(t, 3306, cfg.Database.Port)

	cfg, err = config.Load(writeConfig(t, "[database]\ndriver = \"sqlserver\"\n"))
	require.NoError(t, err)
	assert.Equal(t, 1433, cfg.Database.Port)
}
```

- [ ] **Step 2: Run it, expect FAIL** (`cfg.Database.Driver undefined`)

Run: `go test ./internal/config/ -run TestLoad_DatabaseDriverDefaults`

- [ ] **Step 3: Implement**

In `DatabaseConfig` add before `Host`:

```go
	Driver          string `koanf:"driver"` // postgres (default), mysql, sqlite, sqlserver
	Path            string `koanf:"path"`   // sqlite database file
```

Above the struct:

```go
const (
	DriverPostgres  = "postgres"
	DriverMySQL     = "mysql"
	DriverSQLite    = "sqlite"
	DriverSQLServer = "sqlserver"
)
```

In `setDefaults`, replace the `cfg.Database.Port == 0` block with:

```go
	cfg.Database.Driver = strings.ToLower(strings.TrimSpace(cfg.Database.Driver))
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = DriverPostgres
	}
	if cfg.Database.Port == 0 {
		switch cfg.Database.Driver {
		case DriverMySQL:
			cfg.Database.Port = 3306
		case DriverSQLServer:
			cfg.Database.Port = 1433
		default:
			cfg.Database.Port = 5432
		}
	}
	if cfg.Database.Driver == DriverSQLite && cfg.Database.Path == "" {
		cfg.Database.Path = "./whatomate.db"
	}
```

- [ ] **Step 4: Run, expect PASS**: `go test ./internal/config/`
- [ ] **Step 5: Commit**: `git commit -am "feat(config): database driver and sqlite path settings"`

---

### Task 2: `dialect` package

**Files:**
- Create: `internal/database/dialect/dialect.go`
- Create: `internal/database/dialect/dbtime.go`
- Test: `internal/database/dialect/dialect_test.go`

**Interfaces:**
- Produces:
  - `type Dialect struct{ name string }`; `func Of(db *gorm.DB) Dialect`; `func Named(name string) Dialect`; `func (d Dialect) Name() string`
  - `func (d Dialect) ILike(col string) string` → condition with one `?`
  - `func (d Dialect) AsText(col string) string` → column cast to text for LIKE on JSON columns
  - `func (d Dialect) JSONText(col, key string) string`
  - `func (d Dialect) JSONArrayHas(col string, value string) (string, any)` → condition with one `?` and its argument
  - `func (d Dialect) MinutesBetween(from, to string) string`
  - `func (d Dialect) DayOfWeek(col string) string` (0 = Sunday), `func (d Dialect) HourOf(col string) string`
  - `func (d Dialect) DateTrunc(unit, col string) string` with unit `day|week|month`
  - `func (d Dialect) Now() string`
  - `func (d Dialect) SupportsSkipLocked() bool`
  - `type DBTime struct{ time.Time }` implementing `sql.Scanner` for `time.Time`, `string`, `[]byte`

- [ ] **Step 1: Write the failing test**

```go
package dialect_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/database/dialect"
)

func TestHelpers(t *testing.T) {
	pg, lite := dialect.Named("postgres"), dialect.Named("sqlite")

	cases := []struct{ name, pg, lite string }{
		{"ILike", pg.ILike("name"), lite.ILike("name")},
		{"AsText", pg.AsText("keywords"), lite.AsText("keywords")},
		{"JSONText", pg.JSONText("metadata", "campaign_id"), lite.JSONText("metadata", "campaign_id")},
		{"MinutesBetween", pg.MinutesBetween("a", "b"), lite.MinutesBetween("a", "b")},
		{"DayOfWeek", pg.DayOfWeek("c"), lite.DayOfWeek("c")},
		{"HourOf", pg.HourOf("c"), lite.HourOf("c")},
		{"DateTrunc day", pg.DateTrunc("day", "c"), lite.DateTrunc("day", "c")},
		{"DateTrunc week", pg.DateTrunc("week", "c"), lite.DateTrunc("week", "c")},
		{"DateTrunc month", pg.DateTrunc("month", "c"), lite.DateTrunc("month", "c")},
		{"Now", pg.Now(), lite.Now()},
	}
	want := map[string][2]string{
		"ILike":           {"name ILIKE ?", "name LIKE ?"},
		"AsText":          {"keywords::text", "CAST(keywords AS TEXT)"},
		"JSONText":        {"metadata->>'campaign_id'", "json_extract(metadata, '$.campaign_id')"},
		"MinutesBetween":  {"EXTRACT(EPOCH FROM (b - a))/60", "((julianday(b) - julianday(a)) * 1440)"},
		"DayOfWeek":       {"EXTRACT(DOW FROM c)::int", "CAST(strftime('%w', c) AS INTEGER)"},
		"HourOf":          {"EXTRACT(HOUR FROM c)::int", "CAST(strftime('%H', c) AS INTEGER)"},
		"DateTrunc day":   {"DATE_TRUNC('day', c)", "date(c)"},
		"DateTrunc week":  {"DATE_TRUNC('week', c)", "date(c, '-6 days', 'weekday 1')"},
		"DateTrunc month": {"DATE_TRUNC('month', c)", "strftime('%Y-%m-01', c)"},
		"Now":             {"NOW()", "CURRENT_TIMESTAMP"},
	}
	for _, c := range cases {
		assert.Equal(t, want[c.name][0], c.pg, c.name+" postgres")
		assert.Equal(t, want[c.name][1], c.lite, c.name+" sqlite")
	}

	cond, arg := pg.JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "tags @> ?::jsonb", cond)
	assert.Equal(t, `["vip\"1"]`, arg)
	cond, arg = lite.JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "EXISTS (SELECT 1 FROM json_each(tags) WHERE json_each.value = ?)", cond)
	assert.Equal(t, `vip"1`, arg)

	assert.True(t, pg.SupportsSkipLocked())
	assert.False(t, lite.SupportsSkipLocked())
	assert.Panics(t, func() { pg.DateTrunc("year", "c") })
}

func TestDBTimeScan(t *testing.T) {
	want := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, in := range []any{want, "2026-10-05", "2026-10-05 00:00:00", "2026-10-05T00:00:00Z", []byte("2026-10-05 00:00:00+00:00")} {
		var d dialect.DBTime
		require.NoError(t, d.Scan(in), "%v", in)
		assert.True(t, want.Equal(d.Time), "%v -> %v", in, d.Time)
	}
	var d dialect.DBTime
	require.NoError(t, d.Scan(nil))
	assert.True(t, d.IsZero())
	assert.Error(t, d.Scan("not a date"))
}
```

- [ ] **Step 2: Run, expect FAIL** (package missing): `go test ./internal/database/dialect/`

- [ ] **Step 3: Implement `dialect.go`**

```go
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
```

- [ ] **Step 4: Implement `dbtime.go`**

```go
package dialect

import (
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

func (t *DBTime) parse(s string) error {
	for _, layout := range dbTimeLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("dialect: unrecognised time %q", s)
}
```

- [ ] **Step 5: Run, expect PASS**: `go test ./internal/database/dialect/`
- [ ] **Step 6: Commit**: `git add internal/database/dialect && git commit -m "feat(database): dialect helpers for postgres and sqlite"`

---

### Task 3: Portable JSON values and Go-generated IDs

**Files:**
- Modify: `internal/models/models.go` (`JSONB`, `JSONBArray`, `StringArray` Value/Scan at 16-80; `BaseModel` at ~83; `UserAvailabilityLog` ID at ~153; `AuditLog` ID at ~556)
- Modify: `internal/models/user_passkey.go:13`
- Test: `internal/models/models_portable_test.go`

**Interfaces:**
- Produces: `func (b *BaseModel) BeforeCreate(tx *gorm.DB) error`; same hook on `UserPasskey`, `UserAvailabilityLog`, `AuditLog`. JSON types' `Value()` returns `string`; `Scan` accepts `[]byte` and `string`.

- [ ] **Step 1: Write the failing test**

```go
package models_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func TestJSONTypesScanStringAndBytes(t *testing.T) {
	var j models.JSONB
	require.NoError(t, j.Scan(`{"a":1}`))
	assert.Equal(t, float64(1), j["a"])
	require.NoError(t, j.Scan([]byte(`{"a":2}`)))
	assert.Equal(t, float64(2), j["a"])

	var arr models.JSONBArray
	require.NoError(t, arr.Scan(`["x"]`))
	assert.Equal(t, models.JSONBArray{"x"}, arr)

	var s models.StringArray
	require.NoError(t, s.Scan(`["y"]`))
	assert.Equal(t, models.StringArray{"y"}, s)

	v, err := models.JSONB{"k": "v"}.Value()
	require.NoError(t, err)
	assert.IsType(t, "", v, "Value returns string so text/nvarchar columns accept it")
}

func TestBeforeCreateAssignsID(t *testing.T) {
	var b models.BaseModel
	require.NoError(t, b.BeforeCreate(nil))
	assert.NotEqual(t, uuid.Nil, b.ID)

	keep := uuid.New()
	b2 := models.BaseModel{ID: keep}
	require.NoError(t, b2.BeforeCreate(nil))
	assert.Equal(t, keep, b2.ID)

	for _, m := range []interface{ BeforeCreate(*gorm.DB) error }{&models.UserPasskey{}, &models.UserAvailabilityLog{}, &models.AuditLog{}} {
		require.NoError(t, m.BeforeCreate(nil))
	}
}
```

(Add `"gorm.io/gorm"` to the imports.)

- [ ] **Step 2: Run, expect FAIL**: `go test ./internal/models/ -run 'JSONTypes|BeforeCreate'`

- [ ] **Step 3: Implement**

Add once in `models.go`:

```go
func scanJSON(value any, dest any) error {
	switch v := value.(type) {
	case []byte:
		return json.Unmarshal(v, dest)
	case string:
		return json.Unmarshal([]byte(v), dest)
	}
	return fmt.Errorf("cannot scan %T into JSON", value)
}

func jsonValue(v any) (driver.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
```

Rewrite each of the three types' methods to use them, e.g.:

```go
func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return jsonValue(j)
}

func (j *JSONB) Scan(value any) error {
	if value == nil {
		*j = nil
		return nil
	}
	return scanJSON(value, j)
}
```

(`JSONBArray` and `StringArray` identical apart from the receiver type.) Drop the now-unused `errors` import if nothing else uses it.

Change the four ID tags from `default:gen_random_uuid()` to no default, e.g. `gorm:"type:uuid;primary_key" json:"id"`, and add:

```go
// BeforeCreate assigns a UUID in Go so every database driver gets one.
func (b *BaseModel) BeforeCreate(*gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}
```

Same method on `UserPasskey`, `UserAvailabilityLog`, `AuditLog` (receiver and field differ only). If any of those types already has a `BeforeCreate`, add the ID line at its top instead.

- [ ] **Step 4: Run, expect PASS**: `go test ./internal/models/`, then the PostgreSQL suite command from Global Constraints (must stay green: Postgres still has the column default, and Go now sets IDs first).
- [ ] **Step 5: Commit**: `git commit -am "feat(models): portable JSON scanning and Go-generated IDs"`

---

### Task 4: `database.Open`, SQLite dialector, portable migrations

**Files:**
- Rename: `internal/database/postgres.go` → `internal/database/database.go`
- Create: `internal/database/open.go`, `internal/database/portable_dialector.go`, `internal/database/utc_pool.go`, `internal/database/indexes.go`
- Modify: `internal/database/database.go` (`MigrateUserOrganizations` ~388, `getIndexes` ~240 moves to `indexes.go`)
- Modify: `cmd/whatomate/main.go:150` and `:434` (`database.NewPostgres` → `database.Open`)
- Modify: `internal/handlers/chatbot_flow_migration.go:15-70`
- Test: `internal/database/open_test.go`

**Interfaces:**
- Consumes: `config.DatabaseConfig` (Task 1), model hooks (Task 3).
- Produces: `func Open(cfg *config.DatabaseConfig, debug bool) (*gorm.DB, error)`; `func OpenDialector(cfg *config.DatabaseConfig) (gorm.Dialector, error)`; `NewPostgres` kept as `func NewPostgres(cfg, debug) = Open(cfg, debug)` for one release.

- [ ] **Step 1: Add the dependency**: `go get github.com/glebarez/sqlite@latest && go mod tidy`. Confirm `CGO_ENABLED=0 go build ./...` succeeds.

- [ ] **Step 2: Write the failing tests** (`open_test.go`)

```go
package database_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
)

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Open(&config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "w.db")}, false)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{Email: "a@b.c", Password: "pw", FullName: "A"}))
	return db
}

func TestOpen_RejectsUnsupportedDrivers(t *testing.T) {
	for _, d := range []string{"mysql", "sqlserver", "oracle"} {
		_, err := database.Open(&config.DatabaseConfig{Driver: d}, false)
		assert.ErrorContains(t, err, `"`+d+`"`)
	}
}

func TestOpenSQLite_MigratesAndSeeds(t *testing.T) {
	db := openSQLite(t)
	var n int64
	require.NoError(t, db.Model(&models.User{}).Count(&n).Error)
	assert.EqualValues(t, 1, n, "default admin seeded")
	// Running migrations twice is a no-op.
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{Email: "a@b.c", Password: "pw", FullName: "A"}))
}

func TestOpenSQLite_TimeArgsAreUTC(t *testing.T) {
	db := openSQLite(t)
	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	local := time.FixedZone("CAT", 2*3600)
	since := time.Now().In(local).Add(-time.Minute)
	var n int64
	require.NoError(t, db.Model(&models.Organization{}).Where("created_at >= ?", since).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

func TestOpenSQLite_ConcurrentWrites(t *testing.T) {
	db := openSQLite(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- db.Create(&models.Organization{Name: "o", Slug: fmt.Sprintf("o-%d", i)}).Error
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}
```

(Add `"fmt"` and `"gorm.io/gorm"` imports. If `Organization` has other required fields, set them; check `models.Organization`.)

A PostgreSQL-only test (skipped when `TEST_DATABASE_URL` is unset):

```go
func TestPostgresSchemaUnchanged(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	var dataType, def string
	require.NoError(t, db.Raw(`SELECT data_type, COALESCE(column_default,'') FROM information_schema.columns WHERE table_name='contacts' AND column_name='id'`).Row().Scan(&dataType, &def))
	assert.Equal(t, "uuid", dataType)
	require.NoError(t, db.Raw(`SELECT data_type FROM information_schema.columns WHERE table_name='contacts' AND column_name='tags'`).Row().Scan(&dataType))
	assert.Equal(t, "jsonb", dataType)
}
```

(If `column_default` was `gen_random_uuid()` before, assert it still is: `assert.Contains(t, def, "gen_random_uuid")`. Run it once on the current `whatomate_test` DB before Step 3 to record the baseline.)

- [ ] **Step 3: Run, expect FAIL** (`database.Open undefined`): `go test ./internal/database/ -run 'Open|Postgres'`

- [ ] **Step 4: Implement `portable_dialector.go`**

```go
package database

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// portableDialector maps the PostgreSQL column types used in model tags to
// the wrapped driver's equivalents, so models stay unchanged.
type portableDialector struct {
	gorm.Dialector
	types map[string]string
}

func (d portableDialector) DataTypeOf(field *schema.Field) string {
	if t, ok := d.types[strings.ToLower(string(field.DataType))]; ok {
		return t
	}
	return d.Dialector.DataTypeOf(field)
}

var sqliteTypes = map[string]string{"uuid": "text", "jsonb": "text", "text": "text"}
```

Note: GORM calls `Migrator(db)` on the dialector; the embedded dialector's migrator calls `m.Dialector.DataTypeOf` on its own (unwrapped) dialector. Override `Migrator` so the wrapper is used:

```go
func (d portableDialector) Migrator(db *gorm.DB) gorm.Migrator {
	m := d.Dialector.Migrator(db)
	if sm, ok := m.(sqlite.Migrator); ok {
		sm.Dialector = d
		return sm
	}
	return m
}
```

Verify against glebarez/sqlite's `Migrator` type: it is `sqlite.Migrator{migrator.Migrator{Config: migrator.Config{Dialector: d, ...}}}`. Set `sm.Migrator.Config.Dialector = d` (adjust the field path to the actual struct after `go doc github.com/glebarez/sqlite Migrator`). Covered by `TestOpenSQLite_MigratesAndSeeds`; also assert in a test that `PRAGMA table_info(contacts)` reports `tags` type `text`.

- [ ] **Step 5: Implement `utc_pool.go`**

```go
package database

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// utcPool converts time arguments to UTC before they reach SQLite, which
// compares timestamps as text including the zone offset.
type utcPool struct{ db *sql.DB }

func utcArgs(args []any) []any {
	for i, a := range args {
		switch t := a.(type) {
		case time.Time:
			args[i] = t.UTC()
		case *time.Time:
			if t != nil {
				u := t.UTC()
				args[i] = &u
			}
		}
	}
	return args
}

func (p utcPool) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) { return p.db.PrepareContext(ctx, q) }
func (p utcPool) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) { return p.db.ExecContext(ctx, q, utcArgs(args)...) }
func (p utcPool) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) { return p.db.QueryContext(ctx, q, utcArgs(args)...) }
func (p utcPool) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row { return p.db.QueryRowContext(ctx, q, utcArgs(args)...) }
func (p utcPool) GetDBConn() (*sql.DB, error) { return p.db, nil }

func (p utcPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return utcTx{tx}, nil
}

type utcTx struct{ tx *sql.Tx }

func (t utcTx) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) { return t.tx.PrepareContext(ctx, q) }
func (t utcTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) { return t.tx.ExecContext(ctx, q, utcArgs(args)...) }
func (t utcTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) { return t.tx.QueryContext(ctx, q, utcArgs(args)...) }
func (t utcTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row { return t.tx.QueryRowContext(ctx, q, utcArgs(args)...) }
func (t utcTx) Commit() error   { return t.tx.Commit() }
func (t utcTx) Rollback() error { return t.tx.Rollback() }
```

- [ ] **Step 6: Implement `open.go`**

```go
package database

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/trysoft/whatomate/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OpenDialector returns the GORM dialector for cfg.Driver.
func OpenDialector(cfg *config.DatabaseConfig) (gorm.Dialector, error) {
	switch cfg.Driver {
	case "", config.DriverPostgres:
		return postgres.Open(fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name, cfg.SSLMode,
		)), nil
	case config.DriverSQLite:
		dsn := cfg.Path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		sqlDB.SetMaxOpenConns(1)
		return portableDialector{
			Dialector: sqlite.Dialector{DriverName: "sqlite", DSN: dsn, Conn: utcPool{sqlDB}},
			types:     sqliteTypes,
		}, nil
	}
	return nil, fmt.Errorf("database driver %q is not supported yet", cfg.Driver)
}

// Open connects to the configured database.
func Open(cfg *config.DatabaseConfig, debug bool) (*gorm.DB, error) {
	dialector, err := OpenDialector(cfg)
	if err != nil {
		return nil, err
	}
	logLevel := logger.Silent
	if debug {
		logLevel = logger.Info
	}
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:  logger.Default.LogMode(logLevel),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	if cfg.Driver != config.DriverSQLite {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("failed to get database instance: %w", err)
		}
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	}
	return db, nil
}

// NewPostgres is kept for callers not yet moved to Open.
func NewPostgres(cfg *config.DatabaseConfig, debug bool) (*gorm.DB, error) { return Open(cfg, debug) }
```

Notes: `NowFunc` UTC also applies to PostgreSQL; `timestamptz` columns are zone-aware so stored values are identical. If `sqlite.Dialector` field names differ (`go doc github.com/glebarez/sqlite Dialector`), adapt; the DSN pragma syntax is modernc's (`_pragma=name(value)`).

- [ ] **Step 7: Per-driver indexes (`indexes.go`)**

Move `getIndexes()` here as `indexesFor(driver string) []string`. Rules:
- Every statement in today's list works on SQLite as written (`CREATE INDEX IF NOT EXISTS`, `DESC`, partial `WHERE`), **except** `idx_contacts_tags ... USING GIN (tags)`: include it only when `driver == "postgres"`.
- `CreateIndexes(db)` and `RunMigrationWithProgress` call `indexesFor(db.Dialector.Name())`.

- [ ] **Step 8: Portable data migrations**

`MigrateUserOrganizations` becomes:

```go
func MigrateUserOrganizations(db *gorm.DB) error {
	var users []models.User
	if err := db.Raw(`
		SELECT u.* FROM users u
		LEFT JOIN user_organizations uo ON uo.user_id = u.id AND uo.organization_id = u.organization_id AND uo.deleted_at IS NULL
		WHERE uo.id IS NULL AND u.deleted_at IS NULL`).Scan(&users).Error; err != nil {
		return err
	}
	for _, u := range users {
		row := models.UserOrganization{UserID: u.ID, OrganizationID: u.OrganizationID, RoleID: u.RoleID, IsDefault: true}
		if err := db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
```

(Match `UserOrganization` field names in `models`.) In `chatbot_flow_migration.go`: replace `NOW()` with a `?` bound to `time.Now().UTC()`; replace the `COALESCE(canvas_layout, '{}'::jsonb)` / `graph::text = '{}'` query with `SELECT id, name, canvas_layout, graph FROM chatbot_flows WHERE deleted_at IS NULL`, then in Go keep rows where `graph` is nil or `len(graph) == 0`, defaulting a nil `canvas_layout` to `models.JSONB{}`.

- [ ] **Step 9: Wire `main.go`**: replace both `database.NewPostgres(` calls with `database.Open(`.
- [ ] **Step 10: Run, expect PASS**: `go test ./internal/database/...` (SQLite tests), then the PostgreSQL suite command. `CGO_ENABLED=0 go build ./...` must pass.
- [ ] **Step 11: Commit**: `git add -A && git commit -m "feat(database): Open with sqlite driver, portable types, UTC args, per-driver indexes"`

---

### Task 5: Test harness for `TEST_DB_DRIVER`

**Files:**
- Modify: `test/testutil/db.go`
- Modify: `Makefile` (add `test-sqlite`)

**Interfaces:**
- Consumes: `database.Open`, `database.AutoMigrate` (Task 4).
- Produces: `testutil.SetupTestDB(t)` works for `TEST_DB_DRIVER=postgres|sqlite`; `testutil.Driver() string`.

- [ ] **Step 1: Implement**

In `SetupTestDB`, before the DSN check:

```go
	driver := Driver()
	if driver == config.DriverSQLite {
		testDBOnce.Do(func() {
			dir, err := os.MkdirTemp("", "whatomate-test-*")
			if err != nil {
				testDBInitErr = err
				return
			}
			testDB, testDBInitErr = database.Open(&config.DatabaseConfig{Driver: driver, Path: filepath.Join(dir, "test.db")}, false)
			if testDBInitErr == nil {
				testDBInitErr = runMigrations(testDB)
			}
		})
		if testDBInitErr != nil {
			t.Fatalf("failed to initialize test database: %v", testDBInitErr)
		}
		return testDB.Session(&gorm.Session{})
	}
```

```go
// Driver returns TEST_DB_DRIVER, defaulting to postgres.
func Driver() string {
	if d := strings.ToLower(os.Getenv("TEST_DB_DRIVER")); d != "" {
		return d
	}
	return config.DriverPostgres
}
```

Make `cleanupTables` and `TruncateTables` driver-aware: on sqlite run `PRAGMA foreign_keys = OFF`, then `DELETE FROM <table>` for each, then `PRAGMA foreign_keys = ON`; postgres keeps `TRUNCATE TABLE %s CASCADE`. After `runMigrations`, call the index creation used by the app (`database.CreateIndexes(testDB)`) for both drivers so partial indexes are exercised.

Importing `internal/database` from `testutil` must not create an import cycle; if it does (database imports testutil), move the shared bits instead and note it in the commit.

- [ ] **Step 2: Makefile**

```make
test-sqlite:
	TEST_DB_DRIVER=sqlite TEST_REDIS_URL=$${TEST_REDIS_URL:-redis://127.0.0.1:6379/15} go test -p 1 -count=1 ./...
```

Add `test-sqlite` to `.PHONY` and the help text.

CI: in `.github/workflows/test.yml`, add a job `test-sqlite` after `test`. Copy the `test` job's `redis` service (no postgres service), checkout, Go setup, `go mod download` and `gotestsum` steps; the run step uses env `TEST_DB_DRIVER: sqlite` and `TEST_REDIS_URL: redis://localhost:6379` and the same `gotestsum ... -- -race -p 1 -timeout 10m $(go list ./... | grep -v /test/)` command without `-coverprofile`. Mark it `continue-on-error: true` until Task 10 is done, then remove that line in Task 10's last commit. Validate with `ruby -ryaml -e 'YAML.load_file(".github/workflows/test.yml")'`.

- [ ] **Step 3: Run the SQLite suite and record the failure list** (expected: many, from Tasks 6-9's SQL): `make test-sqlite 2>&1 | rg "^(--- FAIL|FAIL|ok)" > /tmp/sqlite-baseline.txt`
- [ ] **Step 4: PostgreSQL suite must still pass.**
- [ ] **Step 5: Commit**: `git commit -am "test: run the suite on sqlite with TEST_DB_DRIVER"`

---

### Task 6: Case-insensitive search call sites

**Files (all in `internal/handlers/`):** `chatbot.go:681,959,1541`, `webhooks.go:135`, `custom_actions.go:94`, `widgets.go:1264,1266`, `contacts.go:117`, `templates.go:87`, `users.go:112-113`, `canned_responses.go:83`, `apikeys.go:66`, `campaigns.go:85`, `roles.go:74`, `organization.go:745`, `flows.go:80`, `teams.go:72,86`, `import_export.go:237,239`
- Test: `internal/handlers/search_portable_test.go`

**Interfaces:**
- Consumes: `dialect.Of(db).ILike(col)`, `dialect.Of(db).AsText(col)` (Task 2).

- [ ] **Step 1: Write the failing test** (runs on whichever `TEST_DB_DRIVER` is active)

```go
func TestSearch_CaseInsensitive(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	require.NoError(t, app.DB.Create(&models.CannedResponse{OrganizationID: org.ID, Name: "Greeting Hello", Content: "x", CreatedBy: user.ID}).Error)

	req := testutil.NewGETRequest(t)
	req.RequestCtx.QueryArgs().Set("search", "greeting hello")
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListCannedResponses(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), "Greeting Hello")
}

// SQLite folds ASCII only; documented limitation.
func TestSearch_NonASCIIFolding(t *testing.T) {
	if testutil.Driver() != "sqlite" {
		t.Skip("postgres folds non-ASCII")
	}
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	require.NoError(t, app.DB.Create(&models.CannedResponse{OrganizationID: org.ID, Name: "Émile", Content: "x", CreatedBy: user.ID}).Error)
	req := testutil.NewGETRequest(t)
	req.RequestCtx.QueryArgs().Set("search", "émile")
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListCannedResponses(req))
	assert.NotContains(t, string(testutil.GetResponseBody(req)), "Émile")
}
```

(Match `CannedResponse` fields and the list handler name by reading `canned_responses.go`; adjust `CreateTestUser` options to give the user permission if the handler checks it, following an existing canned-response test.)

- [ ] **Step 2: Run on sqlite, expect FAIL** (`near "ILIKE": syntax error`).
- [ ] **Step 3: Replace each call site**, e.g. `chatbot.go:681`:

```go
		d := dialect.Of(a.DB)
		query = query.Where(d.ILike("name")+" OR "+d.ILike(d.AsText("keywords")), searchPattern, searchPattern)
```

`widgets.go:1264`: `return dialect.Of(a.DB).ILike(field), []any{"%" + value + "%"}, true` (and the prefix variant). Same pattern for every listed line; keep placeholders and argument order unchanged.

- [ ] **Step 4: Run both suites; the sqlite failures for search handlers are gone; postgres stays green.** `rg -n "ILIKE|::text" internal --type go -g '!*_test.go'` returns nothing outside `internal/database/dialect`.
- [ ] **Step 5: Commit**: `git commit -am "refactor(handlers): portable case-insensitive search"`

---

### Task 7: JSON queries (tag filters, campaign metadata, tag rename/delete)

**Files:** `internal/handlers/contacts.go:118-136`, `import_export.go:245-255`, `campaigns.go:602,1122`, `tags.go:160-180, 248-260`
- Test: `internal/handlers/tags_portable_test.go`

**Interfaces:**
- Consumes: `dialect.Of(db).JSONArrayHas`, `dialect.Of(db).JSONText` (Task 2).
- Produces: `func (a *App) rewriteContactTags(orgID uuid.UUID, from, to string) error` (`to == ""` removes the tag).

- [ ] **Step 1: Write the failing tests**

```go
func TestTags_RenameAndDeleteRewriteContacts(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	odd := `vip"1 50%`
	c1 := testutil.CreateTestContact(t, app.DB, org.ID)
	c2 := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(c1).Update("tags", models.JSONBArray{odd, "keep"}).Error)
	require.NoError(t, app.DB.Model(c2).Update("tags", models.JSONBArray{"keep"}).Error)

	require.NoError(t, app.RewriteContactTagsForTest(org.ID, odd, "gold"))
	var got models.Contact
	require.NoError(t, app.DB.First(&got, "id = ?", c1.ID).Error)
	assert.ElementsMatch(t, models.JSONBArray{"gold", "keep"}, got.Tags)

	require.NoError(t, app.RewriteContactTagsForTest(org.ID, "gold", ""))
	require.NoError(t, app.DB.First(&got, "id = ?", c1.ID).Error)
	assert.Equal(t, models.JSONBArray{"keep"}, got.Tags)
	require.NoError(t, app.DB.First(&got, "id = ?", c2.ID).Error)
	assert.Equal(t, models.JSONBArray{"keep"}, got.Tags)
}

func TestContacts_FilterByTagWithSpecialChars(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	hit := testutil.CreateTestContact(t, app.DB, org.ID)
	miss := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(hit).Update("tags", models.JSONBArray{"50%"}).Error)
	require.NoError(t, app.DB.Model(miss).Update("tags", models.JSONBArray{"500"}).Error)

	req := testutil.NewGETRequest(t)
	req.RequestCtx.QueryArgs().Set("tags", "50%")
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListContacts(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, hit.ID.String())
	assert.NotContains(t, body, miss.ID.String())
}
```

Add `RewriteContactTagsForTest` in `internal/handlers/tags_export_test.go` (package `handlers`) calling `rewriteContactTags`. Match helper/handler names to the real ones (`CreateTestContact`, `ListContacts`, `Contact.Tags` type) and give the user contact-read permission the way existing contact tests do.

- [ ] **Step 2: Run on sqlite, expect FAIL.**
- [ ] **Step 3: Implement**

`contacts.go` and `import_export.go` tag filter loops:

```go
		d := dialect.Of(a.DB)
		for _, tag := range tagList {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				cond, arg := d.JSONArrayHas("tags", tag)
				conditions = append(conditions, cond)
				args = append(args, arg)
			}
		}
```

`campaigns.go:602` and `:1122`: `Where(dialect.Of(a.DB).JSONText("metadata", "campaign_id")+" = ? AND status = ?", ...)` (and the single-condition variant).

`tags.go`: replace both raw `UPDATE contacts ...` statements with `a.rewriteContactTags(orgID, tagName, req.Name)` and `a.rewriteContactTags(orgID, tagName, "")`:

```go
// rewriteContactTags renames (or, with to == "", removes) a tag on every
// contact in the org. Done in Go so it works on every database.
func (a *App) rewriteContactTags(orgID uuid.UUID, from, to string) error {
	cond, arg := dialect.Of(a.DB).JSONArrayHas("tags", from)
	return a.DB.Transaction(func(tx *gorm.DB) error {
		var contacts []models.Contact
		return tx.Select("id", "tags").Where("organization_id = ?", orgID).Where(cond, arg).
			FindInBatches(&contacts, 500, func(batch *gorm.DB, _ int) error {
				for _, c := range contacts {
					next := make(models.JSONBArray, 0, len(c.Tags))
					seen := map[string]bool{}
					for _, v := range c.Tags {
						s, _ := v.(string)
						if s == from {
							if to == "" {
								continue
							}
							s = to
						}
						if !seen[s] {
							seen[s] = true
							next = append(next, s)
						}
					}
					if err := batch.Model(&models.Contact{}).Where("id = ?", c.ID).Update("tags", next).Error; err != nil {
						return err
					}
				}
				return nil
			}).Error
	})
}
```

(If `Contact.Tags` is not `models.JSONBArray`, adapt the element handling to its real type. Keep the existing "log and continue" behaviour at the call sites.)

- [ ] **Step 4: Run both suites; JSON-related sqlite failures gone; postgres green.** `rg -n "@>|->>|jsonb_" internal --type go -g '!*_test.go'` only matches `internal/database/dialect`.
- [ ] **Step 5: Commit**: `git commit -am "refactor(handlers): portable tag filters, campaign metadata lookups and tag rewrites"`

---

### Task 8: Date and time SQL (analytics, dashboard widgets)

**Files (all in `internal/handlers/`):** `agent_analytics.go:237,246,255,296,305,367,375,470-495`, `widgets.go:938-946,1110-1126,1195-1212`, `widgets_displays.go:10-20,220-242`, `widgets_sources.go:290-305`
- Test: `internal/handlers/analytics_portable_test.go`

**Interfaces:**
- Consumes: `MinutesBetween`, `DateTrunc`, `DayOfWeek`, `HourOf`, `Now`, `DBTime` (Task 2).

- [ ] **Step 1: Write the failing tests**

```go
func TestAgentAnalytics_AvgPickupMinutes(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	picked := start.Add(10 * time.Minute)
	resumed := start.Add(30 * time.Minute)
	require.NoError(t, app.DB.Create(&models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "t", PhoneNumber: contact.PhoneNumber,
		Status: models.TransferStatusResumed, Source: models.TransferSourceManual,
		TransferredAt: start, PickedUpAt: &picked, ResumedAt: &resumed,
	}).Error)

	var avg float64
	d := dialect.Of(app.DB)
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
		Select("AVG("+d.MinutesBetween("transferred_at", "picked_up_at")+")").
		Where("organization_id = ?", org.ID).Row().Scan(&avg))
	assert.InDelta(t, 10, avg, 0.01)
}

func TestDateTrunc_GroupsByDay(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, h := range []int{1, 5, 23} {
		require.NoError(t, app.DB.Create(&models.AgentTransfer{
			OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "t", PhoneNumber: contact.PhoneNumber,
			Status: models.TransferStatusResumed, Source: models.TransferSourceManual,
			TransferredAt: day.Add(time.Duration(h) * time.Hour),
		}).Error)
	}
	d := dialect.Of(app.DB)
	var rows []struct {
		Date  dialect.DBTime
		Count int64
	}
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
		Select(d.DateTrunc("day", "transferred_at")+" AS date, COUNT(*) AS count").
		Where("organization_id = ?", org.ID).
		Group(d.DateTrunc("day", "transferred_at")).Scan(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "2026-10-05", rows[0].Date.Format("2006-01-02"))
	assert.EqualValues(t, 3, rows[0].Count)
}
```

Plus one handler-level test per area so the real SQL runs: call the existing agent-analytics summary handler and the dashboard widget data handler for a `trend`/`heatmap` widget, asserting a 200 status (copy request setup from `agent_analytics_test.go` / `widgets_data_test.go`).

- [ ] **Step 2: Run on sqlite, expect FAIL.**
- [ ] **Step 3: Replace each expression**:
- `AVG(EXTRACT(EPOCH FROM (X - transferred_at))/60)` → `"AVG(" + d.MinutesBetween("transferred_at", "X") + ")"`.
- `widgets_sources.go:296-302` `expr = ...` → `d.MinutesBetween("transferred_at", "resumed_at")` etc.
- `widgets_displays.go:15` → `d.MinutesBetween("t.transferred_at", d.Now()) + " as wait_mins"`.
- `widgets_displays.go:225/234` → `d.DayOfWeek(dateField)`, `d.HourOf(dateField)` in both SELECT and GROUP BY.
- `DATE_TRUNC('day', X)` / `DATE_TRUNC('"+dateTrunc+"', transferred_at)` → `d.DateTrunc("day", X)` / `d.DateTrunc(dateTrunc, "transferred_at")` in SELECT and GROUP BY.
- Every result struct that scans one of these into `time.Time` (`DailyCount.Date`, `TrendResult.Date`, and the structs at `widgets.go:1110-1126` and `1195-1212`) changes the field to `dialect.DBTime`; uses of `.Date` keep working through the embedded `time.Time` (`r.Date.Format(...)`), and where a `time.Time` is required use `r.Date.Time`.

- [ ] **Step 4: Run both suites; date-related sqlite failures gone; postgres green.** `rg -n "EXTRACT\(|DATE_TRUNC|NOW\(\)" internal --type go -g '!*_test.go'` only matches `internal/database/dialect`.
- [ ] **Step 5: Commit**: `git commit -am "refactor(handlers): portable date math in analytics and dashboard widgets"`

---

### Task 9: Row locking when an agent picks a transfer

**Files:** `internal/handlers/agent_transfers.go:889-905`
- Test: existing `agent_transfers_test.go` pickup tests (run on sqlite)

- [ ] **Step 1: Run the existing pickup tests on sqlite, expect FAIL** (`FOR UPDATE` syntax): `TEST_DB_DRIVER=sqlite go test ./internal/handlers/ -run Pickup -count=1`
- [ ] **Step 2: Implement**

```go
	query := tx.Where("organization_id = ? AND status = ? AND agent_id IS NULL", orgID, models.TransferStatusActive).
		Order("transferred_at ASC")
	if dialect.Of(tx).SupportsSkipLocked() {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	}
```

SQLite connections open transactions with `_txlock=immediate` (Task 4), so the pick-and-assign transaction already holds the write lock.

- [ ] **Step 3: Run, expect PASS on both drivers.**
- [ ] **Step 4: Commit**: `git commit -am "refactor(handlers): skip-locked pickup only where supported"`

---

### Task 10: SQLite suite to green

**Files:** whatever the remaining failures point to (production code and test files).

- [ ] **Step 1:** `make test-sqlite 2>&1 | rg "^(--- FAIL|FAIL|panic)"` and list every failure.
- [ ] **Step 2:** For each failure, classify: (a) PostgreSQL SQL in production code missed by the audit: move it onto a `dialect` helper (add a helper plus a `TestHelpers` row if none fits); (b) PostgreSQL SQL inside a test: rewrite the test portably (GORM API or `dialect`); (c) a test that is genuinely PostgreSQL-specific: guard with `if testutil.Driver() != "postgres" { t.Skip("postgres-specific: <reason>") }`, at most a handful, each with a reason.
- [ ] **Step 3:** Repeat until `make test-sqlite` passes. Then the PostgreSQL suite command must also pass. Remove `continue-on-error: true` from the `test-sqlite` CI job (Task 5).
- [ ] **Step 4: Commit** per logical fix: `git commit -am "fix(sqlite): <what>"`.

---

### Task 11: Run the app on SQLite, config example, docs note

**Files:** `config.example.toml`, `docs/src/content/docs/getting-started/configuration.mdx` (`[database]` section)

- [ ] **Step 1:** In `config.example.toml` `[database]` add at the top:

```toml
driver = "postgres"  # postgres | sqlite (mysql and sqlserver coming)
# path = "./whatomate.db"  # sqlite only
```

- [ ] **Step 2:** In `configuration.mdx` `[database]` section, replace "PostgreSQL only." with a short table: `driver` (`postgres` default, `sqlite`), `path` (sqlite file, default `./whatomate.db`), and a note: SQLite suits a single server; separate worker machines need PostgreSQL; Redis is still required.
- [ ] **Step 3: Manual check:** `make build-prod`, then with a copy of `config.local.toml` setting `driver = "sqlite"` and `path = "/tmp/whatomate-sqlite.db"`: `./whatomate server -config /tmp/whatomate-sqlite.toml -migrate` on a free port (`[server] port = 8091`); `curl -sf http://127.0.0.1:8091/health`; log in as the default admin through the API (`POST /api/auth/login`); create a contact with a tag; filter contacts by that tag; stop the server; delete the temp files.
- [ ] **Step 4:** Both suites green, `CGO_ENABLED=0 go build ./...` green, `make release-snapshot` green.
- [ ] **Step 5: Commit**, then merge into `feat/multi-database`:

```bash
git commit -am "docs: sqlite database driver"
git checkout feat/multi-database && git merge --no-edit feat/multi-database-stage1
```
