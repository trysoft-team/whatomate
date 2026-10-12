# Multi-database Stage 2 (MySQL) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Whatomate runs on MySQL 8.0.13+ with `[database] driver = "mysql"`, with the full Go test suite green on PostgreSQL, SQLite and MySQL.

**Architecture:** Same as stage 1. `database.OpenDialector` builds a `gorm.io/driver/mysql` dialector wrapped in `portableDialector` (`uuid` → `char(36)`, `jsonb` → `json`, `text` → `longtext`). A MySQL migrator wrapper turns literal defaults on JSON columns into expression defaults and keeps repeat migrations from altering them. Index definitions become structured so each driver renders its own SQL. `dialect` gains explicit MySQL branches and rejects unknown driver names (bead fwb.7).

**Tech Stack:** Go 1.25, GORM v1.25.7, `gorm.io/driver/mysql` v1.5.7 (requires `gorm.io/gorm v1.25.7`, `github.com/go-sql-driver/mysql v1.7.0`: no GORM upgrade), MySQL 8.4 LTS for tests.

**Spec:** `docs/superpowers/specs/2026-10-11-multi-database-design.md`

Branch: `feat/multi-database-stage2` (off `feat/multi-database`). Merge back into `feat/multi-database` when Task 7 is green. Beads: fwb.7 (Task 1), fwb.2 (whole plan).

## Deviations from the spec

- **MariaDB is not supported in this stage.** It lacks functional index key parts (needed to keep partial unique indexes correct) and `utf8mb4_0900_ai_ci`. `Open` rejects MariaDB with a clear error; a follow-up bead covers it.
- **Partial unique indexes keep their meaning.** The spec said "MySQL gets the index without the `WHERE`"; that would stop a soft-deleted team member from being re-added. Partial *unique* indexes instead get a functional key part `(CASE WHEN <predicate> THEN 1 END)`: rows outside the predicate produce NULL and never conflict. Partial non-unique indexes drop the `WHERE`.
- `ILike` on MySQL is `LOWER(col) LIKE LOWER(?)`, so it does not depend on the column collation.
- `Now()` on MySQL is `UTC_TIMESTAMP(6)`, not `NOW()`.

## Global Constraints

- Minimum MySQL 8.0.13 (functional key parts, expression defaults). Database character set must be `utf8mb4`; `Open` checks both.
- Connection settings (all set by `mysqlDSN`, never by the operator): `parseTime=true`, `loc=UTC`, `time_zone='+00:00'`, collation `utf8mb4_0900_ai_ci`, `clientFoundRows=true` (RowsAffected counts matched rows like PostgreSQL), `sql_mode` = MySQL 8 default minus `NO_ZERO_DATE`/`NO_ZERO_IN_DATE` (zero `time.Time` round-trips as `0000-00-00`).
- `datetime(6)` for all time columns (`DefaultDatetimePrecision: 6`), matching PostgreSQL microseconds.
- Existing PostgreSQL and SQLite behaviour and schema unchanged. The rendered index SQL for both is frozen by a golden test (Task 3).
- Release binaries stay `CGO_ENABLED=0` (go-sql-driver is pure Go). Never import `gorm.io/driver/sqlite` or `mattn/go-sqlite3`.
- Go stays at 1.25 (`go 1.25.0`). Do not bump `gorm.io/gorm`.
- Never point tests at the app database. `TEST_MYSQL_DSN`'s database name must end in `_test` (testutil panics otherwise).
- Test commands:
  - PostgreSQL: `set -a && . ./.env && set +a; TEST_DATABASE_URL="postgres://whatomate:${POSTGRES_PASSWORD}@127.0.0.1:5433/whatomate_test?sslmode=disable" TEST_REDIS_URL="redis://127.0.0.1:6379/15" go test -p 1 -count=1 ./...`
  - SQLite: `TEST_DB_DRIVER=sqlite TEST_REDIS_URL="redis://127.0.0.1:6379/15" go test -p 1 -count=1 ./...`
  - MySQL: `TEST_DB_DRIVER=mysql TEST_MYSQL_DSN='root:whatomate_test@tcp(127.0.0.1:3307)/whatomate_test' TEST_REDIS_URL="redis://127.0.0.1:6379/15" go test -p 1 -count=1 ./...`
- The MySQL test container is `whatomate_mysql_test` (compose profile `mysql-test`, `127.0.0.1:3307`, tmpfs data). Start it with `docker compose --profile mysql-test up -d mysql-test`. Never stop or recreate any other container.
- Commit after each task on `feat/multi-database-stage2`. Local git only, never push.

