# Multi-database support (PostgreSQL, MySQL, SQLite, SQL Server)

Status: approved approach, spec awaiting review
Branch: `feat/multi-database`
Date: 2026-10-11

## Goal

Anyone installing Whatomate can use the database they already run instead of
being forced onto PostgreSQL. The installer picks one of four databases in the
config; every feature works on all four.

Success means:

- `[database] driver` accepts `postgres` (default), `mysql`, `sqlite`, `sqlserver`.
- The full Go test suite passes against each database.
- Existing PostgreSQL installs upgrade with no schema change and no data change.
- Release binaries stay `CGO_ENABLED=0` and keep cross-compiling to every
  current platform.

## Non-goals

- Moving data between databases (e.g. PostgreSQL to MySQL). Separate tool, later.
- Running one installation on more than one database at a time.
- Removing the Redis requirement. Redis stays required on every database.
- Oracle, CockroachDB, YugabyteDB or other engines.

## Configuration

```toml
[database]
driver = "postgres"   # postgres | mysql | sqlite | sqlserver
host = "localhost"
port = 5432
user = "whatomate"
password = "whatomate"
name = "whatomate"
ssl_mode = "disable"   # postgres: libpq sslmode; mysql: tls; sqlserver: encrypt
path = "./whatomate.db"  # sqlite only
```

- `driver` defaults to `postgres`, so existing config files keep working.
- `port` defaults per driver: 5432, 3306, 1433 (unused for sqlite).
- `path` is only read for sqlite. Defaults to `./whatomate.db`.
- Env overrides work as today, e.g. `WHATOMATE_DATABASE__DRIVER=mysql`.
- Pool settings (`max_open_conns` etc.) apply to all drivers, sqlite included
  (see SQLite below).

Startup validates the driver name and fails with a clear message for unknown
values.

## Drivers

All pure Go, so binaries stay CGO-free:

| Driver | GORM dialector | Underlying driver |
|---|---|---|
| postgres | `gorm.io/driver/postgres` (existing) | pgx |
| mysql | `gorm.io/driver/mysql` | go-sql-driver/mysql |
| sqlite | `github.com/glebarez/sqlite` | modernc.org/sqlite (pure Go) |
| sqlserver | `gorm.io/driver/sqlserver` | microsoft/go-mssqldb |

`gorm.io/driver/sqlite` is not used because it needs CGO (mattn/go-sqlite3).

Minimum versions: PostgreSQL 13, MySQL 8.0.19 / MariaDB 10.6, SQLite 3.38 (bundled
with modernc), SQL Server 2019.

`internal/database/postgres.go` becomes `internal/database/database.go` with
`Open(cfg)` choosing the dialector. `NewPostgres` stays as a thin alias until
callers move.

## Schema portability

### Column types

Models keep their current tags: 111 `type:uuid` fields and 53 `type:jsonb`
fields are not edited. Instead each non-PostgreSQL dialector is wrapped in a
small type that embeds the real dialector and overrides `DataTypeOf`:

| Model tag | postgres | mysql | sqlite | sqlserver |
|---|---|---|---|---|
| `type:uuid` | `uuid` | `char(36)` | `text` | `nvarchar(36)` |
| `type:jsonb` | `jsonb` | `json` | `text` | `nvarchar(max)` |
| `type:text` | `text` | `longtext` | `text` | `nvarchar(max)` |

`text` is mapped too because MySQL cannot index plain `text` columns and SQL
Server's `text` type is deprecated. Other types (`decimal(3,2)`, GORM's
defaults for `string`/`int`/`time`) already work on all four through GORM.

`uuid.UUID` already implements `Value`/`Scan` as its 36-char string form, so
UUIDs are stored as strings on the three new databases. SQL Server's
`uniqueidentifier` is deliberately not used: it scans back in a byte order
that does not round-trip through `uuid.UUID`.

`models.JSONB` / `models.JSONBArray` already marshal to JSON text and scan from
`[]byte` or `string`; their `Scan` gains handling for any driver that returns
a different type, with a test per driver.

### IDs