## Review Focus

1. **Soft-deleted rows and unique indexes**: removing a team member, then adding them back, must work on MySQL; two active memberships must still be rejected. Pinned in Task 3.
2. **JSON column defaults**: a contact created without tags reads back `[]`, not `null`. Pinned in Task 2 (`TestOpenMySQL_JSONDefaults`).
3. **Repeat boots**: a second migration must issue no `ALTER TABLE`. Pinned in Task 2 (`TestOpenMySQL_RepeatMigrationAltersNothing`).
4. **Times**: a handler passing a local-zone `time.Now()` still matches UTC rows; microseconds survive a round trip; zero `time.Time` round-trips. Pinned in Task 2.
5. **"Not found" on unchanged updates**: an UPDATE that sets the same values must report 1 row affected. Pinned in Task 2 (`TestOpenMySQL_RowsAffectedCountsMatchedRows`).
6. **Case-insensitive unique names (accepted, documented)**: under `utf8mb4_0900_ai_ci`, `Sales` and `sales` collide in unique indexes (canned responses, custom roles, templates) where PostgreSQL allows both. Documented in Task 6.

---

### Task 1: Dialect hardening and MySQL fragments (fwb.7)

**Files:**
- Modify: `internal/database/dialect/dialect.go`, `internal/database/utc_pool.go` (comment only)
- Test: `internal/database/dialect/dialect_test.go`

- [ ] **Step 1: Write the failing tests**

Replace `TestHelpers` with a three-driver table, and add:

```go
func TestHelpers(t *testing.T) {
	pg, lite, my := dialect.Named("postgres"), dialect.Named("sqlite"), dialect.Named("mysql")
	cases := []struct {
		name          string
		pg, lite, my  string
		wantPG, wantLite, wantMy string
	}{
		{"ILike", pg.ILike("name"), lite.ILike("name"), my.ILike("name"),
			"name ILIKE ?", "name LIKE ?", "LOWER(name) LIKE LOWER(?)"},
		{"AsText", pg.AsText("k"), lite.AsText("k"), my.AsText("k"),
			"k::text", "CAST(k AS TEXT)", "CAST(k AS CHAR)"},
		{"JSONText", pg.JSONText("m", "id"), lite.JSONText("m", "id"), my.JSONText("m", "id"),
			"m->>'id'", "json_extract(m, '$.id')", "JSON_UNQUOTE(JSON_EXTRACT(m, '$.id'))"},
		{"MinutesBetween", pg.MinutesBetween("a", "b"), lite.MinutesBetween("a", "b"), my.MinutesBetween("a", "b"),
			"EXTRACT(EPOCH FROM (b - a))/60", "((julianday(b) - julianday(a)) * 1440)", "(TIMESTAMPDIFF(MICROSECOND, a, b) / 60000000)"},
		{"DayOfWeek", pg.DayOfWeek("c"), lite.DayOfWeek("c"), my.DayOfWeek("c"),
			"EXTRACT(DOW FROM c)::int", "CAST(strftime('%w', c) AS INTEGER)", "(DAYOFWEEK(c) - 1)"},
		{"HourOf", pg.HourOf("c"), lite.HourOf("c"), my.HourOf("c"),
			"EXTRACT(HOUR FROM c)::int", "CAST(strftime('%H', c) AS INTEGER)", "HOUR(c)"},
		{"DateTrunc day", pg.DateTrunc("day", "c"), lite.DateTrunc("day", "c"), my.DateTrunc("day", "c"),
			"DATE_TRUNC('day', c)", "date(c)", "DATE(c)"},
		{"DateTrunc week", pg.DateTrunc("week", "c"), lite.DateTrunc("week", "c"), my.DateTrunc("week", "c"),
			"DATE_TRUNC('week', c)", "date(c, '-6 days', 'weekday 1')", "DATE(DATE_SUB(c, INTERVAL WEEKDAY(c) DAY))"},
		{"DateTrunc month", pg.DateTrunc("month", "c"), lite.DateTrunc("month", "c"), my.DateTrunc("month", "c"),
			"DATE_TRUNC('month', c)", "strftime('%Y-%m-01', c)", "CAST(DATE_FORMAT(c, '%Y-%m-01') AS DATE)"},
		{"Now", pg.Now(), lite.Now(), my.Now(), "NOW()", "CURRENT_TIMESTAMP", "UTC_TIMESTAMP(6)"},
		{"DescNullsLast", pg.DescNullsLast("c"), lite.DescNullsLast("c"), my.DescNullsLast("c"),
			"c DESC NULLS LAST", "c DESC NULLS LAST", "c DESC"},
		{"AscNullsFirst", pg.AscNullsFirst("c"), lite.AscNullsFirst("c"), my.AscNullsFirst("c"),
			"c ASC NULLS FIRST", "c ASC NULLS FIRST", "c ASC"},
	}
	for _, c := range cases {
		assert.Equal(t, c.wantPG, c.pg, c.name+" postgres")
		assert.Equal(t, c.wantLite, c.lite, c.name+" sqlite")
		assert.Equal(t, c.wantMy, c.my, c.name+" mysql")
	}
}

func TestJSONArrayHas(t *testing.T) {
	sql, arg := dialect.Named("mysql").JSONArrayHas("tags", `vip"1`)
	assert.Equal(t, "JSON_CONTAINS(tags, JSON_QUOTE(?))", sql)
	assert.Equal(t, `vip"1`, arg)
	// keep the existing postgres and sqlite assertions
}

func TestCapabilities(t *testing.T) {
	for name, want := range map[string][2]bool{"postgres": {true, true}, "sqlite": {false, false}, "mysql": {true, false}} {
		d := dialect.Named(name)
		assert.Equal(t, want[0], d.SupportsSkipLocked(), name)
		assert.Equal(t, want[1], d.SupportsPercentileCont(), name)
	}
}

func TestNamed_RejectsUnknownDriver(t *testing.T) {
	assert.PanicsWithValue(t, `dialect: unsupported driver "oracle"`, func() { dialect.Named("oracle") })
}

func TestDateTrunc_RejectsUnknownUnit(t *testing.T) {
	for _, name := range []string{"postgres", "sqlite", "mysql"} {
		assert.PanicsWithValue(t, "dialect: unsupported DateTrunc unit year", func() { dialect.Named(name).DateTrunc("year", "c") }, name)
	}
}
```

Extend `TestDBTimeScan` with: a non-UTC offset string (`"2026-10-12 09:30:00+02:00"` → equals `07:30Z`), fractional seconds (`"2026-10-12 09:30:00.123456"`), a `time.Time` from MySQL (`DATE` → midnight UTC), and a bad type (`42` → error containing `cannot scan int`).

- [ ] **Step 2: Run, expect FAIL** (`my.DescNullsLast undefined`, mysql strings wrong, no panic on `oracle`)

Run: `go test ./internal/database/dialect/`

- [ ] **Step 3: Implement**

Switch every helper on the driver name using the `config.Driver*` constants; no `else` falls back to SQLite. Sketch (apply the same shape to every helper):

```go
type Dialect struct{ name string }

// Of returns the dialect of db's driver. It panics on a driver this package
// has no SQL for; Open rejects those first.
func Of(db *gorm.DB) Dialect { return Named(db.Dialector.Name()) }

func Named(name string) Dialect {
	switch name {
	case config.DriverPostgres, config.DriverSQLite, config.DriverMySQL:
		return Dialect{name: name}
	}
	panic(fmt.Sprintf("dialect: unsupported driver %q", name))
}

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
```

`default` is allowed only where it means SQLite (`Named` guarantees the three names). MySQL fragments are exactly the `wantMy` strings above; `JSONArrayHas` returns `"JSON_CONTAINS(" + col + ", JSON_QUOTE(?))", value`. New helpers:

```go
// DescNullsLast orders col descending with NULLs after values. MySQL already
// sorts NULLs last when descending and has no NULLS LAST syntax.
func (d Dialect) DescNullsLast(col string) string

// AscNullsFirst orders col ascending with NULLs first, MySQL's default.
func (d Dialect) AscNullsFirst(col string) string
```

`SupportsSkipLocked`: postgres and mysql. `SupportsPercentileCont`: postgres only. In `utc_pool.go`, add to the `utcPool` doc comment: it only sees arguments passed through `QueryContext`/`ExecContext`/`PrepareContext`; a `driver.Valuer` returning a local `time.Time`, or a `*sql.Conn` taken from `db.DB()`, bypasses it.

- [ ] **Step 4: Run, expect PASS**; then `go build ./... && go vet ./internal/database/...`
- [ ] **Step 5: Commit** `feat(dialect): explicit drivers, MySQL fragments, NULLS ordering helpers` and `bd close whatomate-fwb.7`

---

### Task 2: MySQL connection, column types and migrator

**Files:**
- Modify: `go.mod`/`go.sum`, `internal/database/open.go`, `internal/database/portable_dialector.go`, `docker-compose.yml`
- Create: `internal/database/mysql.go` (DSN, version/charset check, migrator wrapper)
- Test: `internal/database/mysql_test.go` (package `database`, unit), `internal/database/open_mysql_test.go` (package `database_test`, live; skips without `TEST_MYSQL_DSN`)

- [ ] **Step 1: Test container**

Add to `docker-compose.yml` (profile-gated so `docker compose up` never starts it):

```yaml
  mysql-test:
    image: mysql:8.4
    container_name: whatomate_mysql_test
    profiles: ["mysql-test"]
    environment:
      MYSQL_ROOT_PASSWORD: whatomate_test
      MYSQL_DATABASE: whatomate_test
    command: ["--character-set-server=utf8mb4", "--collation-server=utf8mb4_0900_ai_ci"]
    ports:
      - "127.0.0.1:3307:3306"
    tmpfs:
      - /var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "127.0.0.1", "-pwhatomate_test"]
      interval: 5s
      timeout: 5s
      retries: 20