`default:gen_random_uuid()` is removed from the four models that use it
(`BaseModel`, `UserPasskey`, `UserAvailabilityLog`, `AuditLog`). Each gets a
`BeforeCreate` hook that sets `ID = uuid.New()` when it is nil.
The one raw `INSERT ... SELECT gen_random_uuid()` migration
(`MigrateUserOrganizations`) is rewritten to create rows in Go.

PostgreSQL columns keep their existing database default; GORM's AutoMigrate
does not drop it. To verify in stage 1.

### Indexes

`getIndexes()` becomes per driver:

- Plain and composite indexes: same statement on all four (`CREATE INDEX` with
  per-driver "if not exists" handling: MySQL and SQL Server lack
  `IF NOT EXISTS`, so the code checks the catalog first).
- Partial indexes (`... WHERE status = 'active'`): kept on postgres and sqlite
  (both support them); SQL Server uses filtered indexes (same syntax);
  MySQL gets the index without the `WHERE` clause.
- `DESC` columns: supported on all four.
- The GIN index on `contacts.tags`: postgres only. Other databases filter tags
  without an index (see JSON queries); acceptable for typical contact counts,
  revisited if it becomes slow.

## Query portability

A new package `internal/database/dialect` exposes helpers that return SQL
fragments for the active driver. Handlers call these instead of writing
PostgreSQL syntax. `App` holds the active dialect (`a.Dialect`).

| Helper | postgres | mysql | sqlite | sqlserver |
|---|---|---|---|---|
| `ILike(col)` → `col ILIKE ?` | `ILIKE` | `LIKE` (case-insensitive collation) | `LIKE` (ASCII case-insensitive) | `LIKE` (CI collation) |
| `JSONText(col, key)` | `col->>'key'` | `JSON_UNQUOTE(JSON_EXTRACT(col,'$.key'))` | `json_extract(col,'$.key')` | `JSON_VALUE(col,'$.key')` |
| `JSONArrayContains(col)` → `?` is one value | `col @> ?::jsonb` | `JSON_CONTAINS(col, ?)` | `EXISTS (SELECT 1 FROM json_each(col) WHERE value = ?)` | `EXISTS (SELECT 1 FROM OPENJSON(col) WHERE value = ?)` |
| `MinutesBetween(a, b)` | `EXTRACT(EPOCH FROM (b - a))/60` | `TIMESTAMPDIFF(SECOND, a, b)/60` | `(julianday(b) - julianday(a))*1440` | `DATEDIFF_BIG(SECOND, a, b)/60.0` |
| `DayOfWeek(col)` (0 = Sunday) | `EXTRACT(DOW FROM col)::int` | `DAYOFWEEK(col)-1` | `CAST(strftime('%w', col) AS INTEGER)` | `DATEPART(WEEKDAY, col)-1` (with `SET DATEFIRST 7`) |
| `HourOf(col)` | `EXTRACT(HOUR FROM col)::int` | `HOUR(col)` | `CAST(strftime('%H', col) AS INTEGER)` | `DATEPART(HOUR, col)` |
| `DateTrunc(unit, col)` (day, week, month) | `DATE_TRUNC('unit', col)` | `DATE(col)` / week and month via `DATE_SUB`/`DATE_FORMAT` | `date(col)` / `date(col, '-6 days', 'weekday 1')` / `strftime('%Y-%m-01', col)` | `CAST(col AS date)` / `DATEADD`+`DATEDIFF` |
| `AsText(col)` (JSON column searched with LIKE) | `col::text` | `CAST(col AS CHAR)` | `CAST(col AS TEXT)` | `CAST(col AS nvarchar(max))` |
| `Now()` | `NOW()` | `NOW()` | `CURRENT_TIMESTAMP` | `SYSUTCDATETIME()` |
| `SkipLocked()` | `FOR UPDATE SKIP LOCKED` | same (MySQL 8) | none (writes are serialised) | `WITH (UPDLOCK, READPAST, ROWLOCK)` table hint |

Known call sites (from the audit): `ILIKE` 23 in 15 handlers; JSON operators
in contacts, tags, import/export and campaigns; `EXTRACT`/date functions in
agent analytics and dashboard widgets; `::` casts in 6 files; row locking in
agent transfers; `COALESCE` (portable, unchanged).