```

Run `docker compose --profile mysql-test up -d mysql-test` and wait until healthy (`docker inspect -f '{{.State.Health.Status}}' whatomate_mysql_test`). The password is a throwaway for a loopback-only test container.

- [ ] **Step 2: `go get gorm.io/driver/mysql@v1.5.7`**; confirm `go.mod` still has `gorm.io/gorm v1.25.7` and `go 1.25.0`.

- [ ] **Step 3: Write the failing unit tests** (`mysql_test.go`, package `database`)

```go
func TestMySQLDSN(t *testing.T) {
	dsn, err := mysqlDSN(&config.DatabaseConfig{Host: "db", Port: 3306, User: "u", Password: "p@ss:w/rd?&", Name: "app", SSLMode: "disable"})
	require.NoError(t, err)
	c, err := mysqldrv.ParseDSN(dsn)
	require.NoError(t, err)
	assert.Equal(t, "p@ss:w/rd?&", c.Passwd)
	assert.Equal(t, "db:3306", c.Addr)
	assert.Equal(t, "app", c.DBName)
	assert.True(t, c.ParseTime)
	assert.Equal(t, time.UTC, c.Loc)
	assert.True(t, c.ClientFoundRows)
	assert.Equal(t, "utf8mb4_0900_ai_ci", c.Collation)
	assert.Equal(t, "'+00:00'", c.Params["time_zone"])
	assert.NotContains(t, c.Params["sql_mode"], "NO_ZERO_DATE")
	assert.Equal(t, "false", c.TLSConfig)
}

func TestMySQLDSN_SSLModes(t *testing.T) {
	for mode, want := range map[string]string{"": "false", "disable": "false", "require": "skip-verify", "verify-ca": "true", "verify-full": "true", "preferred": "preferred"} {
		dsn, err := mysqlDSN(&config.DatabaseConfig{Host: "h", Port: 3306, SSLMode: mode})
		require.NoError(t, err, mode)
		c, _ := mysqldrv.ParseDSN(dsn)
		assert.Equal(t, want, c.TLSConfig, mode)
	}
	_, err := mysqlDSN(&config.DatabaseConfig{SSLMode: "bogus"})
	assert.EqualError(t, err, `unsupported ssl_mode "bogus" for mysql`)
}

func TestCheckMySQLServer(t *testing.T) {
	assert.NoError(t, checkMySQLServer("8.0.13", "utf8mb4"))
	assert.NoError(t, checkMySQLServer("8.4.3", "utf8mb4"))
	assert.EqualError(t, checkMySQLServer("8.0.12", "utf8mb4"), "MySQL 8.0.13 or later is required (server is 8.0.12)")
	assert.EqualError(t, checkMySQLServer("5.7.44-log", "utf8mb4"), "MySQL 8.0.13 or later is required (server is 5.7.44-log)")
	assert.EqualError(t, checkMySQLServer("10.11.6-MariaDB", "utf8mb4"), "MariaDB is not supported yet; use MySQL 8.0.13 or later")
	assert.EqualError(t, checkMySQLServer("8.4.3", "latin1"), `database character set is "latin1"; create it with CHARACTER SET utf8mb4`)
}

func TestNormalizeMySQLDefault(t *testing.T) {
	for in, want := range map[string]string{`_utf8mb4\'[]\'`: "[]", `'[]'`: "[]", `('{}')`: "{}", `_utf8mb4\'{}\'`: "{}"} {
		assert.Equal(t, want, normalizeMySQLDefault(in), in)
	}
}
```

- [ ] **Step 4: Write the failing live tests** (`open_mysql_test.go`)

`openMySQL(t)` parses `TEST_MYSQL_DSN` (skip if empty), creates a fresh database `wt_<8 random hex>` with `CREATE DATABASE ... CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci` over a plain `sql.Open("mysql", ...)`, drops it in `t.Cleanup`, and returns `database.Open(&config.DatabaseConfig{Driver: "mysql", Host, Port, User, Password, Name: "wt_...", SSLMode: "disable"}, false)` after `RunMigrationWithProgress`. Tests:

- `TestOpenMySQL_MigratesAndSeeds`: `dialect.Of(db).Name() == "mysql"`; admin seeded; `information_schema.columns` shows `contacts.id` = `char(36)`, `contacts.tags` = `json`, `messages.content` = `longtext`, `contacts.created_at` = `datetime(6)`.
- `TestOpenMySQL_RepeatMigrationAltersNothing`: same capture callback as the SQLite test, pattern `(?i)^\s*(CREATE TABLE|ALTER TABLE|DROP TABLE)`; second `RunMigrationWithProgress` captures nothing.
- `TestOpenMySQL_JSONDefaults`: create a `models.Contact` without `Tags`; reload; `Tags` is non-nil and empty; raw `SELECT CAST(tags AS CHAR)` = `[]`.
- `TestOpenMySQL_TimeArgsAreUTC`: as the SQLite version (local-zone `since` still matches).
- `TestOpenMySQL_TimePrecisionAndZero`: a row with `time.Date(2026,10,12,9,30,0,123456000,time.UTC)` reloads equal; a zero `time.Time` field (pick one non-pointer `time.Time` column in the models, or a temp table via `AutoMigrate` of a local struct) round-trips as zero.
- `TestOpenMySQL_RowsAffectedCountsMatchedRows`: `UPDATE organizations SET name = name WHERE id = ?` reports `RowsAffected == 1`.
- `TestOpenMySQL_NestedTransactionRollsBackToSavepoint`: as the SQLite version.

- [ ] **Step 5: Run, expect FAIL**

Run: `TEST_MYSQL_DSN='root:whatomate_test@tcp(127.0.0.1:3307)/whatomate_test' go test ./internal/database/ -run 'MySQL'`

- [ ] **Step 6: Implement**

`mysql.go`:

```go
const mysqlCollation = "utf8mb4_0900_ai_ci"

// MySQL 8's default sql_mode without NO_ZERO_DATE/NO_ZERO_IN_DATE: the driver
// writes a zero time.Time as 0000-00-00 and reads it back as zero.
const mysqlSQLMode = "'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION'"

var mysqlTypes = map[string]string{"uuid": "char(36)", "jsonb": "json", "text": "longtext"}

func mysqlDSN(cfg *config.DatabaseConfig) (string, error) {
	tls, err := mysqlTLS(cfg.SSLMode)
	if err != nil {
		return "", err
	}
	c := mysqldrv.NewConfig()
	c.User, c.Passwd = cfg.User, cfg.Password
	c.Net, c.Addr = "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	c.DBName = cfg.Name
	c.ParseTime, c.Loc = true, time.UTC
	c.Collation = mysqlCollation
	c.ClientFoundRows = true // RowsAffected counts matched rows, as on PostgreSQL
	c.TLSConfig = tls
	c.Params = map[string]string{"time_zone": "'+00:00'", "sql_mode": mysqlSQLMode}
	return c.FormatDSN(), nil
}

// mysqlTLS maps the PostgreSQL-style ssl_mode values to the driver's tls param.
func mysqlTLS(mode string) (string, error) {
	switch mode {
	case "", "disable":
		return "false", nil
	case "require":
		return "skip-verify", nil
	case "verify-ca", "verify-full":
		return "true", nil
	case "preferred":
		return "preferred", nil
	}
	return "", fmt.Errorf("unsupported ssl_mode %q for mysql", mode)
}

// checkMySQLServer rejects servers without functional key parts and
// expression defaults (MySQL < 8.0.13), MariaDB, and non-utf8mb4 databases.
func checkMySQLServer(version, charset string) error
```

`checkMySQLServer` parses the leading `major.minor.patch` of `VERSION()`; the error strings are exactly those in the test. `OpenDialector`'s `case config.DriverMySQL:` builds the DSN and returns `portableDialector{Dialector: mysql.New(mysql.Config{DSN: dsn, DefaultDatetimePrecision: &six}), types: mysqlTypes}`. In `Open`, after `gorm.Open` and pool settings, for MySQL run `SELECT VERSION(), @@character_set_database` and return `checkMySQLServer`'s error (closing the pool first).

Migrator wrapper (in `mysql.go`) and the `portableDialector.Migrator` switch:

```go
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