Dashboard widgets that let users choose a date grouping are built by string
concatenation today; they move onto the same helpers. Truncated dates come
back as text on SQLite, so result structs scan them into `dialect.DBTime`,
which accepts `time.Time`, `string` and `[]byte`.

The active dialect comes from `db.Dialector.Name()` (`dialect.Of(db)`), so no
new field is needed on `App`.

Renaming or deleting a tag rewrites contacts' tag arrays in Go (batches of
500 in one transaction) instead of PostgreSQL JSON functions, so one code path
serves every database.

Every helper has a table-driven unit test per driver, plus the handler tests
that already exercise the call sites.

## SQLite specifics

- WAL mode, `busy_timeout=10000`, `foreign_keys=ON` set on every connection.
- A normal connection pool sized by `max_open_conns` (default 25). Under WAL,
  readers run concurrently; writers serialise through the IMMEDIATE
  transaction lock (`_txlock=immediate`) and wait up to `busy_timeout`
  instead of failing with `database is locked`. Fine for a single server.
- Documented as single-server only: separate `whatomate worker` processes on
  other machines cannot share a SQLite file.
- Times are stored as text including the zone offset and compared as text, so
  SQLite connections convert every `time.Time` argument to UTC before it
  reaches the driver, and GORM's `NowFunc` returns UTC. Date helpers above
  assume UTC.

## MySQL / MariaDB specifics

Stage 2 departs from this spec in a few places; see "Deviations from the
spec" in `docs/superpowers/plans/2026-10-12-multi-database-stage2-mysql.md`:

- Partial unique indexes keep their `WHERE` meaning through a virtual
  generated column in the key, instead of dropping the `WHERE`.
- `ILike` is `LOWER(col) LIKE LOWER(?)`, independent of the collation.
- `Now()` is `UTC_TIMESTAMP(6)`, not `NOW()`.
- The connection collation is not forced; tables use the database default.
- `jsonb` maps to `longtext` on MariaDB, whose `JSON` is an alias that reads
  back as `longtext`.

## Testing

- `test/testutil` reads `TEST_DB_DRIVER` (default `postgres`) and the matching
  DSN env var (`TEST_DATABASE_URL`, `TEST_MYSQL_DSN`, `TEST_SQLSERVER_DSN`;
  sqlite uses a temp file per test package).
- `make test-db DRIVER=mysql` (etc.) starts the matching container and runs
  the suite. MySQL and SQL Server containers are added to
  `docker-compose.yml` under profiles so they don't start by default.
- CI runs the suite as a matrix over the four drivers.
- The existing PostgreSQL run stays the default and must stay green after
  every stage.
- SQL Server's official image is x86-only; on Apple Silicon it runs under
  emulation (slower, works).

## Rollout

Each stage ends with the full suite green on that database and on PostgreSQL,
and is merged into `dev-00` before the next starts.

1. **Groundwork + SQLite**: driver config, `Open`, dialector wrappers, ID
   generation in Go, per-driver indexes, `dialect` package, all call sites
   moved to helpers, test harness with `TEST_DB_DRIVER`, SQLite passing.
2. **MySQL / MariaDB**: MySQL dialect entries, container profile, CI job.
3. **SQL Server**: SQL Server dialect entries, container profile, CI job.
4. **Docs**: configuration reference, per-database setup notes, limits
   (SQLite single-server, Redis still required, no cross-database data move).

## Risks

- **Hidden PostgreSQL SQL** not caught by the audit (e.g. built at runtime).
  Mitigation: the per-driver test runs; grep for raw `Raw(`/`Exec(` calls.
- **Case sensitivity differs**: `LIKE` in SQLite is case-insensitive only for
  ASCII; MySQL/SQL Server depend on collation. Create the MySQL database with
  `utf8mb4_0900_ai_ci` and SQL Server with a `_CI_` collation; documented.
- **Time zones**: MySQL `DATETIME` and SQL Server `datetime2` have no zone.
  GORM is configured to store UTC on all drivers.
- **Performance on large data** without the PostgreSQL GIN index for tag
  filters. Accepted for now; measured in stage 2.
- **Ongoing cost**: every new query must use the helpers or portable SQL. The
  CI matrix catches regressions.