// mysqlMigrator writes literal defaults on JSON and TEXT columns as
// expression defaults, which MySQL requires, and treats the value MySQL
// reports back for them as equal to the model's, so boots don't re-alter.
type mysqlMigrator struct{ mysql.Migrator }

func (m mysqlMigrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	if needsExprDefault(m.DataTypeOf(field), field) {
		f := *field
		f.DefaultValue = "(" + field.DefaultValue + ")"
		return m.Migrator.FullDataTypeOf(&f)
	}
	return m.Migrator.FullDataTypeOf(field)
}

func (m mysqlMigrator) MigrateColumn(value any, field *schema.Field, ct gorm.ColumnType) error {
	if dv, ok := ct.DefaultValue(); ok && needsExprDefault(m.DataTypeOf(field), field) &&
		normalizeMySQLDefault(dv) == normalizeMySQLDefault(field.DefaultValue) {
		ct = reportedDefault{ColumnType: ct, dv: field.DefaultValue}
	}
	return m.Migrator.MigrateColumn(value, field, ct)
}
```

`needsExprDefault` is true when the base type is `json` or `longtext` and `field.DefaultValue` starts with `'`. `reportedDefault` embeds `gorm.ColumnType` and overrides `DefaultValue() (string, bool)`. `normalizeMySQLDefault` strips a `_utf8mb4` introducer, backslashes, one pair of surrounding parentheses and one pair of single quotes.

If `TestOpenMySQL_RepeatMigrationAltersNothing` still captures ALTERs, print them, find which comparison in `gorm.io/gorm@v1.25.7/migrator/migrator.go` `MigrateColumn` fires, and fix it in `mysqlMigrator` (add each normalisation as a `TestNormalizeMySQLDefault` row). Do not change model tags.

Update `TestOpen_RejectsUnsupportedDrivers` to `{"sqlserver", "oracle"}`.

- [ ] **Step 7: Run, expect PASS**: the MySQL run from Step 5, then the SQLite and PostgreSQL `./internal/database/...` runs.
- [ ] **Step 8: Commit** `feat(database): MySQL driver, column types and migrator`

---

### Task 3: Per-driver index rendering

**Files:**
- Modify: `internal/database/indexes.go`, `internal/database/database.go` (`RunMigrationWithProgress` index loop)
- Create: `internal/database/testdata/indexes_postgres.golden`, `internal/database/testdata/indexes_sqlite.golden`
- Test: `internal/database/indexes_test.go`, `internal/database/open_mysql_test.go`

- [ ] **Step 1: Freeze current output before refactoring.** With the existing code, write `strings.Join(indexesFor("postgres"), "\n")` and the sqlite equivalent to the two golden files (a throwaway test or `go run`), and commit nothing else yet.

- [ ] **Step 2: Write the failing tests**

```go
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
	byName := map[string]string{}
	for _, s := range indexesFor("mysql") {
		require.NotEmpty(t, s.name, s.sql)
		assert.NotContains(t, s.sql, "IF NOT EXISTS")
		assert.NotContains(t, s.sql, " WHERE ")
		assert.NotContains(t, s.sql, "ALTER TABLE")
		assert.NotContains(t, s.sql, "GIN")
		byName[s.name] = s.sql
	}
	assert.Equal(t, "CREATE UNIQUE INDEX idx_team_members_unique ON team_members(team_id, user_id, (CASE WHEN deleted_at IS NULL THEN 1 END))", byName["idx_team_members_unique"])
	assert.Equal(t, "CREATE INDEX idx_agent_transfers_agent_active ON agent_transfers(agent_id, status)", byName["idx_agent_transfers_agent_active"])
	assert.Contains(t, byName["idx_ivr_flows_org_call_start"], "(CASE WHEN is_call_start = true AND is_active = true AND deleted_at IS NULL THEN 1 END)")
}
```

Live (`open_mysql_test.go`):

- `TestOpenMySQL_PartialUniqueIndexes`: add a team member, soft-delete it, add the same `(team_id, user_id)` again → succeeds; add a third active duplicate → error. Same pattern for `user_organizations`.
- `TestOpenMySQL_IndexesIdempotent`: `database.CreateIndexes(db)` twice → no error.

- [ ] **Step 3: Run, expect FAIL**
- [ ] **Step 4: Implement**

```go
type index struct {
	name, table, columns string
	unique               bool
	where                string // partial-index predicate
}

// indexStmt is one statement to run; name and table are empty for ALTERs.
type indexStmt struct{ name, table, sql string }

func (i index) render(driver string) indexStmt {
	kind := "INDEX"
	if i.unique {
		kind = "UNIQUE INDEX"
	}
	if driver == config.DriverMySQL {
		cols := i.columns
		if i.where != "" && i.unique {
			// Rows outside the predicate get NULL here and never conflict,
			// so the unique index keeps its partial meaning.
			cols += ", (CASE WHEN " + i.where + " THEN 1 END)"
		}
		return indexStmt{i.name, i.table, fmt.Sprintf("CREATE %s %s ON %s(%s)", kind, i.name, i.table, cols)}
	}
	sql := fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s(%s)", kind, i.name, i.table, i.columns)
	if i.where != "" {
		sql += " WHERE " + i.where
	}
	return indexStmt{i.name, i.table, sql}
}
```

Move every `CREATE ... INDEX` statement into a package-level `var indexDefs = []index{...}` in the same order, keeping each comment (`RunMigrationWithProgress` already has a local named `indexes`). Update `TestIndexesFor_PostgresOnlyStatements` to read `.sql`. `indexesFor(driver) []indexStmt` = PostgreSQL ALTERs (as today), then each `render(driver)`, then the PostgreSQL GIN statement (as today). `createIndex(db, s indexStmt) error`: on MySQL, skip when `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?` is non-zero; otherwise `db.Exec(s.sql)`. `CreateIndexes` and the `RunMigrationWithProgress` loop both call `createIndex`.

- [ ] **Step 5: Run, expect PASS** on all three drivers for `./internal/database/...`
- [ ] **Step 6: Commit** `feat(database): render indexes per driver; MySQL partial unique via CASE key part`

---

### Task 4: Driver-specific statements

**Files:**
- Modify: `internal/database/database.go` (`BackfillLastInboundAt`), `internal/handlers/webhook_delivery_retention.go`, `internal/handlers/agent_transfers.go:166`, `internal/handlers/contacts.go:143`, `internal/assignment/assigner.go:79`
- Test: `internal/database/database_test.go`, existing handler/assigner tests

- [ ] **Step 1: Write the failing test** (`database_test.go`, runs on every driver via testutil): `TestBackfillLastInboundAt` creates a contact with two incoming messages and one outgoing, plus a soft-deleted contact; after `database.BackfillLastInboundAt(db)` the live contact's `last_inbound_at` equals the newest incoming `created_at` (truncate to microseconds) and the deleted contact stays NULL. A second call changes nothing.

- [ ] **Step 2: Run on SQLite and PostgreSQL, expect PASS** (it pins current behaviour). MySQL is checked in Task 5.

- [ ] **Step 3: Implement**
  - `BackfillLastInboundAt`: when `db.Dialector.Name() == config.DriverMySQL`, run
    ```sql
    UPDATE contacts c
    JOIN (
        SELECT contact_id, MAX(created_at) AS max_created
        FROM messages
        WHERE direction = 'incoming' AND deleted_at IS NULL
        GROUP BY contact_id
    ) sub ON c.id = sub.contact_id
    SET c.last_inbound_at = sub.max_created
    WHERE c.last_inbound_at IS NULL AND c.deleted_at IS NULL
    ```
    otherwise the existing statement (MySQL has no `UPDATE ... FROM`).
  - `pruneWebhookDeliveries`: on MySQL use `DELETE FROM webhook_deliveries WHERE created_at < ? LIMIT ?` (MySQL rejects `LIMIT` inside an `IN` subquery, error 1235).
  - The three `NULLS` orderings use `dialect.Of(db).DescNullsLast(...)` / `AscNullsFirst(...)`; keep the secondary sort keys.

- [ ] **Step 4: Run** `go build ./... && go vet ./...` and the SQLite and PostgreSQL suites for `./internal/database/... ./internal/handlers/... ./internal/assignment/...`
- [ ] **Step 5: Commit** `fix: MySQL variants for backfill, webhook prune and NULLS ordering`

---

### Task 5: MySQL test harness and full suite green

**Files:**
- Modify: `test/testutil/db.go`, `Makefile`, `.github/workflows/test.yml`
- Modify as needed: handlers, services or `dialect` for failures found

- [ ] **Step 1: testutil**
  - `Driver()` accepts `config.DriverMySQL`; the panic message lists all three.
  - `SetupTestDB` mysql branch: skip if `TEST_MYSQL_DSN` is empty; `mysqldrv.ParseDSN`; panic unless `DBName` ends in `_test`; split `Addr` into host/port; `database.Open(&config.DatabaseConfig{Driver: "mysql", Host, Port, User, Password, Name, SSLMode: "disable"}, false)`; `runMigrations`; `cleanupTables` (same `testDBOnce` pattern as postgres).
  - `clearTables` switches on the driver; MySQL pins one connection:
    ```go
    _ = db.Connection(func(conn *gorm.DB) error {
        conn.Exec("SET FOREIGN_KEY_CHECKS = 0")
        for _, table := range tables {
            conn.Exec("TRUNCATE TABLE `" + table + "`")
        }
        conn.Exec("SET FOREIGN_KEY_CHECKS = 1")
        return nil
    })
    ```

- [ ] **Step 2: Makefile**
  ```make
  test-mysql:
  	TEST_DB_DRIVER=mysql TEST_MYSQL_DSN="$${TEST_MYSQL_DSN:-root:whatomate_test@tcp(127.0.0.1:3307)/whatomate_test}" TEST_REDIS_URL=$${TEST_REDIS_URL:-redis://127.0.0.1:6379/15} go test -p 1 -count=1 ./...
  ```

- [ ] **Step 3: CI.** Add a `test-mysql` job copied from `test-sqlite`, with a `mysql:8.4` service (`MYSQL_ROOT_PASSWORD: whatomate_test`, `MYSQL_DATABASE: whatomate_test`, port `3306:3306`, health cmd `mysqladmin ping -h 127.0.0.1 -pwhatomate_test`), env `TEST_DB_DRIVER: mysql`, `TEST_MYSQL_DSN: root:whatomate_test@tcp(localhost:3306)/whatomate_test`. Same `-race -p 1 -timeout 25m` and `timeout-minutes: 35`. Not optional.

- [ ] **Step 4: Run the full MySQL suite** and fix every failure. Rules:
  - Fix at the source with `dialect` helpers or a driver switch; never weaken an assertion or skip a test on MySQL. A test may change only if it asserted PostgreSQL-specific representation (for example, exact JSON text formatting); say so in the commit message.
  - Expected categories: JSON text from MySQL is normalised (key order, `", "` spacing); `JSON_UNQUOTE` of a JSON `null` is the string `"null"`; booleans from raw queries come back as `int64` (use the existing `groupLabel`/`booleanFilterFields` path); reserved words used unquoted in raw SQL; `ONLY_FULL_GROUP_BY` on raw GROUP BY queries.
  - Anything that cannot be fixed without a design decision: stop and report it.

- [ ] **Step 5: Run all three suites green**, then the MySQL suite with `-race`.
- [ ] **Step 6: Commit** `test: MySQL harness, make test-mysql, CI job` (plus one commit per fix group)

---

### Task 6: Docs and config example

**Files:**
- Modify: `docs/src/content/docs/getting-started/configuration.mdx`, `config.example.toml`, the spec (MySQL section: link the deviations above)
- Beads: create the MariaDB follow-up

- [ ] **Step 1:** In the `[database]` docs, add a MySQL section:
  - MySQL 8.0.13+; MariaDB not yet supported.
  - Create the database with `CREATE DATABASE whatomate CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;`
  - `ssl_mode` mapping table (disable/require/verify-ca/verify-full/preferred).
  - Times are stored in UTC.
  - Unique names (canned responses, roles, templates) are case-insensitive on MySQL.
- [ ] **Step 2:** `config.example.toml`: commented MySQL example next to the SQLite one.
- [ ] **Step 3:** `bd create "Multi-db: MariaDB support (generated columns for partial unique indexes, collation)" -t feature -p 3 --parent whatomate-fwb`
- [ ] **Step 4: Commit** `docs: MySQL configuration`

---

### Task 7: Final verification and merge

- [ ] PostgreSQL, SQLite and MySQL suites all green (commands above); MySQL with `-race`.
- [ ] `go vet ./...`, `golangci-lint run` (if installed), `CGO_ENABLED=0 go build ./...`, `goreleaser release --snapshot --clean --skip=publish` builds.
- [ ] Manual end-to-end: a fresh database `whatomate_e2e` on the test container; config copied from `config.local.toml` to `/tmp/whatomate-mysql/config.toml` (0600) with `driver = "mysql"`, port 3307, server port 8092, Redis db 13. Start the server as a background shell; log in; create a contact with tags, filter by tag, search by name, open the dashboard, rename a tag; restart the server and confirm the log shows no `ALTER TABLE`. Stop it and drop `whatomate_e2e`. No Meta-changing actions, no messages to real contacts.
- [ ] Final whole-branch code review; fix findings.
- [ ] Merge `feat/multi-database-stage2` into `feat/multi-database` (local, `--no-ff`); `bd close whatomate-fwb.2`.
