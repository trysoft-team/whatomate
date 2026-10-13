# Backup & Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A super-admin Settings page (plus `whatomate backup` / `whatomate restore`) that backs up the whole system into a portable zip and restores it into the running database, even when the backup came from a different database type.

**Architecture:** A new `internal/backup` package does all the work without HTTP: it reads every table through whatomate's GORM models, writes one JSON-lines file per table into a zip with a manifest, and restores by loading rows back as explicit column maps inside one transaction, in the foreign-key order read from the target database. `internal/handlers/backup*.go` exposes it to super admins, runs jobs in the background and switches the app into maintenance mode during a restore. The Vue page lists backups and drives backup, upload, download and restore.

**Tech Stack:** Go 1.25, gorm v1.25.7 (postgres, mysql, glebarez/sqlite), archive/zip, fasthttp/fastglue; Vue 3 + TypeScript, shadcn-vue, vue-i18n, vitest.

**Spec:** `docs/superpowers/specs/2026-10-13-backup-restore-design.md`

## Global Constraints

- `CGO_ENABLED=0`, Go stays at 1.25, gorm stays at v1.25.7. No new Go modules; no mattn/go-sqlite3, no gorm sqlite.
- Must work on postgres, mysql and sqlite (`config.DriverPostgres`, `config.DriverMySQL`, `config.DriverSQLite`).
- Every backup endpoint is super admin only (`a.requireSuperAdmin`). `/api/maintenance` is the only public one.
- Typed confirmation word is exactly `RESTORE`.
- Archive `format` is `1`. Manifest file is `manifest.json`; tables are `db/<table>.jsonl`; files are `files/<area>/<path>` with areas `media` (`storage.local_path`, includes local call recordings) and `audio` (`calling.audio_dir`).
- Not backed up, emptied by restore: `kv_entries`, `scheduled_items`, `campaign_jobs`.
- Key fingerprint: first 12 hex chars of `sha256("whatomate-backup-key:" + encryption_key)`; empty when no key.
- Config: `[backup] dir` (default `backups`, resolved like other paths) and `[backup] max_upload_mb` (default `10240`).
- Never print secrets (DSNs, passwords, keys). Tests never touch the dev database; cross-database tests use only `TEST_BACKUP_DATABASE_URL` / `TEST_BACKUP_MYSQL_DSN`, whose database names must start with `whatomate_test`.
- English UI strings go in `frontend/src/i18n/locales/en.json` only.
- Commit locally after each task; never push.

## Review Focus

1. **A backup made while messages arrive, then restored on MySQL:** MySQL checks foreign keys row by row, so deleting a table that points to itself (`messages.reply_to_message_id`) fails unless those links are cleared first. Expect restore to succeed. Pinned in Task 4 (`clearTables` nulls deferred columns before deleting) and exercised by Task 6's MySQL case.
2. **`false`/`0` in a column whose default is `true`** (e.g. `users.is_active`): inserting through models would silently store `true`. Expect the value to survive. Pinned by Task 4's round-trip test (seed row 1 has every bool `false`) plus an explicit assertion.
3. **Timestamps come back in a different time zone or precision** (pgx returns local time; Postgres/MySQL keep microseconds): expect identical instants and a stable backup file. Pinned in Task 2 (`encodeRow` normalises to UTC) and Task 6 (cross-database seed uses microseconds).
4. **A hostile or damaged upload** (`../` paths, edited rows, a zip from a newer version): expect a clear refusal before anything changes. Pinned in Task 3 (`Inspect`/`Verify` tests) and Task 5 (`safeJoin` test).
5. **The admin who started the restore is not in the backup:** their session dies mid-restore. Expect the dialog to still learn the outcome. Pinned in Task 8 (`/api/maintenance` reports the restore without auth) and Task 11 (dialog polls it).

---

### Task 1: `[backup]` config and spec touch-ups

**Files:**
- Modify: `internal/config/config.go` (Config struct, new `BackupConfig`, `setDefaults`)
- Modify: `internal/config/locate.go:229-249` (`ResolvePaths`)
- Create: `internal/config/backup_test.go`
- Modify: `config.example.toml` (add `[backup]` after `[storage]`)
- Modify: `docs/src/content/docs/getting-started/configuration.mdx` (add a `[backup]` section after the storage section; find it with `rg -n "\[storage\]" docs/src/content/docs`)
- Modify: `docs/superpowers/specs/2026-10-13-backup-restore-design.md`

**Interfaces:**
- Produces: `config.BackupConfig{Dir string; MaxUploadMB int}`, field `Config.Backup`.

- [ ] **Step 1: Write the failing test**

`internal/config/backup_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupDefaultsAndPaths(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte("[app]\nname = \"x\"\n"), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "backups", cfg.Backup.Dir)
	assert.Equal(t, 10240, cfg.Backup.MaxUploadMB)

	base := filepath.Join(string(filepath.Separator), "data", "whatomate")
	cfg.ResolvePaths(base)
	assert.Equal(t, filepath.Join(base, "backups"), cfg.Backup.Dir)

	abs := filepath.Join(string(filepath.Separator), "srv", "bk")
	cfg.Backup.Dir = abs
	cfg.ResolvePaths(base)
	assert.Equal(t, abs, cfg.Backup.Dir)
}

func TestBackupSettingsFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte("[backup]\ndir = \"/x/y\"\nmax_upload_mb = 50\n"), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "/x/y", cfg.Backup.Dir)
	assert.Equal(t, 50, cfg.Backup.MaxUploadMB)
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/config -run TestBackup -count=1`
Expected: FAIL to compile, `cfg.Backup undefined`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add `Backup BackupConfig `koanf:"backup"`` as the last field of `Config`, and the type next to `StorageConfig`:

```go
// BackupConfig controls where backups are kept and how large an uploaded
// backup may be.
type BackupConfig struct {
	Dir         string `koanf:"dir"`
	MaxUploadMB int    `koanf:"max_upload_mb"`
}
```

At the end of `setDefaults`:

```go
	if cfg.Backup.Dir == "" {
		cfg.Backup.Dir = "backups"
	}
	if cfg.Backup.MaxUploadMB == 0 {
		cfg.Backup.MaxUploadMB = 10240
	}
```

In `ResolvePaths` (`locate.go`), after `join(&c.Calling.AudioDir)`:

```go
	join(&c.Backup.Dir)
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/config -count=1`
Expected: PASS.

- [ ] **Step 5: Example config and docs**

`config.example.toml`, after the `[storage]` section:

```toml
[backup]
# Folder for backups made from Settings → Backup & Restore. A relative path
# is resolved like the other paths (against the data folder).
dir = "backups"
# Largest backup file the page accepts for upload, in MB.
max_upload_mb = 10240
```

In `configuration.mdx`, add a `### [backup]` subsection in the same style as the storage one with a two-row table (`dir`, `max_upload_mb`) and one sentence: "In Docker, point `dir` at a mounted volume so backups survive the container."

- [ ] **Step 6: Spec touch-ups**

Edit the spec so it matches this plan:
- Archive format: replace the three `files/...` lines with `files/media/...` (storage.local_path: chat media, flow media and local call recordings) and `files/audio/...` (calling.audio_dir); the manifest `files` example keeps only `media` and `audio`.
- Restore → Run step 6, the order bullet: "The order comes from the foreign keys the target database reports (information_schema on Postgres and MySQL, `pragma_foreign_key_list` on SQLite), topologically sorted; ties keep `GetMigrationModels` order." Add a bullet before the deletes: "Columns that are filled in later (self references, cycle breakers) are set to NULL first, because MySQL checks foreign keys row by row while deleting."
- Restore → Run step 7: "extract each area into a `.restore-<random>` folder inside the area, move the current entries into a `.old-<random>` folder inside the area, move the restored entries in, then remove both. Renames stay inside the folder, so this also works when the folder is a mount point."
- Endpoints table: replace the upload row with three rows: `POST /api/admin/backup-uploads` (start: name, size), `PUT /api/admin/backup-uploads/{id}?offset=N` (one chunk of up to 8 MB), `POST /api/admin/backup-uploads/{id}/finish`; plus `DELETE /api/admin/backup-uploads/{id}`. Replace `GET /api/admin/backups/job` with `GET /api/admin/backup-job`. Replace the upload paragraph with: "Uploads are sent in chunks of at most 8 MB, so the 15 MB request limit stays in place for every other route; the total may not exceed `[backup] max_upload_mb`. Downloads get a 6-hour write timeout through fasthttp's `HeaderReceived` hook."
- `/api/maintenance` row: "Public. While a restore runs (and for an hour after) returns its phase, progress and whether it failed — never the error text."

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/locate.go internal/config/backup_test.go config.example.toml docs/src/content/docs/getting-started/configuration.mdx docs/superpowers/specs/2026-10-13-backup-restore-design.md
git commit -m "feat(config): add [backup] dir and max_upload_mb"
```

---

### Task 2: Tables, foreign keys, load order and the row codec

**Files:**
- Create: `internal/backup/tables.go`, `internal/backup/fk.go`, `internal/backup/order.go`, `internal/backup/codec.go`
- Create: `internal/backup/helpers_test.go`, `internal/backup/order_test.go`, `internal/backup/tables_test.go`, `internal/backup/codec_test.go`

**Interfaces:**
- Produces (package-internal, used by Tasks 3–6):
  - `type table struct { Name string; Schema *schema.Schema; Columns []*schema.Field }`
  - `func backupTables(db *gorm.DB) ([]table, error)`
  - `var storeTableNames = []string{"kv_entries", "scheduled_items", "campaign_jobs"}`
  - `type foreignKey struct { Table, Column, RefTable, RefColumn string }`
  - `func foreignKeys(db *gorm.DB) ([]foreignKey, error)`
  - `type loadPlan struct { Order []string; Deferred map[string][]string }`
  - `func planLoad(names []string, fks []foreignKey) loadPlan`
  - `func encodeRow(ctx context.Context, cols []*schema.Field, rv reflect.Value) ([]byte, error)`
  - `func decodeRow(line []byte, cols map[string]*schema.Field) (map[string]any, error)`
  - test helper `newSQLiteDB(t) *gorm.DB`

- [ ] **Step 1: Test helper**

`internal/backup/helpers_test.go`:

```go
package backup

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"gorm.io/gorm"
)

// newSQLiteDB returns a fresh, migrated SQLite database in a temp folder.
func newSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Open(&config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(t.TempDir(), "w.db")}, false)
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			_ = s.Close()
		}
	})
	return db
}
```

- [ ] **Step 2: Write the failing tests**

`internal/backup/order_test.go`:

```go
package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlanLoad_ParentsFirstAndSelfReference(t *testing.T) {
	fks := []foreignKey{
		{Table: "contacts", Column: "organization_id", RefTable: "organizations", RefColumn: "id"},
		{Table: "messages", Column: "contact_id", RefTable: "contacts", RefColumn: "id"},
		{Table: "messages", Column: "reply_to_message_id", RefTable: "messages", RefColumn: "id"},
		{Table: "elsewhere", Column: "x_id", RefTable: "organizations", RefColumn: "id"},
	}
	p := planLoad([]string{"messages", "contacts", "organizations"}, fks)
	assert.Equal(t, []string{"organizations", "contacts", "messages"}, p.Order)
	assert.Equal(t, map[string][]string{"messages": {"reply_to_message_id"}}, p.Deferred)
}

func TestPlanLoad_BreaksCycles(t *testing.T) {
	fks := []foreignKey{
		{Table: "a", Column: "b_id", RefTable: "b", RefColumn: "id"},
		{Table: "b", Column: "a_id", RefTable: "a", RefColumn: "id"},
	}
	p := planLoad([]string{"a", "b"}, fks)
	assert.Equal(t, []string{"a", "b"}, p.Order)
	assert.Equal(t, map[string][]string{"a": {"b_id"}}, p.Deferred)
}
```

`internal/backup/tables_test.go`:

```go
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
```

`internal/backup/codec_test.go`:

```go
package backup

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type codecInner struct {
	Hours int  `gorm:"column:inner_hours"`
	On    bool `gorm:"column:inner_on"`
}

type codecSample struct {
	ID      uuid.UUID `gorm:"type:uuid;primaryKey"`
	Ref     *uuid.UUID
	Name    string
	Flag    bool
	Count   int64
	Ratio   float64
	At      time.Time
	Maybe   *time.Time
	Deleted gorm.DeletedAt
	Doc     models.JSONB
	List    models.JSONBArray
	Tags    models.StringArray
	Raw     []byte
	Inner   codecInner `gorm:"embedded"`
}

func codecColumns(t *testing.T) []*schema.Field {
	t.Helper()
	s, err := schema.Parse(&codecSample{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	var cols []*schema.Field
	for _, n := range s.DBNames {
		cols = append(cols, s.FieldsByDBName[n])
	}
	return cols
}

func TestCodec_RoundTripsEveryKind(t *testing.T) {
	cols := codecColumns(t)
	ref := uuid.New()
	at := time.Date(2026, 10, 13, 9, 30, 15, 123456789, time.FixedZone("x", 2*3600))
	in := codecSample{
		ID: uuid.New(), Ref: &ref, Name: "héllo ✓ <b>", Flag: false, Count: 1 << 60, Ratio: 1.25,
		At: at, Maybe: nil, Deleted: gorm.DeletedAt{Time: at, Valid: true},
		Doc: models.JSONB{"a": "b", "n": float64(2)}, List: models.JSONBArray{"x", float64(1)},
		Tags: models.StringArray{"p", "q"}, Raw: []byte{0, 1, 255}, Inner: codecInner{Hours: 0, On: true},
	}
	line, err := encodeRow(context.Background(), cols, reflect.ValueOf(in))
	require.NoError(t, err)
	assert.Contains(t, string(line), `"at":"2026-10-13T07:30:15.123456789Z"`, "times are written in UTC")

	byName := map[string]*schema.Field{}
	for _, f := range cols {
		byName[f.DBName] = f
	}
	row, err := decodeRow(line, byName)
	require.NoError(t, err)
	assert.Equal(t, in.ID, row["id"])
	assert.Equal(t, &ref, row["ref"])
	assert.Equal(t, false, row["flag"])
	assert.Equal(t, int64(1<<60), row["count"])
	assert.True(t, at.Equal(row["at"].(time.Time)))
	assert.Nil(t, row["maybe"], "nil pointers decode to an untyped nil")
	assert.True(t, row["deleted"].(gorm.DeletedAt).Valid)
	assert.Equal(t, in.Doc, row["doc"])
	assert.Equal(t, in.List, row["list"])
	assert.Equal(t, in.Tags, row["tags"])
	assert.Equal(t, in.Raw, row["raw"])
	assert.Equal(t, 0, row["inner_hours"])
	assert.Equal(t, true, row["inner_on"])
}

func TestCodec_RejectsUnknownColumn(t *testing.T) {
	_, err := decodeRow([]byte(`{"nope":1}`), map[string]*schema.Field{})
	assert.ErrorContains(t, err, `unknown column "nope"`)
}
```

- [ ] **Step 3: Run them to make sure they fail**

Run: `go test ./internal/backup -count=1`
Expected: FAIL to compile (`backupTables`, `foreignKeys`, `planLoad`, `encodeRow`, `decodeRow` undefined).

- [ ] **Step 4: Implement `tables.go`**

```go
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
```

- [ ] **Step 5: Implement `fk.go`**

```go
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
	defer rows.Close()
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
```

- [ ] **Step 6: Implement `order.go`**

```go
package backup

import "sort"

// loadPlan is the order to insert tables in (parents first) and, per table,
// the columns that are left NULL on insert and filled in once every table is
// loaded: self references and the columns that break a cycle.
type loadPlan struct {
	Order    []string
	Deferred map[string][]string
}

func planLoad(names []string, fks []foreignKey) loadPlan {
	in := make(map[string]bool, len(names))
	for _, n := range names {
		in[n] = true
	}
	p := loadPlan{Deferred: map[string][]string{}}
	deps := map[string]map[string][]string{} // child -> parent -> columns
	for _, fk := range fks {
		if !in[fk.Table] || !in[fk.RefTable] {
			continue
		}
		if fk.Table == fk.RefTable {
			p.Deferred[fk.Table] = appendUnique(p.Deferred[fk.Table], fk.Column)
			continue
		}
		if deps[fk.Table] == nil {
			deps[fk.Table] = map[string][]string{}
		}
		deps[fk.Table][fk.RefTable] = appendUnique(deps[fk.Table][fk.RefTable], fk.Column)
	}

	done := map[string]bool{}
	for len(p.Order) < len(names) {
		progressed := false
		for _, n := range names {
			if done[n] {
				continue
			}
			ready := true
			for parent := range deps[n] {
				if !done[parent] {
					ready = false
					break
				}
			}
			if ready {
				p.Order = append(p.Order, n)
				done[n] = true
				progressed = true
			}
		}
		if progressed {
			continue
		}
		// A cycle: load the first waiting table now and fill in its links
		// to tables not loaded yet afterwards.
		for _, n := range names {
			if done[n] {
				continue
			}
			for parent, cols := range deps[n] {
				if !done[parent] {
					for _, c := range cols {
						p.Deferred[n] = appendUnique(p.Deferred[n], c)
					}
					delete(deps[n], parent)
				}
			}
			break
		}
	}
	for t := range p.Deferred {
		sort.Strings(p.Deferred[t])
	}
	return p
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
```

- [ ] **Step 7: Implement `codec.go`**

```go
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// encodeRow writes one row as a JSON object keyed by column name. Values are
// encoded from the model's Go types, so the result does not depend on the
// database it was read from; times are written in UTC.
func encodeRow(ctx context.Context, cols []*schema.Field, rv reflect.Value) ([]byte, error) {
	obj := make(map[string]json.RawMessage, len(cols))
	for _, f := range cols {
		v, _ := f.ValueOf(ctx, rv)
		b, err := json.Marshal(utc(v))
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", f.DBName, err)
		}
		obj[f.DBName] = b
	}
	return json.Marshal(obj)
}

func utc(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.UTC()
	case *time.Time:
		if t != nil {
			u := t.UTC()
			return &u
		}
	case gorm.DeletedAt:
		if t.Valid {
			t.Time = t.Time.UTC()
		}
		return t
	}
	return v
}

// decodeRow turns a backed-up row into values of each column's Go type, ready
// to insert. A column the model does not have is an error.
func decodeRow(line []byte, cols map[string]*schema.Field) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(raw))
	for name, val := range raw {
		f, ok := cols[name]
		if !ok {
			return nil, fmt.Errorf("unknown column %q", name)
		}
		ptr := reflect.New(f.FieldType)
		if err := json.Unmarshal(val, ptr.Interface()); err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		v := ptr.Elem()
		if v.Kind() == reflect.Pointer && v.IsNil() {
			out[name] = nil
			continue
		}
		out[name] = v.Interface()
	}
	return out, nil
}
```

- [ ] **Step 8: Run the tests**

Run: `go test ./internal/backup -count=1 -v`
Expected: PASS. If `TestForeignKeys_OrderLoadsParentsFirst` reports a parent after a child, print `fks` for that table and fix `planLoad`, not the test.

- [ ] **Step 9: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): table list, foreign-key order and row codec"
```

---

### Task 3: Write, list and check backups

**Files:**
- Create: `internal/backup/manifest.go`, `internal/backup/names.go`, `internal/backup/create.go`, `internal/backup/list.go`, `internal/backup/inspect.go`, `internal/backup/space.go`
- Create: `internal/backup/seed_test.go`, `internal/backup/create_test.go`, `internal/backup/inspect_test.go`
- Modify: `internal/setup/fs.go` (export `FreeDisk`)

**Interfaces:**
- Consumes: Task 2 (`backupTables`, `encodeRow`, `table`).
- Produces (exported, used by Tasks 4–10):
  - `const FormatVersion = 1`; `KindManual = "manual"`, `KindPreRestore = "pre-restore"`, `KindCLI = "cli"`
  - Phases: `PhaseTables = "tables"`, `PhaseFiles = "files"`, `PhaseVerify = "verify"`, `PhaseLoad = "load"`, `PhaseLinks = "links"`, `PhaseSwap = "swap"`
  - `type Manifest struct { Format int; AppVersion, Kind string; CreatedAt time.Time; SourceDriver, KeyFingerprint string; IncludeFiles bool; Tables []TableEntry; Files map[string]FileStats }`
  - `type TableEntry struct { Name string; Rows int64; SHA256 string; Columns []string }`, `type FileStats struct { Count, Bytes int64 }`
  - `type FileArea struct { Name, Dir string }`, `func Areas(media, audio string) []FileArea`
  - `type Options struct { Kind, AppVersion, EncryptionKey string; IncludeFiles bool; Areas []FileArea; Skip []string; Path string }`
  - `type Progress struct { Phase, Table string; Done, Total int64 }`, `type ProgressFunc func(Progress)`
  - `type Info struct { Name string; Size int64; Manifest *Manifest; Error string }`
  - `func Create(ctx context.Context, db *gorm.DB, dir string, opts Options, progress ProgressFunc) (Info, error)`
  - `func List(dir string) ([]Info, error)`, `func ReadManifest(path string) (*Manifest, error)`
  - `func ValidName(name string) bool`, `func UploadName(dir, original string) string`, `func CleanTemp(dir string)`
  - `const UploadPrefix = ".upload-"`
  - `var ErrCorrupt, ErrNewer error`
  - `type Summary struct { Name string; Size int64; Manifest Manifest; TargetDriver string; KeyMatches bool }`
  - `func Inspect(path string, db *gorm.DB, encryptionKey string) (Summary, error)` (fast: manifest + compatibility)
  - `func Verify(path string, progress ProgressFunc) error` (checksums and safe names)
  - `func KeyFingerprint(key string) string`
  - `func EnsureSpace(dir string, need int64) error`
  - test helpers `seedEveryTable(t, db, precision)`, `dumpTables(t, db) map[string][]string`, `rewriteBackup(t, src string, edit func(name string, data []byte) ([]byte, bool)) string`

- [ ] **Step 1: Export the disk helper**

Append to `internal/setup/fs.go`:

```go
// FreeDisk returns the bytes available to this user on the disk holding path,
// or on its nearest existing parent.
func FreeDisk(path string) (uint64, error) { return hostSystem{}.FreeDisk(path) }
```

Run: `go build ./internal/setup` — Expected: OK on darwin; also run `GOOS=linux go build ./internal/setup && GOOS=windows go build ./internal/setup`.

- [ ] **Step 2: Seed and dump helpers**

`internal/backup/seed_test.go`:

```go
package backup

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

var seedTime = time.Date(2026, 10, 13, 9, 30, 15, 123456789, time.UTC)

// seedEveryTable puts three rows into every backed-up table with a value in
// every column, parents first so foreign keys resolve. Row 1 has every bool
// false; row 2 is soft-deleted; messages form a reply chain.
func seedEveryTable(t *testing.T, db *gorm.DB, precision time.Duration) {
	t.Helper()
	tbls, err := backupTables(db)
	require.NoError(t, err)
	fks, err := foreignKeys(db)
	require.NoError(t, err)
	byName := map[string]table{}
	var names []string
	for _, tb := range tbls {
		byName[tb.Name] = tb
		names = append(names, tb.Name)
	}
	refs := map[string]map[string]foreignKey{}
	for _, fk := range fks {
		if refs[fk.Table] == nil {
			refs[fk.Table] = map[string]foreignKey{}
		}
		refs[fk.Table][fk.Column] = fk
	}
	inserted := map[string][]map[string]any{}
	for _, name := range planLoad(names, fks).Order {
		tb := byName[name]
		for i := 0; i < 3; i++ {
			row := map[string]any{}
			for _, f := range tb.Columns {
				if fk, ok := refs[name][f.DBName]; ok {
					parents := inserted[fk.RefTable]
					switch {
					case fk.RefTable == name && i == 0:
						row[f.DBName] = nil
					case fk.RefTable == name:
						row[f.DBName] = inserted[name][i-1][fk.RefColumn]
					case len(parents) == 0:
						row[f.DBName] = nil
					default:
						row[f.DBName] = parents[i%len(parents)][fk.RefColumn]
					}
					continue
				}
				row[f.DBName] = sampleValue(f, i, precision)
			}
			require.NoError(t, db.Table(name).Create(row).Error, "seed %s row %d", name, i)
			inserted[name] = append(inserted[name], row)
		}
	}
}

func sampleValue(f *schema.Field, i int, precision time.Duration) any {
	v := sampleOf(f.FieldType, f.DBName, f.Size, i, precision)
	if v == nil && f.NotNull {
		v = sampleOf(f.FieldType, f.DBName, f.Size, 1, precision)
	}
	return v
}

func sampleOf(typ reflect.Type, name string, size, i int, precision time.Duration) any {
	when := seedTime.Add(time.Duration(i) * time.Hour).Truncate(precision)
	switch typ {
	case reflect.TypeOf(uuid.UUID{}):
		return uuid.New()
	case reflect.TypeOf(time.Time{}):
		return when
	case reflect.TypeOf(gorm.DeletedAt{}):
		if i == 2 {
			return gorm.DeletedAt{Time: when, Valid: true}
		}
		return gorm.DeletedAt{}
	case reflect.TypeOf(models.JSONB{}):
		if i == 0 {
			return nil
		}
		return models.JSONB{"text": "héllo ✓", "n": float64(i), "nested": map[string]any{"ok": true}}
	case reflect.TypeOf(models.JSONBArray{}):
		return models.JSONBArray{"a", float64(i)}
	case reflect.TypeOf(models.StringArray{}):
		return models.StringArray{"x", fmt.Sprint(i)}
	}
	switch typ.Kind() {
	case reflect.Pointer:
		if i == 0 {
			return nil
		}
		v := reflect.New(typ.Elem())
		if inner := sampleOf(typ.Elem(), name, size, i, precision); inner != nil {
			v.Elem().Set(reflect.ValueOf(inner).Convert(typ.Elem()))
		}
		return v.Interface()
	case reflect.String:
		s := []rune(fmt.Sprintf("%d-%s-ü", i, name))
		if size > 0 && len(s) > size {
			s = s[:size]
		}
		return reflect.ValueOf(string(s)).Convert(typ).Interface()
	case reflect.Bool:
		return reflect.ValueOf(i%2 == 0).Convert(typ).Interface()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(int64(i*10 + 7)).Convert(typ).Interface()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.ValueOf(uint64(i*10 + 7)).Convert(typ).Interface()
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(1.25 + float64(i)).Convert(typ).Interface()
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return []byte{0, byte(i), 255}
		}
	}
	return reflect.Zero(typ).Interface()
}

// dumpTables returns every backed-up row of every table, encoded and sorted,
// for comparing two databases.
func dumpTables(t *testing.T, db *gorm.DB) map[string][]string {
	t.Helper()
	tbls, err := backupTables(db)
	require.NoError(t, err)
	out := map[string][]string{}
	for _, tb := range tbls {
		rows, err := db.Unscoped().Model(reflect.New(tb.Schema.ModelType).Interface()).Rows()
		require.NoError(t, err)
		for rows.Next() {
			item := reflect.New(tb.Schema.ModelType)
			require.NoError(t, db.ScanRows(rows, item.Interface()))
			line, err := encodeRow(context.Background(), tb.Columns, item.Elem())
			require.NoError(t, err)
			out[tb.Name] = append(out[tb.Name], string(line))
		}
		require.NoError(t, rows.Close())
		sort.Strings(out[tb.Name])
	}
	return out
}
```

- [ ] **Step 3: Write the failing create/list tests**

`internal/backup/create_test.go`:

```go
package backup

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreate_WritesEveryTableAndManifest(t *testing.T) {
	db := newSQLiteDB(t)
	seedEveryTable(t, db, time.Nanosecond)
	dir := t.TempDir()

	var phases []string
	info, err := Create(context.Background(), db, dir, Options{Kind: KindManual, AppVersion: "v9", EncryptionKey: "k"}, func(p Progress) {
		phases = append(phases, p.Phase)
	})
	require.NoError(t, err)
	assert.Regexp(t, `^whatomate-backup-\d{8}T\d{6}Z\.zip$`, info.Name)
	assert.Contains(t, phases, PhaseTables)

	m, err := ReadManifest(filepath.Join(dir, info.Name))
	require.NoError(t, err)
	assert.Equal(t, FormatVersion, m.Format)
	assert.Equal(t, "sqlite", m.SourceDriver)
	assert.Equal(t, KeyFingerprint("k"), m.KeyFingerprint)
	assert.Len(t, m.KeyFingerprint, 12)
	for _, te := range m.Tables {
		var n int64
		require.NoError(t, db.Table(te.Name).Count(&n).Error)
		assert.Equal(t, n, te.Rows, te.Name)
		assert.NotEmpty(t, te.SHA256)
		assert.NotEmpty(t, te.Columns)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".tmp-"), "temp file left: %s", e.Name())
	}
}

func TestCreate_FilesSkipSymlinksAndSkipDirs(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(media, "flow_media", "org"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(media, "flow_media", "org", "a.jpg"), []byte("jpeg"), 0o640))
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(media, "link")))
	backups := filepath.Join(media, "backups")
	require.NoError(t, os.MkdirAll(backups, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(backups, "old.zip"), []byte("x"), 0o640))

	info, err := Create(context.Background(), db, backups, Options{
		Kind: KindManual, IncludeFiles: true, Areas: []FileArea{{Name: "media", Dir: media}}, Skip: []string{backups},
	}, nil)
	require.NoError(t, err)

	zr, err := zip.OpenReader(filepath.Join(backups, info.Name))
	require.NoError(t, err)
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "files/") {
			names = append(names, f.Name)
		}
	}
	assert.Equal(t, []string{"files/media/flow_media/org/a.jpg"}, names)
	assert.Equal(t, FileStats{Count: 1, Bytes: 4}, info.Manifest.Files["media"])
}

func TestCreate_CancelLeavesNothing(t *testing.T) {
	db := newSQLiteDB(t)
	seedEveryTable(t, db, time.Nanosecond)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Create(ctx, db, dir, Options{Kind: KindManual}, nil)
	require.Error(t, err)
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)
}

func TestList_NewestFirstAndBrokenFiles(t *testing.T) {
	db := newSQLiteDB(t)
	dir := t.TempDir()
	first, err := Create(context.Background(), db, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	second, err := Create(context.Background(), db, dir, Options{Kind: KindPreRestore}, nil)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(second.Name, "whatomate-pre-restore-"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.zip"), []byte("not a zip"), 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".tmp-x.zip"), []byte("x"), 0o640))

	list, err := List(dir)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, second.Name, list[0].Name)
	assert.Equal(t, first.Name, list[1].Name)
	assert.Equal(t, "broken.zip", list[2].Name)
	assert.NotEmpty(t, list[2].Error)
	assert.Nil(t, list[2].Manifest)
}

func TestNames(t *testing.T) {
	assert.True(t, ValidName("whatomate-backup-20261013T181530Z.zip"))
	for _, bad := range []string{"", ".tmp-1.zip", "../x.zip", "a/b.zip", `a\b.zip`, "x.txt", "x..zip"} {
		assert.False(t, ValidName(bad), bad)
	}
	dir := t.TempDir()
	assert.Equal(t, "my-backup.zip", UploadName(dir, "../../my backup.ZIP"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "my-backup.zip"), nil, 0o640))
	assert.Equal(t, "my-backup-2.zip", UploadName(dir, "my backup.zip"))
	assert.Equal(t, "uploaded-backup.zip", UploadName(dir, "...zip"))
}

func TestAreas_DropNestedAndEmpty(t *testing.T) {
	root := t.TempDir()
	assert.Equal(t, []FileArea{{Name: "media", Dir: root}}, Areas(root, filepath.Join(root, "audio")))
	other := t.TempDir()
	assert.Equal(t, []FileArea{{Name: "media", Dir: root}, {Name: "audio", Dir: other}}, Areas(root, other))
	assert.Equal(t, []FileArea{{Name: "audio", Dir: other}}, Areas("", other))
}

func TestCleanTemp(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{".tmp-a.zip", ".upload-b.part", "keep.zip"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), nil, 0o640))
	}
	CleanTemp(dir)
	entries, _ := os.ReadDir(dir)
	require.Len(t, entries, 1)
	assert.Equal(t, "keep.zip", entries[0].Name())
}
```

- [ ] **Step 4: Write the failing inspect tests**

`internal/backup/inspect_test.go`:

```go
package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rewriteBackup copies src to a new file, passing each entry through edit;
// edit returns the new content and false to drop the entry.
func rewriteBackup(t *testing.T, src string, edit func(name string, data []byte) ([]byte, bool)) string {
	t.Helper()
	zr, err := zip.OpenReader(src)
	require.NoError(t, err)
	defer zr.Close()
	dst := filepath.Join(t.TempDir(), "edited.zip")
	f, err := os.Create(dst)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	for _, e := range zr.File {
		rc, err := e.Open()
		require.NoError(t, err)
		var buf bytes.Buffer
		_, err = buf.ReadFrom(rc)
		require.NoError(t, err)
		_ = rc.Close()
		data, keep := edit(e.Name, buf.Bytes())
		if !keep {
			continue
		}
		w, err := zw.Create(e.Name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return dst
}

func editManifest(t *testing.T, change func(m *Manifest)) func(string, []byte) ([]byte, bool) {
	return func(name string, data []byte) ([]byte, bool) {
		if name != manifestName {
			return data, true
		}
		var m Manifest
		require.NoError(t, json.Unmarshal(data, &m))
		change(&m)
		out, err := json.Marshal(m)
		require.NoError(t, err)
		return out, true
	}
}

func makeBackup(t *testing.T) (string, *gorm.DB) {
	t.Helper()
	db := newSQLiteDB(t)
	seedEveryTable(t, db, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), db, dir, Options{Kind: KindManual, AppVersion: "v1", EncryptionKey: "key-a"}, nil)
	require.NoError(t, err)
	return filepath.Join(dir, info.Name), db
}

func TestInspect_Summary(t *testing.T) {
	path, db := makeBackup(t)
	sum, err := Inspect(path, db, "key-a")
	require.NoError(t, err)
	assert.Equal(t, "sqlite", sum.TargetDriver)
	assert.True(t, sum.KeyMatches)
	assert.Equal(t, filepath.Base(path), sum.Name)

	sum, err = Inspect(path, db, "key-b")
	require.NoError(t, err)
	assert.False(t, sum.KeyMatches)
	require.NoError(t, Verify(path, nil))
}

func TestInspect_RefusesNewerBackups(t *testing.T) {
	path, db := makeBackup(t)
	for name, change := range map[string]func(m *Manifest){
		"format": func(m *Manifest) { m.Format = FormatVersion + 1 },
		"table":  func(m *Manifest) { m.Tables = append(m.Tables, TableEntry{Name: "future_things"}) },
		"column": func(m *Manifest) { m.Tables[0].Columns = append(m.Tables[0].Columns, "future_col") },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(rewriteBackup(t, path, editManifest(t, change)), db, "key-a")
			assert.ErrorIs(t, err, ErrNewer)
		})
	}
}

func TestVerify_RefusesDamage(t *testing.T) {
	path, db := makeBackup(t)
	tampered := rewriteBackup(t, path, func(name string, data []byte) ([]byte, bool) {
		if name == "db/organizations.jsonl" {
			return bytes.Replace(data, []byte("0-name"), []byte("0-NAME"), 1), true
		}
		return data, true
	})
	assert.ErrorIs(t, Verify(tampered, nil), ErrCorrupt)

	missing := rewriteBackup(t, path, func(name string, data []byte) ([]byte, bool) { return data, name != "db/users.jsonl" })
	assert.ErrorIs(t, Verify(missing, nil), ErrCorrupt)

	evil := rewriteBackup(t, path, func(_ string, data []byte) ([]byte, bool) { return data, true })
	addEntry(t, evil, "files/media/../../evil.txt", []byte("x"))
	assert.ErrorIs(t, Verify(evil, nil), ErrCorrupt)

	notZip := filepath.Join(t.TempDir(), "n.zip")
	require.NoError(t, os.WriteFile(notZip, []byte("hello"), 0o600))
	_, err := Inspect(notZip, db, "")
	assert.ErrorIs(t, err, ErrCorrupt)
}

// addEntry rewrites the backup at p with one more entry.
func addEntry(t *testing.T, p, name string, data []byte) {
	t.Helper()
	copyPath := rewriteBackup(t, p, func(_ string, d []byte) ([]byte, bool) { return d, true })
	zr, err := zip.OpenReader(copyPath)
	require.NoError(t, err)
	defer zr.Close()
	f, err := os.Create(p)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	for _, e := range zr.File {
		require.NoError(t, zw.Copy(e))
	}
	w, err := zw.Create(name)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}
```

- [ ] **Step 5: Run them to make sure they fail**

Run: `go test ./internal/backup -count=1`
Expected: FAIL to compile (`Create`, `List`, `Inspect`, ... undefined).

- [ ] **Step 6: Implement `manifest.go`**

```go
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// FormatVersion is the archive layout version this build writes and reads.
const FormatVersion = 1

const manifestName = "manifest.json"

// Backup kinds.
const (
	KindManual     = "manual"
	KindPreRestore = "pre-restore"
	KindCLI        = "cli"
)

// Progress phases.
const (
	PhaseTables = "tables"
	PhaseFiles  = "files"
	PhaseVerify = "verify"
	PhaseLoad   = "load"
	PhaseLinks  = "links"
	PhaseSwap   = "swap"
)

// Manifest describes a backup archive.
type Manifest struct {
	Format         int                  `json:"format"`
	AppVersion     string               `json:"app_version"`
	Kind           string               `json:"kind"`
	CreatedAt      time.Time            `json:"created_at"`
	SourceDriver   string               `json:"source_driver"`
	KeyFingerprint string               `json:"key_fingerprint"`
	IncludeFiles   bool                 `json:"include_files"`
	Tables         []TableEntry         `json:"tables"`
	Files          map[string]FileStats `json:"files,omitempty"`
}

// TableEntry is one table in a backup.
type TableEntry struct {
	Name    string   `json:"name"`
	Rows    int64    `json:"rows"`
	SHA256  string   `json:"sha256"`
	Columns []string `json:"columns"`
}

// FileStats counts the files backed up from one folder.
type FileStats struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// Progress reports how far a backup or restore has got.
type Progress struct {
	Phase string `json:"phase"`
	Table string `json:"table,omitempty"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// ProgressFunc receives progress updates; it must not block.
type ProgressFunc func(Progress)

// Info is a backup file in the backup folder.
type Info struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// KeyFingerprint identifies an encryption key without revealing it.
func KeyFingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("whatomate-backup-key:" + key))
	return hex.EncodeToString(sum[:])[:12]
}

func orNop(p ProgressFunc) ProgressFunc {
	if p == nil {
		return func(Progress) {}
	}
	return p
}
```

- [ ] **Step 7: Implement `names.go`**

```go
package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	tempPrefix = ".tmp-"
	// UploadPrefix starts the names of partly uploaded backups.
	UploadPrefix = ".upload-"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}\.zip$`)

// ValidName reports whether name is a backup file name the API accepts: a
// plain file name ending in .zip, without path separators or "..".
func ValidName(name string) bool {
	return nameRE.MatchString(name) && !strings.Contains(name, "..")
}

func newName(dir, kind string, at time.Time) string {
	prefix := "whatomate-backup-"
	if kind == KindPreRestore {
		prefix = "whatomate-pre-restore-"
	}
	return freeName(dir, prefix+at.UTC().Format("20060102T150405Z"))
}

// UploadName turns an uploaded file's name into a free, valid name in dir.
func UploadName(dir, original string) string {
	base := filepath.Base(strings.ReplaceAll(original, `\`, "/"))
	if strings.EqualFold(filepath.Ext(base), ".zip") {
		base = base[:len(base)-4]
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	clean := strings.Trim(b.String(), "-_")
	for strings.Contains(clean, "--") {
		clean = strings.ReplaceAll(clean, "--", "-")
	}
	if clean == "" {
		clean = "uploaded-backup"
	}
	if len(clean) > 150 {
		clean = clean[:150]
	}
	return freeName(dir, clean)
}

func freeName(dir, base string) string {
	name := base + ".zip"
	for i := 2; exists(filepath.Join(dir, name)); i++ {
		name = fmt.Sprintf("%s-%d.zip", base, i)
	}
	return name
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return !errors.Is(err, fs.ErrNotExist)
}

// CleanTemp removes unfinished backups and uploads left by a crash.
func CleanTemp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) || strings.HasPrefix(e.Name(), UploadPrefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
```

- [ ] **Step 8: Implement `space.go`**

```go
package backup

import (
	"fmt"

	"github.com/trysoft/whatomate/internal/setup"
)

// EnsureSpace returns an error when the disk holding dir has less than need
// bytes free (plus a 64 MB margin). An unreadable disk is not an error.
func EnsureSpace(dir string, need int64) error {
	free, err := setup.FreeDisk(dir)
	if err != nil {
		return nil
	}
	const margin = 64 << 20
	if int64(free) < need+margin {
		return fmt.Errorf("not enough disk space in %s: %d MB free, about %d MB needed", dir, free>>20, (need+margin)>>20)
	}
	return nil
}
```

- [ ] **Step 9: Implement `create.go`**

```go
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"
)

// FileArea is a folder of local files a backup can hold.
type FileArea struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// Areas returns the file areas for the media and audio folders, dropping an
// empty one and one that lies inside the other.
func Areas(media, audio string) []FileArea {
	var out []FileArea
	add := func(name, dir string) {
		if dir == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return
		}
		for _, a := range out {
			if within(abs, a.Dir) {
				return
			}
		}
		out = append(out, FileArea{Name: name, Dir: abs})
	}
	add("media", media)
	add("audio", audio)
	return out
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Options control a backup.
type Options struct {
	Kind          string
	AppVersion    string
	EncryptionKey string
	IncludeFiles  bool
	Areas         []FileArea
	// Skip lists folders never copied, such as the backup folder itself.
	Skip []string
	// Path, when set, is the file to write instead of a new name in dir.
	Path string
}

// Create writes a backup of db (and, if asked, the local files) into dir.
// The file only appears under its final name once it is complete.
func Create(ctx context.Context, db *gorm.DB, dir string, opts Options, progress ProgressFunc) (Info, error) {
	progress = orNop(progress)
	tmpDir := dir
	if opts.Path != "" {
		tmpDir = filepath.Dir(opts.Path)
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return Info{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, tempPrefix+"*.zip")
	if err != nil {
		return Info{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	kind := opts.Kind
	if kind == "" {
		kind = KindManual
	}
	m := Manifest{
		Format: FormatVersion, AppVersion: opts.AppVersion, Kind: kind, CreatedAt: time.Now().UTC(),
		SourceDriver: db.Name(), KeyFingerprint: KeyFingerprint(opts.EncryptionKey), IncludeFiles: opts.IncludeFiles,
	}
	zw := zip.NewWriter(tmp)
	tbls, err := backupTables(db)
	if err != nil {
		return Info{}, err
	}
	for _, t := range tbls {
		entry, err := writeTable(ctx, db, zw, t, progress)
		if err != nil {
			return Info{}, fmt.Errorf("table %s: %w", t.Name, err)
		}
		m.Tables = append(m.Tables, entry)
	}
	if opts.IncludeFiles {
		m.Files = map[string]FileStats{}
		for _, a := range opts.Areas {
			st, err := writeArea(ctx, zw, a, opts.Skip, progress)
			if err != nil {
				return Info{}, fmt.Errorf("files in %s: %w", a.Dir, err)
			}
			m.Files[a.Name] = st
		}
	}
	w, err := zw.Create(manifestName)
	if err != nil {
		return Info{}, err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return Info{}, err
	}
	if err := zw.Close(); err != nil {
		return Info{}, err
	}
	if err := tmp.Sync(); err != nil {
		return Info{}, err
	}
	if err := tmp.Close(); err != nil {
		return Info{}, err
	}
	final := opts.Path
	if final == "" {
		final = filepath.Join(dir, newName(dir, kind, m.CreatedAt))
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return Info{}, err
	}
	done = true
	st, err := os.Stat(final)
	if err != nil {
		return Info{}, err
	}
	return Info{Name: filepath.Base(final), Size: st.Size(), Manifest: &m}, nil
}

func writeTable(ctx context.Context, db *gorm.DB, zw *zip.Writer, t table, progress ProgressFunc) (TableEntry, error) {
	entry := TableEntry{Name: t.Name}
	for _, f := range t.Columns {
		entry.Columns = append(entry.Columns, f.DBName)
	}
	var total int64
	if err := db.WithContext(ctx).Table(t.Name).Count(&total).Error; err != nil {
		return entry, err
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "db/" + t.Name + ".jsonl", Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return entry, err
	}
	h := sha256.New()
	out := io.MultiWriter(w, h)
	model := reflect.New(t.Schema.ModelType).Interface()
	rows, err := db.WithContext(ctx).Unscoped().Model(model).Order(strings.Join(t.Schema.PrimaryFieldDBNames, ", ")).Rows()
	if err != nil {
		return entry, err
	}
	defer rows.Close()
	for rows.Next() {
		item := reflect.New(t.Schema.ModelType)
		if err := db.ScanRows(rows, item.Interface()); err != nil {
			return entry, err
		}
		line, err := encodeRow(ctx, t.Columns, item.Elem())
		if err != nil {
			return entry, err
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			return entry, err
		}
		entry.Rows++
		if entry.Rows%500 == 0 {
			progress(Progress{Phase: PhaseTables, Table: t.Name, Done: entry.Rows, Total: total})
		}
	}
	if err := rows.Err(); err != nil {
		return entry, err
	}
	progress(Progress{Phase: PhaseTables, Table: t.Name, Done: entry.Rows, Total: total})
	entry.SHA256 = hex.EncodeToString(h.Sum(nil))
	return entry, nil
}

func writeArea(ctx context.Context, zw *zip.Writer, a FileArea, skip []string, progress ProgressFunc) (FileStats, error) {
	var st FileStats
	root, err := filepath.Abs(a.Dir)
	if err != nil {
		return st, err
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (isWorkDir(d.Name()) || skipped(p, skip)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = "files/" + a.Name + "/" + filepath.ToSlash(rel)
		hdr.Method = zip.Store
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		n, err := io.Copy(w, f)
		_ = f.Close()
		if err != nil {
			return err
		}
		st.Count++
		st.Bytes += n
		progress(Progress{Phase: PhaseFiles, Table: a.Name, Done: st.Count})
		return nil
	})
	return st, err
}

// isWorkDir reports whether name is a restore's staging or old-files folder.
func isWorkDir(name string) bool {
	return strings.HasPrefix(name, ".restore-") || strings.HasPrefix(name, ".old-")
}

func skipped(p string, skip []string) bool {
	for _, s := range skip {
		if abs, err := filepath.Abs(s); err == nil && abs == p {
			return true
		}
	}
	return false
}
```

- [ ] **Step 10: Implement `list.go`**

```go
package backup

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// List returns the backups in dir, newest first. Files that are not readable
// backups are listed with Error set.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		info := Info{Name: e.Name()}
		if fi, err := e.Info(); err == nil {
			info.Size = fi.Size()
		}
		if m, err := ReadManifest(filepath.Join(dir, e.Name())); err != nil {
			info.Error = err.Error()
		} else {
			info.Manifest = m
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Manifest, out[j].Manifest
		switch {
		case a != nil && b != nil:
			return a.CreatedAt.After(b.CreatedAt)
		case a != nil:
			return true
		case b != nil:
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ReadManifest reads the manifest of the backup at path.
func ReadManifest(path string) (*Manifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer zr.Close()
	return readManifest(&zr.Reader)
}

func readManifest(zr *zip.Reader) (*Manifest, error) {
	f := findFile(zr, manifestName)
	if f == nil {
		return nil, fmt.Errorf("%w: no %s", ErrCorrupt, manifestName)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer rc.Close()
	var m Manifest
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
	}
	return &m, nil
}

func findFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}
```

- [ ] **Step 11: Implement `inspect.go`**

```go
package backup

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

var (
	// ErrCorrupt means the file is not a readable, intact backup.
	ErrCorrupt = errors.New("backup file is damaged")
	// ErrNewer means the backup holds data this version does not know.
	ErrNewer = errors.New("backup was made by a newer whatomate")
)

// Summary is what a restore would do, shown before it starts.
type Summary struct {
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	Manifest     Manifest `json:"manifest"`
	TargetDriver string   `json:"target_driver"`
	// KeyMatches is false when the backup's secrets were encrypted with a
	// different encryption key than this server's.
	KeyMatches bool `json:"key_matches"`
}

// Inspect reads the manifest and checks the backup fits this version's
// tables. It does not read the data; Verify does.
func Inspect(p string, db *gorm.DB, encryptionKey string) (Summary, error) {
	st, err := os.Stat(p)
	if err != nil {
		return Summary{}, err
	}
	m, err := ReadManifest(p)
	if err != nil {
		return Summary{}, err
	}
	if m.Format > FormatVersion {
		return Summary{}, fmt.Errorf("%w (%s, format %d); upgrade this server first", ErrNewer, m.AppVersion, m.Format)
	}
	tbls, err := backupTables(db)
	if err != nil {
		return Summary{}, err
	}
	known := map[string]table{}
	for _, t := range tbls {
		known[t.Name] = t
	}
	for _, te := range m.Tables {
		t, ok := known[te.Name]
		if !ok {
			return Summary{}, fmt.Errorf("%w (%s): unknown table %s; upgrade this server first", ErrNewer, m.AppVersion, te.Name)
		}
		cols := columnsByName(t)
		for _, c := range te.Columns {
			if _, ok := cols[c]; !ok {
				return Summary{}, fmt.Errorf("%w (%s): unknown column %s.%s; upgrade this server first", ErrNewer, m.AppVersion, te.Name, c)
			}
		}
	}
	return Summary{
		Name: filepath.Base(p), Size: st.Size(), Manifest: *m, TargetDriver: db.Name(),
		KeyMatches: m.KeyFingerprint == "" || m.KeyFingerprint == KeyFingerprint(encryptionKey),
	}, nil
}

// Verify reads the whole backup: every table's checksum and row count, every
// file's zip checksum, and that no entry points outside its folder.
func Verify(p string, progress ProgressFunc) error {
	progress = orNop(progress)
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer zr.Close()
	m, err := readManifest(&zr.Reader)
	if err != nil {
		return err
	}
	for i, te := range m.Tables {
		f := findFile(&zr.Reader, "db/"+te.Name+".jsonl")
		if f == nil {
			return fmt.Errorf("%w: table %s is missing", ErrCorrupt, te.Name)
		}
		sum, rows, err := hashLines(f)
		if err != nil {
			return fmt.Errorf("%w: table %s: %v", ErrCorrupt, te.Name, err)
		}
		if sum != te.SHA256 || rows != te.Rows {
			return fmt.Errorf("%w: table %s does not match its checksum", ErrCorrupt, te.Name)
		}
		progress(Progress{Phase: PhaseVerify, Table: te.Name, Done: int64(i + 1), Total: int64(len(m.Tables))})
	}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "files/") {
			continue
		}
		if _, _, ok := splitFileEntry(f.Name); !ok {
			return fmt.Errorf("%w: unsafe file name %q", ErrCorrupt, f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, f.Name, err)
		}
		_, err = io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, f.Name, err)
		}
	}
	return nil
}

func hashLines(f *zip.File) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	h := sha256.New()
	br := bufio.NewReader(io.TeeReader(rc, h))
	var rows int64
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			rows++
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), rows, nil
}

// splitFileEntry splits "files/<area>/<rel>" and rejects names that are
// absolute or climb out of the area. Directory entries ("files/media/sub/")
// are allowed and come back with an empty rel.
func splitFileEntry(name string) (area, rel string, ok bool) {
	rest, found := strings.CutPrefix(name, "files/")
	if !found {
		return "", "", false
	}
	area, rel, found = strings.Cut(rest, "/")
	if !found || area == "" || area == "." || area == ".." || strings.Contains(area, `\`) {
		return "", "", false
	}
	if rel == "" || strings.HasSuffix(rel, "/") {
		return area, "", true
	}
	if strings.Contains(rel, `\`) || path.IsAbs(rel) {
		return "", "", false
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", false
	}
	return area, clean, true
}
```

- [ ] **Step 12: Run the tests**

Run: `go test ./internal/backup -count=1`
Expected: PASS. If a `seed` insert fails on a specific column (e.g. a unique index on a short column), adjust `sampleOf` so the value stays unique for that table — never skip the table.

- [ ] **Step 13: Commit**

```bash
git add internal/backup internal/setup/fs.go
git commit -m "feat(backup): write, list, inspect and verify backup archives"
```

---

### Task 4: Restore the database

**Files:**
- Create: `internal/backup/restore.go`, `internal/backup/restore_test.go`

**Interfaces:**
- Consumes: Tasks 2–3.
- Produces:
  - `type RestoreOptions struct { EncryptionKey string; Areas []FileArea; Skip []string }`
  - `var ErrFiles error`
  - `func Restore(ctx context.Context, db *gorm.DB, path string, opts RestoreOptions, progress ProgressFunc) error`
  - internal `clearTables(tx *gorm.DB, p loadPlan) error`, `restoreFiles(...)` (Task 5 fills it in; this task adds a stub that returns nil)

- [ ] **Step 1: Write the failing tests**

`internal/backup/restore_test.go`:

```go
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func TestRestore_RoundTripEveryTable(t *testing.T) {
	src := newSQLiteDB(t)
	seedEveryTable(t, src, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)

	dst := newSQLiteDB(t)
	stale := models.Organization{Name: "stale", Slug: "stale-" + uuid.NewString()[:8]}
	require.NoError(t, dst.Create(&stale).Error)
	require.NoError(t, dst.Exec("INSERT INTO kv_entries (kv_key, kv_value) VALUES ('x', 'y')").Error)

	var phases []string
	require.NoError(t, Restore(context.Background(), dst, filepath.Join(dir, info.Name), RestoreOptions{}, func(p Progress) { phases = append(phases, p.Phase) }))
	assert.Contains(t, phases, PhaseLoad)

	want, got := dumpTables(t, src), dumpTables(t, dst)
	for name := range want {
		assert.Equal(t, want[name], got[name], name)
	}
	var n int64
	require.NoError(t, dst.Unscoped().Model(&models.Organization{}).Where("id = ?", stale.ID).Count(&n).Error)
	assert.Zero(t, n, "existing data is replaced")
	require.NoError(t, dst.Table("kv_entries").Count(&n).Error)
	assert.Zero(t, n, "store tables are emptied")

	var inactive int64
	require.NoError(t, dst.Unscoped().Model(&models.User{}).Where("is_active = ?", false).Count(&inactive).Error)
	assert.Positive(t, inactive, "false survives a column whose default is true")
}

// editTable rewrites one table's rows (and its manifest entry) in a backup.
func editTable(t *testing.T, path, name string, edit func(rows []map[string]json.RawMessage) []map[string]json.RawMessage, dropColumn string) string {
	t.Helper()
	var newData []byte
	out := rewriteBackup(t, path, func(entry string, data []byte) ([]byte, bool) {
		if entry != "db/"+name+".jsonl" {
			return data, true
		}
		var rows []map[string]json.RawMessage
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var r map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(line, &r))
			if dropColumn != "" {
				delete(r, dropColumn)
			}
			rows = append(rows, r)
		}
		var buf bytes.Buffer
		for _, r := range edit(rows) {
			b, _ := json.Marshal(r)
			buf.Write(append(b, '\n'))
		}
		newData = buf.Bytes()
		return newData, true
	})
	sum := sha256.Sum256(newData)
	return rewriteBackup(t, out, editManifest(t, func(m *Manifest) {
		for i := range m.Tables {
			if m.Tables[i].Name != name {
				continue
			}
			m.Tables[i].SHA256 = hex.EncodeToString(sum[:])
			m.Tables[i].Rows = int64(bytes.Count(newData, []byte("\n")))
			if dropColumn != "" {
				var cols []string
				for _, c := range m.Tables[i].Columns {
					if c != dropColumn {
						cols = append(cols, c)
					}
				}
				m.Tables[i].Columns = cols
			}
		}
	}))
}

func TestRestore_FailureChangesNothing(t *testing.T) {
	src := newSQLiteDB(t)
	seedEveryTable(t, src, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	broken := editTable(t, filepath.Join(dir, info.Name), "users", func(rows []map[string]json.RawMessage) []map[string]json.RawMessage {
		rows[0]["organization_id"] = json.RawMessage(`"` + uuid.NewString() + `"`)
		return rows
	}, "")

	dst := newSQLiteDB(t)
	keep := models.Organization{Name: "keep", Slug: "keep-" + uuid.NewString()[:8]}
	require.NoError(t, dst.Create(&keep).Error)
	err = Restore(context.Background(), dst, broken, RestoreOptions{}, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "users"), err.Error())
	var n int64
	require.NoError(t, dst.Model(&models.Organization{}).Where("id = ?", keep.ID).Count(&n).Error)
	assert.Equal(t, int64(1), n, "a failed restore rolls back")
}

func TestRestore_OlderBackupWithoutAColumn(t *testing.T) {
	src := newSQLiteDB(t)
	seedEveryTable(t, src, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	older := editTable(t, filepath.Join(dir, info.Name), "users", func(rows []map[string]json.RawMessage) []map[string]json.RawMessage { return rows }, "is_available")

	dst := newSQLiteDB(t)
	require.NoError(t, Restore(context.Background(), dst, older, RestoreOptions{}, nil))
	var unavailable int64
	require.NoError(t, dst.Unscoped().Model(&models.User{}).Where("is_available = ?", false).Count(&unavailable).Error)
	assert.Zero(t, unavailable, "a column missing from the backup gets its default (true)")
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test ./internal/backup -run TestRestore -count=1`
Expected: FAIL to compile (`Restore` undefined).

- [ ] **Step 3: Implement `restore.go`**

```go
package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/trysoft/whatomate/internal/database"
	"gorm.io/gorm"
)

// ErrFiles means the database was restored but the files were not.
var ErrFiles = errors.New("database restored, but files could not be put back")

// RestoreOptions control a restore.
type RestoreOptions struct {
	EncryptionKey string
	// Areas are the live file folders; files are restored only when the
	// backup holds files and an area of the same name is given.
	Areas []FileArea
	// Skip lists folders inside an area that a restore leaves alone, such as
	// the backup folder.
	Skip []string
}

// Restore replaces every backed-up table in db with the backup's rows, in one
// transaction, then puts back the files. On a database error nothing changes.
func Restore(ctx context.Context, db *gorm.DB, path string, opts RestoreOptions, progress ProgressFunc) error {
	progress = orNop(progress)
	sum, err := Inspect(path, db, opts.EncryptionKey)
	if err != nil {
		return err
	}
	if err := Verify(path, progress); err != nil {
		return err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer zr.Close()

	if err := database.AutoMigrate(db.WithContext(ctx)); err != nil {
		return fmt.Errorf("update tables: %w", err)
	}
	tbls, err := backupTables(db)
	if err != nil {
		return err
	}
	fks, err := foreignKeys(db)
	if err != nil {
		return err
	}
	byName := map[string]table{}
	var names []string
	for _, t := range tbls {
		byName[t.Name] = t
		names = append(names, t.Name)
	}
	for _, s := range storeTableNames {
		if db.Migrator().HasTable(s) {
			names = append(names, s)
		}
	}
	plan := planLoad(names, fks)
	entries := map[string]TableEntry{}
	for _, te := range sum.Manifest.Tables {
		entries[te.Name] = te
	}

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := clearTables(tx, plan); err != nil {
			return err
		}
		var links []pendingLink
		for _, name := range plan.Order {
			te, ok := entries[name]
			if !ok {
				continue
			}
			l, err := loadTable(ctx, tx, &zr.Reader, byName[name], te, plan.Deferred[name], progress)
			if err != nil {
				return err
			}
			links = append(links, l...)
		}
		return applyLinks(tx, links, progress)
	})
	if err != nil {
		return err
	}
	if sum.Manifest.IncludeFiles && len(opts.Areas) > 0 {
		if err := restoreFiles(ctx, &zr.Reader, opts.Areas, opts.Skip, progress); err != nil {
			return fmt.Errorf("%w: %v", ErrFiles, err)
		}
	}
	return nil
}

// clearTables empties every table in the plan, children first. Deferred
// columns are cleared before any delete because MySQL checks foreign keys
// row by row, so a table that points to itself cannot be deleted in one go.
func clearTables(tx *gorm.DB, p loadPlan) error {
	tablesWithLinks := make([]string, 0, len(p.Deferred))
	for t := range p.Deferred {
		tablesWithLinks = append(tablesWithLinks, t)
	}
	sort.Strings(tablesWithLinks)
	for _, t := range tablesWithLinks {
		sets := map[string]any{}
		for _, c := range p.Deferred[t] {
			sets[c] = nil
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Table(t).Updates(sets).Error; err != nil {
			return fmt.Errorf("clear links in %s: %w", t, err)
		}
	}
	for i := len(p.Order) - 1; i >= 0; i-- {
		if err := tx.Exec("DELETE FROM " + tx.Statement.Quote(p.Order[i])).Error; err != nil {
			return fmt.Errorf("empty %s: %w", p.Order[i], err)
		}
	}
	return nil
}

// pendingLink is a deferred column value set after every table is loaded.
type pendingLink struct {
	Table string
	Key   map[string]any
	Set   map[string]any
}

func loadTable(ctx context.Context, tx *gorm.DB, zr *zip.Reader, t table, te TableEntry, deferred []string, progress ProgressFunc) ([]pendingLink, error) {
	f := findFile(zr, "db/"+t.Name+".jsonl")
	if f == nil {
		return nil, fmt.Errorf("%w: table %s is missing", ErrCorrupt, t.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	cols := columnsByName(t)
	batchSize := 30000 / max(1, len(te.Columns))
	batchSize = min(max(batchSize, 1), 500)
	var (
		batch []map[string]any
		links []pendingLink
		done  int64
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := tx.Table(t.Name).Create(batch).Error; err != nil {
			return fmt.Errorf("table %s, rows %d-%d: %w", t.Name, done-int64(len(batch))+1, done, err)
		}
		batch = nil
		progress(Progress{Phase: PhaseLoad, Table: t.Name, Done: done, Total: te.Rows})
		return nil
	}
	br := bufio.NewReader(rc)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			row, err := decodeRow(line, cols)
			if err != nil {
				return nil, fmt.Errorf("table %s, row %d: %w", t.Name, done+1, err)
			}
			if set := takeDeferred(row, deferred); len(set) > 0 {
				key := map[string]any{}
				for _, pk := range t.Schema.PrimaryFieldDBNames {
					key[pk] = row[pk]
				}
				links = append(links, pendingLink{Table: t.Name, Key: key, Set: set})
			}
			batch = append(batch, row)
			done++
			if len(batch) >= batchSize {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	progress(Progress{Phase: PhaseLoad, Table: t.Name, Done: done, Total: te.Rows})
	return links, nil
}

// takeDeferred moves non-NULL deferred columns out of row (leaving NULL) and
// returns them.
func takeDeferred(row map[string]any, deferred []string) map[string]any {
	set := map[string]any{}
	for _, c := range deferred {
		v, ok := row[c]
		if !ok {
			continue
		}
		if v != nil {
			set[c] = v
		}
		row[c] = nil
	}
	return set
}

func applyLinks(tx *gorm.DB, links []pendingLink, progress ProgressFunc) error {
	for i, l := range links {
		if err := tx.Table(l.Table).Where(l.Key).Updates(l.Set).Error; err != nil {
			return fmt.Errorf("table %s, links: %w", l.Table, err)
		}
		if i%200 == 0 || i == len(links)-1 {
			progress(Progress{Phase: PhaseLinks, Done: int64(i + 1), Total: int64(len(links))})
		}
	}
	return nil
}
```

Add a temporary stub in a new file `internal/backup/files.go` (Task 5 replaces it):

```go
package backup

import (
	"archive/zip"
	"context"
)

func restoreFiles(ctx context.Context, zr *zip.Reader, areas []FileArea, skip []string, progress ProgressFunc) error {
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/backup -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): restore every table in one transaction"
```

---

### Task 5: Restore files

**Files:**
- Modify: `internal/backup/files.go` (replace the stub)
- Create: `internal/backup/files_test.go`

**Interfaces:**
- Consumes: `splitFileEntry`, `isWorkDir`, `skipped` (Task 3), `restoreFiles` call site (Task 4).
- Produces: `restoreFiles(ctx, zr, areas, skip, progress) error`.

- [ ] **Step 1: Write the failing tests**

`internal/backup/files_test.go`:

```go
package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(s), 0o640))
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

func TestRestore_PutsFilesBack(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	backups := filepath.Join(media, "backups")
	write(t, filepath.Join(media, "a.txt"), "a1")
	write(t, filepath.Join(media, "sub", "b.txt"), "b1")
	areas := []FileArea{{Name: "media", Dir: media}}
	info, err := Create(context.Background(), db, backups, Options{Kind: KindManual, IncludeFiles: true, Areas: areas, Skip: []string{backups}}, nil)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(media, "a.txt")))
	write(t, filepath.Join(media, "sub", "b.txt"), "b2")
	write(t, filepath.Join(media, "c.txt"), "new")

	require.NoError(t, Restore(context.Background(), db, filepath.Join(backups, info.Name), RestoreOptions{Areas: areas, Skip: []string{backups}}, nil))
	assert.Equal(t, "a1", read(t, filepath.Join(media, "a.txt")))
	assert.Equal(t, "b1", read(t, filepath.Join(media, "sub", "b.txt")))
	assert.NoFileExists(t, filepath.Join(media, "c.txt"))
	assert.FileExists(t, filepath.Join(backups, info.Name), "the backup folder is left alone")
	entries, _ := os.ReadDir(media)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".restore-") || strings.HasPrefix(e.Name(), ".old-"), e.Name())
	}
}

func TestRestore_WithoutFilesLeavesFolders(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	write(t, filepath.Join(media, "a.txt"), "a1")
	dir := t.TempDir()
	info, err := Create(context.Background(), db, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	write(t, filepath.Join(media, "a.txt"), "changed")
	require.NoError(t, Restore(context.Background(), db, filepath.Join(dir, info.Name), RestoreOptions{Areas: []FileArea{{Name: "media", Dir: media}}}, nil))
	assert.Equal(t, "changed", read(t, filepath.Join(media, "a.txt")))
}

func TestSplitFileEntry(t *testing.T) {
	for _, bad := range []string{"files/media/../x", "files/media//etc/passwd", "files/media/a\\..\\b", "files//x", "files/media"} {
		_, rel, ok := splitFileEntry(bad)
		assert.False(t, ok && rel != "", bad)
	}
	area, rel, ok := splitFileEntry("files/audio/x/y.opus")
	assert.True(t, ok)
	assert.Equal(t, "audio", area)
	assert.Equal(t, "x/y.opus", rel)
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test ./internal/backup -run 'TestRestore_PutsFilesBack|TestSplitFileEntry' -count=1`
Expected: `TestRestore_PutsFilesBack` FAILS (`a.txt` missing) because the stub restores nothing.

- [ ] **Step 3: Implement `files.go`**

```go
package backup

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// restoreFiles puts back each area's files. Everything happens inside the
// area folder (staging and old-files folders are its children), so it also
// works when the folder is a mount point.
func restoreFiles(ctx context.Context, zr *zip.Reader, areas []FileArea, skip []string, progress ProgressFunc) error {
	for _, a := range areas {
		if err := restoreArea(ctx, zr, a, skip, progress); err != nil {
			return fmt.Errorf("%s: %w", a.Dir, err)
		}
	}
	return nil
}

func restoreArea(ctx context.Context, zr *zip.Reader, a FileArea, skip []string, progress ProgressFunc) error {
	root, err := filepath.Abs(a.Dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	var n int64
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "files/") {
			continue
		}
		area, rel, ok := splitFileEntry(f.Name)
		if !ok {
			return fmt.Errorf("unsafe file name %q", f.Name)
		}
		if area != a.Name || rel == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := extract(f, filepath.Join(stage, filepath.FromSlash(rel))); err != nil {
			return err
		}
		n++
		progress(Progress{Phase: PhaseSwap, Table: a.Name, Done: n})
	}

	old, err := os.MkdirTemp(root, ".old-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(old)
	current, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var moved []string
	for _, e := range current {
		p := filepath.Join(root, e.Name())
		if p == stage || p == old || isWorkDir(e.Name()) || skipped(p, skip) {
			continue
		}
		if err := os.Rename(p, filepath.Join(old, e.Name())); err != nil {
			putBack(root, old, moved)
			return err
		}
		moved = append(moved, e.Name())
	}
	staged, err := os.ReadDir(stage)
	if err != nil {
		putBack(root, old, moved)
		return err
	}
	for _, e := range staged {
		if err := os.Rename(filepath.Join(stage, e.Name()), filepath.Join(root, e.Name())); err != nil {
			return fmt.Errorf("move %s into place: %w", e.Name(), err)
		}
	}
	return nil
}

// putBack undoes moving names from root into old.
func putBack(root, old string, names []string) {
	for _, name := range names {
		_ = os.Rename(filepath.Join(old, name), filepath.Join(root, name))
	}
}

func extract(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/backup -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backup
git commit -m "feat(backup): restore local files inside each folder"
```

---

### Task 6: Cross-database tests

**Files:**
- Create: `internal/backup/crossdb_test.go`

**Interfaces:**
- Consumes: `seedEveryTable`, `dumpTables`, `planLoad`, `clearTables`, `Create`, `Restore`; `testutil.PostgresConfigFromURL`, `testutil.MySQLConfigFromDSN`.

- [ ] **Step 1: Prepare the test databases (once, by hand)**

Use the Postgres test server on port 5433 and the MySQL test server. Create databases whose names start with `whatomate_test` and that no other test uses — `whatomate_test_backup` on both. Take the user and password from the existing `TEST_DATABASE_URL` / `TEST_MYSQL_DSN` without printing them, e.g.:

```bash
export TEST_BACKUP_DATABASE_URL="$(printf '%s' "$TEST_DATABASE_URL" | sed -E 's#/whatomate_test([^/?]*)#/whatomate_test_backup#')"
export TEST_BACKUP_MYSQL_DSN="$(printf '%s' "$TEST_MYSQL_DSN" | sed -E 's#/whatomate_test([^/?]*)#/whatomate_test_backup#')"
```

and create the databases with the matching client (`createdb`/`CREATE DATABASE whatomate_test_backup`). Never point these at the dev database.

- [ ] **Step 2: Write the test**

`internal/backup/crossdb_test.go`:

```go
package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

// openKind returns an empty, migrated database of the given kind. Postgres
// and MySQL come from TEST_BACKUP_DATABASE_URL / TEST_BACKUP_MYSQL_DSN and
// are skipped when unset; their names must start with whatomate_test.
func openKind(t *testing.T, kind string) *gorm.DB {
	t.Helper()
	var cfg *config.DatabaseConfig
	var err error
	switch kind {
	case config.DriverSQLite:
		return newSQLiteDB(t)
	case config.DriverPostgres:
		raw := os.Getenv("TEST_BACKUP_DATABASE_URL")
		if raw == "" {
			t.Skip("TEST_BACKUP_DATABASE_URL not set")
		}
		cfg, err = testutil.PostgresConfigFromURL(raw)
	case config.DriverMySQL:
		raw := os.Getenv("TEST_BACKUP_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_BACKUP_MYSQL_DSN not set")
		}
		cfg, err = testutil.MySQLConfigFromDSN(raw)
	}
	require.NoError(t, err)
	db, err := database.Open(cfg, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			_ = s.Close()
		}
	})
	require.NoError(t, database.AutoMigrate(db))
	tbls, err := backupTables(db)
	require.NoError(t, err)
	fks, err := foreignKeys(db)
	require.NoError(t, err)
	names := append([]string{}, storeTableNames...)
	for _, tb := range tbls {
		names = append(names, tb.Name)
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return clearTables(tx, planLoad(names, fks)) }))
	return db
}

func TestCrossDatabase(t *testing.T) {
	for _, c := range []struct{ from, to string }{
		{config.DriverSQLite, config.DriverPostgres},
		{config.DriverPostgres, config.DriverSQLite},
		{config.DriverSQLite, config.DriverMySQL},
		{config.DriverMySQL, config.DriverPostgres},
	} {
		t.Run(c.from+"_to_"+c.to, func(t *testing.T) {
			src := openKind(t, c.from)
			seedEveryTable(t, src, time.Microsecond)
			dir := t.TempDir()
			info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
			require.NoError(t, err)
			want := dumpTables(t, src)

			dst := openKind(t, c.to)
			require.NoError(t, Restore(context.Background(), dst, filepath.Join(dir, info.Name), RestoreOptions{}, nil))
			got := dumpTables(t, dst)
			for name := range want {
				assert.Equal(t, want[name], got[name], name)
			}
		})
	}
}
```

- [ ] **Step 3: Run it**

Run: `TEST_BACKUP_DATABASE_URL=… TEST_BACKUP_MYSQL_DSN=… go test ./internal/backup -run TestCrossDatabase -count=1 -v`
Expected: all four subtests PASS (or SKIP for a missing server). A difference here is a real cross-database bug: fix the codec or the restore, not the test. Typical fixes: a column type that the target returns in a different Go shape (normalise it in `encodeRow`), or a value that exceeds a MySQL column size (shorten the seed only if the app itself can never store that value).

- [ ] **Step 4: Commit**

```bash
git add internal/backup/crossdb_test.go
git commit -m "test(backup): restore across sqlite, postgres and mysql"
```

---

### Task 7: Maintenance gate, pausing background work, closing websockets

**Files:**
- Create: `internal/maintenance/gate.go`, `internal/maintenance/gate_test.go`
- Modify: `internal/handlers/app.go` (App gets `Maintenance *maintenance.Gate` and `Version string`)
- Modify: `internal/handlers/sla_processor.go:43-44`, `internal/handlers/chat_delay_processor.go:45-48`, `internal/handlers/webhook_delivery_retention.go:57-58`
- Modify: `internal/worker/worker.go` (field `Gate`, wait at the top of `HandleRecipientJob`)
- Modify: `internal/websocket/hub.go` (add `CloseAll`), Create: `internal/websocket/hub_closeall_test.go`

**Interfaces:**
- Produces: `maintenance.Gate` with `Active() bool`, `Set(bool)`, `Wait(ctx) error` (all nil-safe); `(*websocket.Hub).CloseAll()`; `App.Maintenance`, `App.Version`; `Worker.Gate`.

- [ ] **Step 1: Write the failing tests**

`internal/maintenance/gate_test.go`:

```go
package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGate(t *testing.T) {
	var nilGate *Gate
	assert.False(t, nilGate.Active())
	nilGate.Set(true)
	assert.NoError(t, nilGate.Wait(context.Background()))

	g := &Gate{}
	g.Set(true)
	assert.True(t, g.Active())
	go func() { time.Sleep(50 * time.Millisecond); g.Set(false) }()
	assert.NoError(t, g.Wait(context.Background()))

	g.Set(true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, g.Wait(ctx), context.DeadlineExceeded)
}
```

`internal/websocket/hub_closeall_test.go`:

```go
package websocket

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/zerodha/logf"
)

func TestHub_CloseAll(t *testing.T) {
	h := NewHub(logf.New(logf.Opts{}))
	c := &Client{hub: h, send: make(chan []byte, 1), userID: uuid.New(), organizationID: uuid.New()}
	h.registerClient(c)
	assert.Equal(t, 1, h.GetClientCount())
	h.CloseAll()
	assert.Equal(t, 0, h.GetClientCount())
	_, open := <-c.send
	assert.False(t, open)
	h.unregisterClient(c) // a late unregister must not panic
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test ./internal/maintenance ./internal/websocket -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/maintenance/gate.go`:

```go
// Package maintenance holds the switch that pauses the app during a restore.
package maintenance

import (
	"context"
	"sync/atomic"
	"time"
)

// Gate is on while a restore replaces the data. A nil Gate is always off.
type Gate struct{ on atomic.Bool }

// Active reports whether maintenance mode is on.
func (g *Gate) Active() bool { return g != nil && g.on.Load() }

// Set turns maintenance mode on or off.
func (g *Gate) Set(on bool) {
	if g != nil {
		g.on.Store(on)
	}
}

// Wait blocks while maintenance mode is on, or until ctx is done.
func (g *Gate) Wait(ctx context.Context) error {
	for g.Active() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil
}
```

`internal/websocket/hub.go`, after `unregisterClient`:

```go
// CloseAll disconnects every client; their write pumps close the sockets.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for orgID, users := range h.clients {
		for _, clients := range users {
			for c := range clients {
				close(c.send)
			}
		}
		delete(h.clients, orgID)
	}
	h.log.Info("WebSocket clients disconnected for maintenance")
}
```

`internal/handlers/app.go` — add to `App`:

```go
	// Maintenance is on while a restore runs; background work skips its ticks.
	Maintenance *maintenance.Gate
	// Version is the running build's version, written into backups.
	Version string
```

In each of the three processors' `case <-ticker.C:` add as the first statement:

```go
			if p.app.Maintenance.Active() {
				continue
			}
```

`internal/worker/worker.go` — add a field to `Worker`:

```go
	// Gate pauses job handling while a restore runs.
	Gate *maintenance.Gate
```

and as the first lines of `HandleRecipientJob`:

```go
	if err := w.Gate.Wait(ctx); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the tests and the packages that changed**

Run: `go test ./internal/maintenance ./internal/websocket ./internal/worker -count=1 && TEST_DB_DRIVER=sqlite go test ./internal/handlers -count=1 -run 'SLA|Delay|Prune' `
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/maintenance internal/websocket internal/handlers/app.go internal/handlers/sla_processor.go internal/handlers/chat_delay_processor.go internal/handlers/webhook_delivery_retention.go internal/worker/worker.go
git commit -m "feat: maintenance gate that pauses background work and websockets"
```

---

### Task 8: Backup API and maintenance mode

**Files:**
- Create: `internal/handlers/backup.go`, `internal/handlers/backup_upload.go`
- Create: `internal/handlers/backup_test.go` (package `handlers_test`), `internal/handlers/backup_internal_test.go` (package `handlers`)
- Modify: `internal/handlers/app.go` (unexported fields `backupJob backupJobs`, `uploads uploadStore`)

**Interfaces:**
- Consumes: `backup.*` (Tasks 3–5), `maintenance.Gate` (Task 7), `a.requireSuperAdmin`, `a.decodeRequest`, `a.getMediaStoragePath`, cache prefix constants in `cache.go`, `refreshTokenKey` prefix `refresh:`.
- Produces (handlers, routed in Task 9):
  - `ListBackups`, `CreateBackup`, `GetBackupJob`, `DownloadBackup`, `InspectBackup`, `RestoreBackup`, `DeleteBackup`
  - `StartBackupUpload`, `PutBackupUploadChunk`, `FinishBackupUpload`, `CancelBackupUpload`
  - `GetMaintenance` (public), `MaintenanceGuard(r *fastglue.Request) *fastglue.Request`
  - `func IsBackupDownload(method, uri string) bool`
  - `type BackupJob struct {...}`, `type MaintenanceStatus struct {...}`

- [ ] **Step 1: Write the failing internal test**

`internal/handlers/backup_internal_test.go`:

```go
package handlers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBackupJobs_OneAtATime(t *testing.T) {
	var j backupJobs
	assert.True(t, j.start("backup", ""))
	assert.False(t, j.start("restore", "x.zip"))
	j.finish(errors.New("boom"))
	got := j.get()
	assert.False(t, got.Running)
	assert.Equal(t, "boom", got.Error)
	assert.True(t, j.start("restore", "x.zip"))
}

func TestIsBackupDownload(t *testing.T) {
	assert.True(t, IsBackupDownload("GET", "/api/admin/backups/a.zip/download"))
	assert.True(t, IsBackupDownload("GET", "/base/api/admin/backups/a.zip/download?x=1"))
	assert.False(t, IsBackupDownload("POST", "/api/admin/backups/a.zip/download"))
	assert.False(t, IsBackupDownload("GET", "/api/admin/backups"))
}
```

- [ ] **Step 2: Write the failing API tests**

`internal/handlers/backup_test.go`:

```go
package handlers_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/maintenance"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// backupApp is a test app on its own SQLite database, so restores never touch
// the shared test database.
func backupApp(t *testing.T) (*handlers.App, uuid.UUID, uuid.UUID) {
	t.Helper()
	app := newTestApp(t)
	root := t.TempDir()
	db, err := database.Open(&config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(root, "w.db")}, false)
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			_ = s.Close()
		}
	})
	state := store.NewSQLKV(db)
	t.Cleanup(state.Close)
	app.DB, app.State = db, state
	app.Maintenance = &maintenance.Gate{}
	app.Version = "test"
	app.Config.Backup = config.BackupConfig{Dir: filepath.Join(root, "backups"), MaxUploadMB: 1}
	app.Config.Storage.LocalPath = filepath.Join(root, "uploads")
	app.Config.Calling.AudioDir = filepath.Join(root, "audio")
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithEmail(testutil.UniqueEmail("bk-admin")), testutil.WithSuperAdmin())
	return app, org.ID, admin.ID
}

func call(t *testing.T, h fastglue.FastRequestHandler, req *fastglue.Request, orgID, userID uuid.UUID) int {
	t.Helper()
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req)
}

func waitJob(t *testing.T, app *handlers.App, orgID, adminID uuid.UUID) handlers.BackupJob {
	t.Helper()
	var job handlers.BackupJob
	require.Eventually(t, func() bool {
		req := testutil.NewGETRequest(t)
		call(t, app.GetBackupJob, req, orgID, adminID)
		testutil.ParseEnvelopeResponse(t, req, &job)
		return !job.Running
	}, 30*time.Second, 50*time.Millisecond)
	return job
}

func TestBackupAPI_SuperAdminOnly(t *testing.T) {
	app, orgID, _ := backupApp(t)
	user := testutil.CreateTestUser(t, app.DB, orgID, testutil.WithEmail(testutil.UniqueEmail("bk-user")))
	for name, h := range map[string]fastglue.FastRequestHandler{
		"list": app.ListBackups, "create": app.CreateBackup, "job": app.GetBackupJob, "download": app.DownloadBackup,
		"inspect": app.InspectBackup, "restore": app.RestoreBackup, "delete": app.DeleteBackup, "upload": app.StartBackupUpload,
	} {
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetPathParam(req, "name", "x.zip")
		assert.Equal(t, fasthttp.StatusForbidden, call(t, h, req, orgID, user.ID), name)
	}
}

func TestBackupAPI_CreateListDownloadDelete(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	require.Equal(t, fasthttp.StatusOK, call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID))
	job := waitJob(t, app, orgID, adminID)
	require.Empty(t, job.Error)
	require.NotEmpty(t, job.Name)

	list := testutil.NewGETRequest(t)
	call(t, app.ListBackups, list, orgID, adminID)
	var resp struct {
		Dir     string        `json:"dir"`
		Backups []backup.Info `json:"backups"`
	}
	testutil.ParseEnvelopeResponse(t, list, &resp)
	require.Len(t, resp.Backups, 1)
	assert.Equal(t, job.Name, resp.Backups[0].Name)

	dl := testutil.NewGETRequest(t)
	testutil.SetPathParam(dl, "name", job.Name)
	assert.Equal(t, fasthttp.StatusOK, call(t, app.DownloadBackup, dl, orgID, adminID))
	assert.Contains(t, string(dl.RequestCtx.Response.Header.Peek("Content-Disposition")), job.Name)
	assert.True(t, bytes.HasPrefix(dl.RequestCtx.Response.Body(), []byte("PK")))

	for _, bad := range []string{"../w.db", "x.txt", "missing.zip"} {
		req := testutil.NewGETRequest(t)
		testutil.SetPathParam(req, "name", bad)
		assert.Equal(t, fasthttp.StatusNotFound, call(t, app.DownloadBackup, req, orgID, adminID), bad)
	}

	del := testutil.NewRequest(t)
	testutil.SetPathParam(del, "name", job.Name)
	assert.Equal(t, fasthttp.StatusOK, call(t, app.DeleteBackup, del, orgID, adminID))
	assert.NoFileExists(t, filepath.Join(app.Config.Backup.Dir, job.Name))
}

func TestBackupAPI_Restore(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": true}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)
	later := models.Organization{Name: "later", Slug: "later-" + uuid.NewString()[:8]}
	require.NoError(t, app.DB.Create(&later).Error)

	noConfirm := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetPathParam(noConfirm, "name", made.Name)
	assert.Equal(t, fasthttp.StatusBadRequest, call(t, app.RestoreBackup, noConfirm, orgID, adminID))

	insp := testutil.NewGETRequest(t)
	testutil.SetPathParam(insp, "name", made.Name)
	assert.Equal(t, fasthttp.StatusOK, call(t, app.InspectBackup, insp, orgID, adminID))

	req := testutil.NewJSONRequest(t, map[string]any{"confirm": "RESTORE"})
	testutil.SetPathParam(req, "name", made.Name)
	require.Equal(t, fasthttp.StatusOK, call(t, app.RestoreBackup, req, orgID, adminID))
	job := waitJob(t, app, orgID, adminID)
	require.Empty(t, job.Error)
	assert.NotEmpty(t, job.SafetyName)
	assert.False(t, app.Maintenance.Active())

	var n int64
	require.NoError(t, app.DB.Model(&models.Organization{}).Where("id = ?", later.ID).Count(&n).Error)
	assert.Zero(t, n)
	m, err := backup.ReadManifest(filepath.Join(app.Config.Backup.Dir, job.SafetyName))
	require.NoError(t, err)
	assert.Equal(t, backup.KindPreRestore, m.Kind)

	pub := testutil.NewGETRequest(t)
	require.NoError(t, app.GetMaintenance(pub))
	var st handlers.MaintenanceStatus
	testutil.ParseEnvelopeResponse(t, pub, &st)
	require.NotNil(t, st.Restore)
	assert.True(t, st.Restore.Finished)
	assert.False(t, st.Restore.Failed)
}

func TestMaintenanceGuard(t *testing.T) {
	app, _, _ := backupApp(t)
	pass := func(path string) bool {
		req := testutil.NewGETRequest(t)
		req.RequestCtx.Request.SetRequestURI(path)
		return app.MaintenanceGuard(req) != nil
	}
	assert.True(t, pass("/api/contacts"))
	app.Maintenance.Set(true)
	assert.False(t, pass("/api/contacts"))
	assert.False(t, pass("/ws"))
	assert.False(t, pass("/public/chatbot-media/x"))
	assert.True(t, pass("/api/maintenance"))
	assert.True(t, pass("/health"))
	assert.True(t, pass("/settings/backup"))
}

func TestBackupAPI_ChunkedUpload(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)
	data, err := os.ReadFile(filepath.Join(app.Config.Backup.Dir, made.Name))
	require.NoError(t, err)

	start := testutil.NewJSONRequest(t, map[string]any{"name": "From other server.zip", "size": len(data)})
	require.Equal(t, fasthttp.StatusOK, call(t, app.StartBackupUpload, start, orgID, adminID))
	var up struct {
		ID string `json:"id"`
	}
	testutil.ParseEnvelopeResponse(t, start, &up)

	put := func(offset int, chunk []byte) int {
		req := testutil.NewRequest(t)
		req.RequestCtx.Request.Header.SetMethod("PUT")
		req.RequestCtx.Request.SetBody(chunk)
		testutil.SetPathParam(req, "id", up.ID)
		testutil.SetQueryParam(req, "offset", offset)
		return call(t, app.PutBackupUploadChunk, req, orgID, adminID)
	}
	half := len(data) / 2
	assert.Equal(t, fasthttp.StatusOK, put(0, data[:half]))
	assert.Equal(t, fasthttp.StatusConflict, put(0, data[:half]), "wrong offset")
	assert.Equal(t, fasthttp.StatusOK, put(half, data[half:]))

	fin := testutil.NewJSONRequest(t, nil)
	testutil.SetPathParam(fin, "id", up.ID)
	require.Equal(t, fasthttp.StatusOK, call(t, app.FinishBackupUpload, fin, orgID, adminID))
	var info backup.Info
	testutil.ParseEnvelopeResponse(t, fin, &info)
	assert.Equal(t, "From-other-server.zip", info.Name)

	// too large for max_upload_mb = 1
	big := testutil.NewJSONRequest(t, map[string]any{"name": "x.zip", "size": 2 << 20})
	assert.Equal(t, fasthttp.StatusRequestEntityTooLarge, call(t, app.StartBackupUpload, big, orgID, adminID))

	// garbage is refused and removed
	g := testutil.NewJSONRequest(t, map[string]any{"name": "junk.zip", "size": 5})
	call(t, app.StartBackupUpload, g, orgID, adminID)
	testutil.ParseEnvelopeResponse(t, g, &up)
	assert.Equal(t, fasthttp.StatusOK, put(0, []byte("hello")))
	fin2 := testutil.NewJSONRequest(t, nil)
	testutil.SetPathParam(fin2, "id", up.ID)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, call(t, app.FinishBackupUpload, fin2, orgID, adminID))
	assert.NoFileExists(t, filepath.Join(app.Config.Backup.Dir, "junk.zip"))
}
```

(If `ParseEnvelopeResponse` expects a different envelope shape, read it in `test/testutil/http.go` and adapt the call, not the handler.)

- [ ] **Step 3: Run them to make sure they fail**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/handlers -run 'Backup|Maintenance' -count=1`
Expected: FAIL to compile.

- [ ] **Step 4: Implement `backup.go`**

```go
package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// BackupJob is the running or most recent backup or restore.
type BackupJob struct {
	Kind       string     `json:"kind"` // "backup" or "restore"
	Running    bool       `json:"running"`
	Phase      string     `json:"phase"`
	Table      string     `json:"table,omitempty"`
	Done       int64      `json:"done"`
	Total      int64      `json:"total"`
	Name       string     `json:"name,omitempty"`
	SafetyName string     `json:"safety_name,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// backupJobs allows one backup or restore at a time.
type backupJobs struct {
	mu  sync.Mutex
	job BackupJob
}

func (j *backupJobs) start(kind, name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job.Running {
		return false
	}
	j.job = BackupJob{Kind: kind, Running: true, Name: name, StartedAt: time.Now().UTC()}
	return true
}

func (j *backupJobs) progress(p backup.Progress) {
	j.mu.Lock()
	j.job.Phase, j.job.Table, j.job.Done, j.job.Total = p.Phase, p.Table, p.Done, p.Total
	j.mu.Unlock()
}

func (j *backupJobs) update(fn func(*BackupJob)) {
	j.mu.Lock()
	fn(&j.job)
	j.mu.Unlock()
}

func (j *backupJobs) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now().UTC()
	j.job.Running, j.job.FinishedAt = false, &now
	if err != nil {
		j.job.Error = err.Error()
		j.job.Phase = "failed"
	} else {
		j.job.Phase = "done"
	}
}

func (j *backupJobs) get() BackupJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.job
}

func (a *App) backupAreas() []backup.FileArea {
	return backup.Areas(a.getMediaStoragePath(), a.Config.Calling.AudioDir)
}

func (a *App) backupOptions(kind string, includeFiles bool) backup.Options {
	return backup.Options{
		Kind: kind, AppVersion: a.Version, EncryptionKey: a.Config.App.EncryptionKey,
		IncludeFiles: includeFiles, Areas: a.backupAreas(), Skip: []string{a.Config.Backup.Dir},
	}
}

// ListBackups returns the saved backups, the backup folder and the current job.
func (a *App) ListBackups(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	list, err := backup.List(a.Config.Backup.Dir)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, fmt.Sprintf("Could not read %s: %v", a.Config.Backup.Dir, err), nil, "")
	}
	if list == nil {
		list = []backup.Info{}
	}
	return r.SendEnvelope(map[string]any{"dir": a.Config.Backup.Dir, "backups": list, "job": a.backupJob.get()})
}

// CreateBackup starts a backup in the background.
func (a *App) CreateBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	var req struct {
		IncludeFiles *bool `json:"include_files"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	includeFiles := req.IncludeFiles == nil || *req.IncludeFiles
	if !a.backupJob.start("backup", "") {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "A backup or restore is already running", nil, "")
	}
	go func() {
		info, err := backup.Create(context.Background(), a.DB, a.Config.Backup.Dir, a.backupOptions(backup.KindManual, includeFiles), a.backupJob.progress)
		if err != nil {
			a.Log.Error("Backup failed", "error", err)
		} else {
			a.Log.Info("Backup written", "name", info.Name, "size", info.Size)
			a.backupJob.update(func(j *BackupJob) { j.Name = info.Name })
		}
		a.backupJob.finish(err)
	}()
	return r.SendEnvelope(a.backupJob.get())
}

// GetBackupJob returns the running or most recent backup or restore.
func (a *App) GetBackupJob(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	return r.SendEnvelope(a.backupJob.get())
}

// backupPath resolves the {name} path parameter to a file in the backup
// folder, or answers 404.
func (a *App) backupPath(r *fastglue.Request) (string, bool) {
	name, _ := r.RequestCtx.UserValue("name").(string)
	if !backup.ValidName(name) {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Backup not found", nil, "")
		return "", false
	}
	p := filepath.Join(a.Config.Backup.Dir, name)
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Backup not found", nil, "")
		return "", false
	}
	return p, true
}

// IsBackupDownload reports whether a request downloads a backup; those get a
// long write timeout.
func IsBackupDownload(method, uri string) bool {
	path, _, _ := strings.Cut(uri, "?")
	return method == fasthttp.MethodGet && strings.Contains(path, "/api/admin/backups/") && strings.HasSuffix(path, "/download")
}

// DownloadBackup streams a backup file.
func (a *App) DownloadBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	fasthttp.ServeFileUncompressed(r.RequestCtx, p)
	r.RequestCtx.Response.Header.Set("Content-Type", "application/zip")
	r.RequestCtx.Response.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(p)))
	return nil
}

// InspectBackup describes what restoring a backup would do.
func (a *App) InspectBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	sum, err := backup.Inspect(p, a.DB, a.Config.App.EncryptionKey)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, err.Error(), nil, "")
	}
	return r.SendEnvelope(sum)
}

// RestoreBackup starts a restore after the typed confirmation.
func (a *App) RestoreBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Confirm != "RESTORE" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Type RESTORE to confirm", nil, "")
	}
	sum, err := backup.Inspect(p, a.DB, a.Config.App.EncryptionKey)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, err.Error(), nil, "")
	}
	if sum.Manifest.IncludeFiles {
		var need int64
		for _, st := range sum.Manifest.Files {
			need += st.Bytes
		}
		if err := backup.EnsureSpace(a.Config.Backup.Dir, 2*need); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInsufficientStorage, err.Error(), nil, "")
		}
	}
	if !a.backupJob.start("restore", sum.Name) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "A backup or restore is already running", nil, "")
	}
	go a.runRestore(p, sum)
	return r.SendEnvelope(a.backupJob.get())
}

func (a *App) runRestore(path string, sum backup.Summary) {
	ctx := context.Background()
	safety, err := backup.Create(ctx, a.DB, a.Config.Backup.Dir, a.backupOptions(backup.KindPreRestore, sum.Manifest.IncludeFiles), func(p backup.Progress) {
		p.Phase = "safety_backup"
		a.backupJob.progress(p)
	})
	if err != nil {
		a.Log.Error("Safety backup before restore failed", "error", err)
		a.backupJob.finish(fmt.Errorf("the safety backup failed, so nothing was changed: %w", err))
		return
	}
	a.backupJob.update(func(j *BackupJob) { j.SafetyName = safety.Name })

	a.Maintenance.Set(true)
	if a.WSHub != nil {
		a.WSHub.CloseAll()
	}
	err = backup.Restore(ctx, a.DB, path, backup.RestoreOptions{
		EncryptionKey: a.Config.App.EncryptionKey, Areas: a.backupAreas(), Skip: []string{a.Config.Backup.Dir},
	}, a.backupJob.progress)
	if err == nil || errors.Is(err, backup.ErrFiles) {
		a.clearCachesAfterRestore()
	}
	a.Maintenance.Set(false)
	if errors.Is(err, backup.ErrFiles) {
		err = fmt.Errorf("%w. Restore %s to undo", err, safety.Name)
	}
	if err != nil {
		a.Log.Error("Restore failed", "backup", sum.Name, "error", err)
	} else {
		a.Log.Info("Restore finished", "backup", sum.Name, "safety_backup", safety.Name)
	}
	a.backupJob.finish(err)
}

// restoreCachePrefixes are every cache key prefix that can hold data from
// the database; a restore clears them all.
var restoreCachePrefixes = []string{
	settingsCachePrefix, flowsCachePrefix, keywordRulesCachePrefix, whatsappAccountCachePrefix,
	webhooksCachePrefix, slaSettingsCacheKey, aiContextsCachePrefix, userPermissionsCachePrefix,
	rolePermissionsCachePrefix, tagsCachePrefix, metaAnalyticsCachePrefix, metaWABACurrencyCachePrefix,
	"team:assignment:", "ivr_flow:", "org:calling_settings:",
}

func (a *App) clearCachesAfterRestore() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if a.Cache != nil {
		for _, p := range restoreCachePrefixes {
			if err := a.Cache.DelPrefix(ctx, p); err != nil {
				a.Log.Warn("Could not clear cache after restore", "prefix", p, "error", err)
			}
		}
	}
	if a.State != nil {
		if err := a.State.DelPrefix(ctx, "refresh:"); err != nil {
			a.Log.Warn("Could not sign out sessions after restore", "error", err)
		}
	}
}

// DeleteBackup removes a backup file.
func (a *App) DeleteBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	if job := a.backupJob.get(); job.Running && job.Name == filepath.Base(p) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "That backup is being restored", nil, "")
	}
	if err := os.Remove(p); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]string{"deleted": filepath.Base(p)})
}

// RestoreStatus is the public view of a restore: no error text, no names.
type RestoreStatus struct {
	Running  bool   `json:"running"`
	Phase    string `json:"phase"`
	Table    string `json:"table,omitempty"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
	Finished bool   `json:"finished"`
	Failed   bool   `json:"failed"`
}

// MaintenanceStatus answers GET /api/maintenance.
type MaintenanceStatus struct {
	Active  bool           `json:"active"`
	Restore *RestoreStatus `json:"restore,omitempty"`
}

// GetMaintenance reports maintenance mode and, during and for an hour after a
// restore, its progress. Public: the admin who started a restore may not
// exist in the backup.
func (a *App) GetMaintenance(r *fastglue.Request) error {
	st := MaintenanceStatus{Active: a.Maintenance.Active()}
	if job := a.backupJob.get(); job.Kind == "restore" && time.Since(job.StartedAt) < time.Hour {
		st.Restore = &RestoreStatus{
			Running: job.Running, Phase: job.Phase, Table: job.Table, Done: job.Done, Total: job.Total,
			Finished: !job.Running, Failed: !job.Running && job.Error != "",
		}
	}
	return r.SendEnvelope(st)
}

// MaintenanceGuard answers 503 to API, websocket and public media requests
// while a restore runs. /api/maintenance, health checks and the web app pass.
func (a *App) MaintenanceGuard(r *fastglue.Request) *fastglue.Request {
	if !a.Maintenance.Active() {
		return r
	}
	path := string(r.RequestCtx.Path())
	if strings.HasSuffix(path, "/api/maintenance") {
		return r
	}
	if strings.Contains(path, "/api/") || strings.HasSuffix(path, "/ws") || strings.Contains(path, "/public/") {
		_ = r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Restore in progress", nil, "")
		return nil
	}
	return r
}
```

- [ ] **Step 5: Implement `backup_upload.go`**

```go
package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// uploadChunkMax keeps each chunk well under the server's 15 MB body limit.
const uploadChunkMax = 8 << 20

type backupUpload struct {
	name     string
	path     string
	size     int64
	received int64
}

type uploadStore struct {
	mu    sync.Mutex
	items map[string]*backupUpload
}

func (s *uploadStore) get(id string) *backupUpload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[id]
}

func (s *uploadStore) put(id string, u *backupUpload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]*backupUpload{}
	}
	s.items[id] = u
}

func (s *uploadStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

// StartBackupUpload begins a chunked upload of a backup file.
func (a *App) StartBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	var req struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if !strings.HasSuffix(strings.ToLower(req.Name), ".zip") || req.Size <= 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose a whatomate backup (.zip)", nil, "")
	}
	if limit := int64(a.Config.Backup.MaxUploadMB) << 20; req.Size > limit {
		return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge,
			fmt.Sprintf("The backup is larger than the %d MB upload limit ([backup] max_upload_mb)", a.Config.Backup.MaxUploadMB), nil, "")
	}
	if err := os.MkdirAll(a.Config.Backup.Dir, 0o750); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	if err := backup.EnsureSpace(a.Config.Backup.Dir, req.Size); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInsufficientStorage, err.Error(), nil, "")
	}
	id := uuid.NewString()
	p := filepath.Join(a.Config.Backup.Dir, backup.UploadPrefix+id+".part")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	_ = f.Close()
	a.uploads.put(id, &backupUpload{name: req.Name, path: p, size: req.Size})
	return r.SendEnvelope(map[string]any{"id": id, "received": 0, "chunk_max": uploadChunkMax})
}

// PutBackupUploadChunk appends one chunk at ?offset=N.
func (a *App) PutBackupUploadChunk(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	u := a.uploads.get(id)
	if u == nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Upload not found", nil, "")
	}
	offset, err := strconv.ParseInt(string(r.RequestCtx.QueryArgs().Peek("offset")), 10, 64)
	if err != nil || offset != u.received {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, fmt.Sprintf("Expected offset %d", u.received), map[string]int64{"received": u.received}, "")
	}
	body := r.RequestCtx.PostBody()
	if len(body) == 0 || len(body) > uploadChunkMax || u.received+int64(len(body)) > u.size {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Bad chunk size", nil, "")
	}
	f, err := os.OpenFile(u.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	n, err := f.Write(body)
	_ = f.Close()
	u.received += int64(n)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]int64{"received": u.received})
}

// FinishBackupUpload checks the uploaded file and adds it to the list.
func (a *App) FinishBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	u := a.uploads.get(id)
	if u == nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Upload not found", nil, "")
	}
	if u.received != u.size {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, fmt.Sprintf("Received %d of %d bytes", u.received, u.size), nil, "")
	}
	a.uploads.remove(id)
	final := filepath.Join(a.Config.Backup.Dir, backup.UploadName(a.Config.Backup.Dir, u.name))
	if err := os.Rename(u.path, final); err != nil {
		_ = os.Remove(u.path)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	sum, err := backup.Inspect(final, a.DB, a.Config.App.EncryptionKey)
	if err != nil {
		_ = os.Remove(final)
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, err.Error(), nil, "")
	}
	m := sum.Manifest
	return r.SendEnvelope(backup.Info{Name: sum.Name, Size: sum.Size, Manifest: &m})
}

// CancelBackupUpload drops a partial upload.
func (a *App) CancelBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	if u := a.uploads.get(id); u != nil {
		_ = os.Remove(u.path)
		a.uploads.remove(id)
	}
	return r.SendEnvelope(map[string]string{"cancelled": id})
}
```

Add to `App` in `app.go` (unexported, zero values are ready to use):

```go
	backupJob backupJobs
	uploads   uploadStore
```

- [ ] **Step 6: Run the tests**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/handlers -run 'Backup|Maintenance|IsBackupDownload' -count=1 -v`
Expected: PASS. Then the whole package on SQLite: `TEST_DB_DRIVER=sqlite go test ./internal/handlers -count=1` — Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers
git commit -m "feat(handlers): backup and restore API with maintenance mode"
```

---

### Task 9: Server wiring and the command line

**Files:**
- Modify: `cmd/whatomate/main.go` (routes, guard, HeaderReceived, App fields, workers' gate, startup cleanup, server lock, `backup`/`restore` commands, usage)
- Create: `cmd/whatomate/backup_cmd.go`
- Create: `internal/backup/lock.go`, `internal/backup/lock_unix.go`, `internal/backup/lock_windows.go`, `internal/backup/lock_test.go`

**Interfaces:**
- Consumes: Tasks 3–8.
- Produces: `backup.WriteServerLock(dir string) error`, `backup.RemoveServerLock(dir string)`, `backup.ServerRunning(dir string) (pid int, running bool)`; CLI `whatomate backup`, `whatomate restore`.

- [ ] **Step 1: Write the failing lock test**

`internal/backup/lock_test.go`:

```go
package backup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerLock(t *testing.T) {
	dir := t.TempDir()
	_, running := ServerRunning(dir)
	assert.False(t, running)

	require.NoError(t, WriteServerLock(dir))
	_, running = ServerRunning(dir)
	assert.False(t, running, "our own PID does not count")

	if runtime.GOOS != "windows" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte("1\n"), 0o640))
		pid, running := ServerRunning(dir)
		assert.Equal(t, 1, pid)
		assert.True(t, running, "PID 1 is always alive")
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte("999999999\n"), 0o640))
	_, running = ServerRunning(dir)
	assert.False(t, running, "a dead PID is stale")

	require.NoError(t, WriteServerLock(dir))
	RemoveServerLock(dir)
	assert.NoFileExists(t, filepath.Join(dir, lockName))
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/backup -run TestServerLock -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement the lock**

`internal/backup/lock.go`:

```go
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const lockName = "server.lock"

// WriteServerLock records that a server using this backup folder is running.
func WriteServerLock(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, lockName), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o640)
}

// RemoveServerLock removes the lock if this process wrote it.
func RemoveServerLock(dir string) {
	if pid, ok := readLock(dir); ok && pid == os.Getpid() {
		_ = os.Remove(filepath.Join(dir, lockName))
	}
}

// ServerRunning reports whether another live process holds the lock.
func ServerRunning(dir string) (int, bool) {
	pid, ok := readLock(dir)
	if !ok || pid == os.Getpid() {
		return pid, false
	}
	return pid, processAlive(pid)
}

func readLock(dir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid, err == nil && pid > 0
}
```

`internal/backup/lock_unix.go`:

```go
//go:build !windows

package backup

import (
	"errors"
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
```

`internal/backup/lock_windows.go`:

```go
//go:build windows

package backup

import "os"

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
```

Run: `go test ./internal/backup -run TestServerLock -count=1 && GOOS=windows go vet ./internal/backup`
Expected: PASS / no output.

- [ ] **Step 4: Wire the server (`runServer` in `main.go`)**

After `cfg, cfgPath := loadConfig(*configPath, lo)`:

```go
	backup.CleanTemp(cfg.Backup.Dir)
	if err := backup.WriteServerLock(cfg.Backup.Dir); err != nil {
		lo.Warn("Could not write the server lock in the backup folder", "dir", cfg.Backup.Dir, "error", err)
	}
	defer backup.RemoveServerLock(cfg.Backup.Dir)
	gate := &maintenance.Gate{}
```

In the `app := &handlers.App{...}` literal add `Maintenance: gate, Version: Version,`.

After `w := worker.New(cfg, db, consumer, stores.Broker, lo)` add `w.Gate = gate`.

In the `fasthttp.Server` literal add:

```go
		HeaderReceived: func(h *fasthttp.RequestHeader) fasthttp.RequestConfig {
			if handlers.IsBackupDownload(string(h.Method()), string(h.RequestURI())) {
				return fasthttp.RequestConfig{WriteTimeout: 6 * time.Hour}
			}
			return fasthttp.RequestConfig{}
		},
```

In `setupRoutes`, next to the server-settings routes:

```go
	g.GET("/api/maintenance", app.GetMaintenance)
	g.GET("/api/admin/backups", app.ListBackups)
	g.POST("/api/admin/backups", app.CreateBackup)
	g.GET("/api/admin/backup-job", app.GetBackupJob)
	g.GET("/api/admin/backups/{name}/download", app.DownloadBackup)
	g.GET("/api/admin/backups/{name}/inspect", app.InspectBackup)
	g.POST("/api/admin/backups/{name}/restore", app.RestoreBackup)
	g.DELETE("/api/admin/backups/{name}", app.DeleteBackup)
	g.POST("/api/admin/backup-uploads", app.StartBackupUpload)
	g.PUT("/api/admin/backup-uploads/{id}", app.PutBackupUploadChunk)
	g.POST("/api/admin/backup-uploads/{id}/finish", app.FinishBackupUpload)
	g.DELETE("/api/admin/backup-uploads/{id}", app.CancelBackupUpload)
```

Immediately before the auth `g.Before(func(r *fastglue.Request) ...` add `g.Before(app.MaintenanceGuard)`, and add `path == "/api/maintenance" ||` to the public-route list inside the auth hook.

- [ ] **Step 5: The commands**

In `main()`'s switch:

```go
	case "backup":
		runBackupCmd(args[1:])
	case "restore":
		runRestoreCmd(args[1:])
```

In `printUsage`, under Commands:

```
  backup    Back up the database (and files) into the backup folder
  restore   Restore a backup into the configured database
```

and a section:

```
Backup Options:
  -config string    Path to config file
  -no-files         Leave out uploaded media and audio
  -o string         Write to this file instead of the backup folder

Restore Options:
  -config string    Path to config file
  -yes              Do not ask to type RESTORE
```

`cmd/whatomate/backup_cmd.go`:

```go
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/zerodha/logf"
)

func cliLogger() logf.Logger {
	return logf.New(logf.Opts{EnableColor: true, Level: logf.InfoLevel, TimestampFormat: "2006-01-02 15:04:05", DefaultFields: []any{"app", "whatomate"}})
}

// cliAreas mirrors handlers.App.backupAreas for the command line.
func cliAreas(cfg *config.Config) []backup.FileArea {
	media := cfg.Storage.LocalPath
	if media == "" {
		media = "./media"
	}
	return backup.Areas(media, cfg.Calling.AudioDir)
}

func printProgress(p backup.Progress) {
	switch {
	case p.Total > 0:
		fmt.Printf("\r  %-8s %-32s %d/%d   ", p.Phase, p.Table, p.Done, p.Total)
	default:
		fmt.Printf("\r  %-8s %-32s %d   ", p.Phase, p.Table, p.Done)
	}
}

func runBackupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file")
	noFiles := fs.Bool("no-files", false, "Leave out uploaded media and audio")
	out := fs.String("o", "", "Write to this file instead of the backup folder")
	_ = fs.Parse(args)

	lo := cliLogger()
	if _, err := config.Locate(*configPath); errors.Is(err, config.ErrNotFound) {
		lo.Fatal("No config file found; run whatomate server first to set one up")
	}
	cfg, _ := loadConfig(*configPath, lo)
	db, err := database.Open(&cfg.Database, false)
	if err != nil {
		lo.Fatal("Failed to connect to database", "error", err)
	}
	info, err := backup.Create(context.Background(), db, cfg.Backup.Dir, backup.Options{
		Kind: backup.KindCLI, AppVersion: Version, EncryptionKey: cfg.App.EncryptionKey,
		IncludeFiles: !*noFiles, Areas: cliAreas(cfg), Skip: []string{cfg.Backup.Dir}, Path: *out,
	}, printProgress)
	fmt.Println()
	if err != nil {
		lo.Fatal("Backup failed", "error", err)
	}
	where := *out
	if where == "" {
		where = cfg.Backup.Dir + string(os.PathSeparator) + info.Name
	}
	fmt.Printf("Backup written: %s (%d MB)\n", where, info.Size>>20)
}

func runRestoreCmd(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file")
	yes := fs.Bool("yes", false, "Do not ask to type RESTORE")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Println("Usage: whatomate restore [-config f] [-yes] <backup.zip>")
		os.Exit(2)
	}
	file := fs.Arg(0)

	lo := cliLogger()
	if _, err := config.Locate(*configPath); errors.Is(err, config.ErrNotFound) {
		lo.Fatal("No config file found; run whatomate server first to set one up, then restore")
	}
	cfg, _ := loadConfig(*configPath, lo)
	if pid, running := backup.ServerRunning(cfg.Backup.Dir); running {
		lo.Fatal("whatomate server is running; stop it first or restore from Settings → Backup & Restore", "pid", pid)
	}
	db, err := database.Open(&cfg.Database, false)
	if err != nil {
		lo.Fatal("Failed to connect to database", "error", err)
	}
	sum, err := backup.Inspect(file, db, cfg.App.EncryptionKey)
	if err != nil {
		lo.Fatal("Cannot restore this file", "error", err)
	}
	m := sum.Manifest
	var rows int64
	for _, t := range m.Tables {
		rows += t.Rows
	}
	fmt.Printf("Backup:    %s (%d MB)\nMade:      %s with whatomate %s on %s\nRestoring: into %s\nRows:      %d in %d tables\nFiles:     %v\n",
		sum.Name, sum.Size>>20, m.CreatedAt.Format("2006-01-02 15:04 MST"), m.AppVersion, m.SourceDriver, sum.TargetDriver, rows, len(m.Tables), m.IncludeFiles)
	if !sum.KeyMatches {
		fmt.Println("WARNING: this backup was made with a different encryption_key. Saved WhatsApp tokens, AI keys and SSO secrets will not decrypt and must be entered again.")
	}
	fmt.Println("Everything in the current database will be replaced. A safety backup is taken first.")
	if !*yes {
		fmt.Print("Type RESTORE to continue: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "RESTORE" {
			fmt.Println("Cancelled.")
			os.Exit(1)
		}
	}
	areas := cliAreas(cfg)
	safety, err := backup.Create(context.Background(), db, cfg.Backup.Dir, backup.Options{
		Kind: backup.KindPreRestore, AppVersion: Version, EncryptionKey: cfg.App.EncryptionKey,
		IncludeFiles: m.IncludeFiles, Areas: areas, Skip: []string{cfg.Backup.Dir},
	}, printProgress)
	fmt.Println()
	if err != nil {
		lo.Fatal("Safety backup failed; nothing was changed", "error", err)
	}
	fmt.Printf("Safety backup: %s\n", safety.Name)
	err = backup.Restore(context.Background(), db, file, backup.RestoreOptions{
		EncryptionKey: cfg.App.EncryptionKey, Areas: areas, Skip: []string{cfg.Backup.Dir},
	}, printProgress)
	fmt.Println()
	if errors.Is(err, backup.ErrFiles) {
		lo.Fatal("Restore incomplete", "error", err, "undo_with", safety.Name)
	}
	if err != nil {
		lo.Fatal("Restore failed; nothing was changed", "error", err)
	}
	fmt.Println("Restore finished. Start whatomate and sign in with an account from the backup.")
}
```

- [ ] **Step 6: Build and try it**

Run: `CGO_ENABLED=0 go build -o /tmp/whatomate-bk ./cmd/whatomate && go vet ./cmd/whatomate ./internal/...`
Expected: builds, no vet findings.

Then on a throwaway SQLite config (never the dev config):

```bash
mkdir -p /tmp/bkcli && cat > /tmp/bkcli/config.toml <<'EOF'
[database]
driver = "sqlite"
path = "w.db"
EOF
/tmp/whatomate-bk server -config /tmp/bkcli/config.toml -migrate & SERVER=$!; sleep 3; kill $SERVER
/tmp/whatomate-bk backup -config /tmp/bkcli/config.toml
/tmp/whatomate-bk restore -config /tmp/bkcli/config.toml -yes /tmp/bkcli/backups/whatomate-backup-*.zip
```

Expected: a backup line with a file name, then "Restore finished." Starting the server again and running `restore` while it runs prints the "server is running" refusal. Remove `/tmp/bkcli` afterwards.

- [ ] **Step 7: Commit**

```bash
git add cmd/whatomate internal/backup
git commit -m "feat: wire backup routes, maintenance guard and backup/restore commands"
```

---

### Task 10: Frontend helpers and API client

**Files:**
- Create: `frontend/src/lib/backup.ts`, `frontend/src/lib/backup.spec.ts`
- Modify: `frontend/src/services/api.ts` (types and `backupsService`, near `serverSettingsService`)

**Interfaces:**
- Produces: `RESTORE_WORD`, `canConfirmRestore(text)`, `formatBytes(n)`, `jobPercent({done,total})`, `UPLOAD_CHUNK`, `UploadApi`, `uploadInChunks(file, api, onProgress, chunkSize?)`; `backupsService` and the `Backup*`/`MaintenanceStatus` types.

- [ ] **Step 1: Write the failing spec**

`frontend/src/lib/backup.spec.ts`:

```ts
import { describe, expect, it, vi } from 'vitest'
import { canConfirmRestore, formatBytes, jobPercent, uploadInChunks, type UploadApi } from './backup'

describe('canConfirmRestore', () => {
  it('needs exactly RESTORE', () => {
    expect(canConfirmRestore('RESTORE')).toBe(true)
    expect(canConfirmRestore('  RESTORE ')).toBe(true)
    expect(canConfirmRestore('restore')).toBe(false)
    expect(canConfirmRestore('')).toBe(false)
  })
})

describe('formatBytes', () => {
  it('uses binary units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatBytes(3 * 1024 ** 3)).toBe('3.0 GB')
  })
})

describe('jobPercent', () => {
  it('handles unknown totals', () => {
    expect(jobPercent({ done: 5, total: 0 })).toBe(0)
    expect(jobPercent({ done: 50, total: 200 })).toBe(25)
    expect(jobPercent({ done: 300, total: 200 })).toBe(100)
  })
})

describe('uploadInChunks', () => {
  function fakeApi(): UploadApi & { chunks: number[] } {
    const chunks: number[] = []
    return {
      chunks,
      start: vi.fn(async () => ({ id: 'u1' })),
      put: vi.fn(async (_id: string, offset: number, chunk: Blob) => {
        chunks.push(chunk.size)
        return { received: offset + chunk.size }
      }),
      finish: vi.fn(async () => ({ name: 'b.zip' })),
      cancel: vi.fn(async () => ({})),
    }
  }

  it('sends the file in order and finishes', async () => {
    const api = fakeApi()
    const file = Object.assign(new Blob([new Uint8Array(10)]), { name: 'b.zip' })
    const seen: number[] = []
    const res = await uploadInChunks(file, api, (sent) => seen.push(sent), 4)
    expect(api.chunks).toEqual([4, 4, 2])
    expect(seen).toEqual([4, 8, 10])
    expect(res).toEqual({ name: 'b.zip' })
    expect(api.cancel).not.toHaveBeenCalled()
  })

  it('cancels when a chunk fails', async () => {
    const api = fakeApi()
    api.put = vi.fn(async () => { throw new Error('network') })
    const file = Object.assign(new Blob([new Uint8Array(3)]), { name: 'b.zip' })
    await expect(uploadInChunks(file, api, () => {}, 4)).rejects.toThrow('network')
    expect(api.cancel).toHaveBeenCalledWith('u1')
  })
})
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd frontend && npx vitest run src/lib/backup.spec.ts`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement `lib/backup.ts`**

```ts
export const RESTORE_WORD = 'RESTORE'

export function canConfirmRestore(text: string): boolean {
  return text.trim() === RESTORE_WORD
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

export function jobPercent(job: { done: number; total: number }): number {
  if (!job.total) return 0
  return Math.min(100, Math.round((job.done / job.total) * 100))
}

export const UPLOAD_CHUNK = 4 * 1024 * 1024

export interface UploadApi {
  start(name: string, size: number): Promise<{ id: string }>
  put(id: string, offset: number, chunk: Blob): Promise<{ received: number }>
  finish(id: string): Promise<unknown>
  cancel(id: string): Promise<unknown>
}

/** Uploads file in chunks; on failure the partial upload is cancelled. */
export async function uploadInChunks(
  file: Blob & { name: string },
  api: UploadApi,
  onProgress: (sent: number, total: number) => void,
  chunkSize = UPLOAD_CHUNK,
): Promise<unknown> {
  const { id } = await api.start(file.name, file.size)
  let offset = 0
  try {
    while (offset < file.size) {
      const chunk = file.slice(offset, Math.min(offset + chunkSize, file.size))
      const { received } = await api.put(id, offset, chunk)
      offset = received
      onProgress(offset, file.size)
    }
    return await api.finish(id)
  } catch (e) {
    await api.cancel(id).catch(() => {})
    throw e
  }
}
```

- [ ] **Step 4: Add the API client**

In `frontend/src/services/api.ts`, after `serverSettingsService`:

```ts
export interface BackupManifest {
  format: number
  app_version: string
  kind: 'manual' | 'pre-restore' | 'cli'
  created_at: string
  source_driver: string
  key_fingerprint: string
  include_files: boolean
  tables: { name: string; rows: number; sha256: string; columns: string[] }[]
  files?: Record<string, { count: number; bytes: number }>
}

export interface BackupInfo {
  name: string
  size: number
  manifest?: BackupManifest
  error?: string
}

export interface BackupJob {
  kind: '' | 'backup' | 'restore'
  running: boolean
  phase: string
  table?: string
  done: number
  total: number
  name?: string
  safety_name?: string
  error?: string
  started_at: string
  finished_at?: string
}

export interface BackupSummary {
  name: string
  size: number
  manifest: BackupManifest
  target_driver: string
  key_matches: boolean
}

export interface MaintenanceStatus {
  active: boolean
  restore?: { running: boolean; phase: string; table?: string; done: number; total: number; finished: boolean; failed: boolean }
}

const backupPath = (name: string) => `/admin/backups/${encodeURIComponent(name)}`

export const backupsService = {
  list: () => api.get<{ data: { dir: string; backups: BackupInfo[]; job: BackupJob } }>('/admin/backups'),
  create: (includeFiles: boolean) => api.post<{ data: BackupJob }>('/admin/backups', { include_files: includeFiles }),
  job: () => api.get<{ data: BackupJob }>('/admin/backup-job'),
  inspect: (name: string) => api.get<{ data: BackupSummary }>(`${backupPath(name)}/inspect`),
  restore: (name: string) => api.post<{ data: BackupJob }>(`${backupPath(name)}/restore`, { confirm: 'RESTORE' }),
  remove: (name: string) => api.delete(backupPath(name)),
  downloadUrl: (name: string) => `${API_BASE_URL}${backupPath(name)}/download`,
  startUpload: (name: string, size: number) => api.post<{ data: { id: string } }>('/admin/backup-uploads', { name, size }),
  putChunk: (id: string, offset: number, chunk: Blob) =>
    api.put<{ data: { received: number } }>(`/admin/backup-uploads/${id}`, chunk, {
      params: { offset },
      headers: { 'Content-Type': 'application/octet-stream' },
      timeout: 120000,
    }),
  finishUpload: (id: string) => api.post<{ data: BackupInfo }>(`/admin/backup-uploads/${id}/finish`, undefined, { timeout: 120000 }),
  cancelUpload: (id: string) => api.delete(`/admin/backup-uploads/${id}`),
  maintenance: () => api.get<{ data: MaintenanceStatus }>('/maintenance'),
}
```

- [ ] **Step 5: Run checks**

Run: `cd frontend && npx vitest run && npx vue-tsc --noEmit 2>&1 | rg -c "error TS"`
Expected: all specs pass; the vue-tsc error count is the existing baseline (20), unchanged.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/backup.ts frontend/src/lib/backup.spec.ts frontend/src/services/api.ts
git commit -m "feat(frontend): backup API client and upload helpers"
```

---

### Task 11: Backup & Restore page

**Files:**
- Create: `frontend/src/views/settings/BackupView.vue`, `frontend/src/views/settings/RestoreBackupDialog.vue`
- Modify: `frontend/src/router/index.ts` (route after `settings/organizations`; `navigationOrder` entry after `/settings/organizations`)
- Modify: `frontend/src/components/layout/navigation.ts` (child after Organizations; import `Archive` from lucide)
- Modify: `frontend/src/i18n/locales/en.json` (`nav.backup`, `backup.*`)

**Interfaces:**
- Consumes: `backupsService`, types and helpers from Task 10; `PageHeader`, `DeleteConfirmDialog` from `@/components/shared`; `useAuthStore().logout()`.

- [ ] **Step 1: Strings**

In `en.json` add `"backup": "Backup & Restore"` inside `"nav"`, and a top-level `"backup"` object:

```json
"backup": {
  "title": "Backup & Restore",
  "subtitle": "Back up everything on this server and restore it, even onto a different database",
  "createTitle": "Back up now",
  "createDesc": "Saves every organization's data. Backups are kept on the server.",
  "includeFiles": "Include uploaded files (media, audio, local call recordings)",
  "backUpNow": "Back up now",
  "savedIn": "Saved in {dir}",
  "listTitle": "Backups",
  "listDesc": "Download a backup to keep it somewhere else, or upload one made on another server.",
  "upload": "Upload backup",
  "uploading": "Uploading {percent}%",
  "uploaded": "Backup uploaded",
  "empty": "No backups yet",
  "colDate": "Date",
  "colSize": "Size",
  "colMadeOn": "Made on",
  "colFiles": "Files",
  "filesYes": "Yes",
  "filesNo": "No",
  "preRestore": "Before restore",
  "unreadable": "Unreadable: {error}",
  "download": "Download",
  "restore": "Restore",
  "delete": "Delete",
  "deleteTitle": "Delete backup",
  "deleteDesc": "The backup file is removed from the server.",
  "started": "Backup started",
  "finished": "Backup finished: {name}",
  "failed": "Backup failed: {error}",
  "phase": {
    "tables": "Saving tables",
    "files": "Saving files",
    "verify": "Checking the backup",
    "load": "Loading data",
    "links": "Linking records",
    "swap": "Putting files back",
    "safety_backup": "Taking a safety backup",
    "done": "Done",
    "failed": "Failed"
  },
  "restoreDialog": {
    "title": "Restore {name}",
    "checking": "Checking the backup…",
    "made": "Made {date} with whatomate {version}",
    "move": "{from} → {to}",
    "rows": "{rows} rows in {tables} tables",
    "files": "Includes {count} files ({size})",
    "noFiles": "Database only; current files stay as they are",
    "replaceWarning": "Everything on this server is replaced by the backup. A safety backup of the current state is taken first.",
    "keyWarning": "This backup was made with a different encryption key. Saved WhatsApp tokens, AI keys and SSO secrets will not work and must be entered again.",
    "workersWarning": "Stop any separate “whatomate worker” processes first. Calls in progress may be cut off.",
    "signOutWarning": "Everyone is signed out. Sign in again with an account from the backup.",
    "typeToConfirm": "Type {word} to confirm",
    "start": "Restore",
    "running": "Restoring… don't close this page",
    "doneTitle": "Restore finished",
    "doneBody": "Sign in with an account from the backup.",
    "signIn": "Go to sign in",
    "failedTitle": "Restore failed",
    "failedBody": "Nothing was changed."
  }
}
```

- [ ] **Step 2: Route and menu**

`router/index.ts`, after the organizations route:

```ts
        {
          path: 'settings/backup',
          name: 'backup',
          component: () => import('@/views/settings/BackupView.vue'),
          meta: { superAdmin: true }
        },
```

and in `navigationOrder`'s settings `childPaths`, after organizations: `{ path: '/settings/backup', superAdmin: true },`.

`navigation.ts`, after the Organizations child (add `Archive` to the lucide import):

```ts
          { name: 'nav.backup', path: '/settings/backup', icon: Archive, superAdmin: true },
```

- [ ] **Step 3: `RestoreBackupDialog.vue`**

```vue
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { AlertTriangle, CheckCircle2, Loader2, XCircle } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Progress } from '@/components/ui/progress'
import { backupsService, type BackupSummary, type MaintenanceStatus } from '@/services/api'
import { RESTORE_WORD, canConfirmRestore, formatBytes, jobPercent } from '@/lib/backup'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ open: boolean; name: string | null }>()
const emit = defineEmits<{ 'update:open': [boolean]; finished: [] }>()
const { t } = useI18n()
const router = useRouter()
const authStore = useAuthStore()

type Stage = 'loading' | 'confirm' | 'running' | 'done' | 'failed'
const stage = ref<Stage>('loading')
const summary = ref<BackupSummary | null>(null)
const error = ref('')
const typed = ref('')
const status = ref<MaintenanceStatus['restore'] | null>(null)
let timer: ReturnType<typeof setInterval> | null = null

const rows = computed(() => summary.value?.manifest.tables.reduce((n, tb) => n + tb.rows, 0) ?? 0)
const files = computed(() => {
  const f = summary.value?.manifest.files ?? {}
  return Object.values(f).reduce((acc, s) => ({ count: acc.count + s.count, bytes: acc.bytes + s.bytes }), { count: 0, bytes: 0 })
})
const percent = computed(() => (status.value ? jobPercent(status.value) : 0))

function stopPolling() {
  if (timer) clearInterval(timer)
  timer = null
}

watch(() => [props.open, props.name], async ([open]) => {
  stopPolling()
  if (!open || !props.name) return
  stage.value = 'loading'
  summary.value = null
  error.value = ''
  typed.value = ''
  try {
    const res = await backupsService.inspect(props.name)
    summary.value = res.data.data
    stage.value = 'confirm'
  } catch (e) {
    error.value = getErrorMessage(e, t('backup.restoreDialog.failedTitle'))
    stage.value = 'failed'
  }
}, { immediate: true })

async function poll() {
  try {
    const res = await backupsService.maintenance()
    const r = res.data.data.restore
    if (!r) return
    status.value = r
    if (!r.finished) return
    stopPolling()
    if (r.failed) {
      stage.value = 'failed'
      try {
        const job = await backupsService.job()
        error.value = job.data.data.error || t('backup.restoreDialog.failedBody')
      } catch {
        error.value = t('backup.restoreDialog.failedBody')
      }
    } else {
      stage.value = 'done'
    }
  } catch {
    // The server is busy restoring; keep polling.
  }
}

async function start() {
  if (!props.name || !canConfirmRestore(typed.value)) return
  try {
    await backupsService.restore(props.name)
    stage.value = 'running'
    timer = setInterval(poll, 1000)
  } catch (e) {
    error.value = getErrorMessage(e, t('backup.restoreDialog.failedTitle'))
    stage.value = 'failed'
  }
}

async function signIn() {
  await authStore.logout().catch(() => {})
  emit('update:open', false)
  router.push('/login')
}

function close(open: boolean) {
  if (stage.value === 'running') return
  if (!open && stage.value === 'failed') emit('finished')
  emit('update:open', open)
}

onBeforeUnmount(stopPolling)
</script>

<template>
  <Dialog :open="open" @update:open="close">
    <DialogContent class="sm:max-w-lg" @interact-outside="(e: Event) => stage === 'running' && e.preventDefault()">
      <DialogHeader>
        <DialogTitle>{{ $t('backup.restoreDialog.title', { name: name ?? '' }) }}</DialogTitle>
        <DialogDescription v-if="stage === 'loading'">{{ $t('backup.restoreDialog.checking') }}</DialogDescription>
      </DialogHeader>

      <div v-if="stage === 'loading'" class="flex justify-center py-8"><Loader2 class="h-6 w-6 animate-spin text-white/40 light:text-gray-400" /></div>

      <div v-else-if="stage === 'confirm' && summary" class="space-y-4 text-sm">
        <div class="space-y-1 rounded-lg border border-white/[0.08] bg-white/[0.02] p-3 light:border-gray-200 light:bg-gray-50">
          <p class="font-medium">{{ $t('backup.restoreDialog.move', { from: summary.manifest.source_driver, to: summary.target_driver }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.made', { date: formatDate(summary.manifest.created_at, { dateStyle: 'medium', timeStyle: 'short' }), version: summary.manifest.app_version || '?' }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.rows', { rows: rows.toLocaleString(), tables: summary.manifest.tables.length }) }}</p>
          <p class="text-white/60 light:text-gray-600">
            {{ summary.manifest.include_files ? $t('backup.restoreDialog.files', { count: files.count, size: formatBytes(files.bytes) }) : $t('backup.restoreDialog.noFiles') }}
          </p>
        </div>
        <div v-if="!summary.key_matches" class="flex gap-2 rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-red-300 light:text-red-700">
          <XCircle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.keyWarning') }}
        </div>
        <ul class="space-y-2 text-amber-300 light:text-amber-700">
          <li class="flex gap-2"><AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.replaceWarning') }}</li>
          <li class="flex gap-2"><AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.workersWarning') }}</li>
          <li class="flex gap-2"><AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.signOutWarning') }}</li>
        </ul>
        <div class="space-y-1.5">
          <label for="restore-confirm" class="text-white/70 light:text-gray-700">{{ $t('backup.restoreDialog.typeToConfirm', { word: RESTORE_WORD }) }}</label>
          <Input id="restore-confirm" v-model="typed" autocomplete="off" class="font-mono" data-testid="restore-confirm" />
        </div>
      </div>

      <div v-else-if="stage === 'running'" class="space-y-3 py-2 text-sm">
        <p class="flex items-center gap-2"><Loader2 class="h-4 w-4 animate-spin" />{{ $t('backup.restoreDialog.running') }}</p>
        <Progress :model-value="percent" class="h-2" />
        <p class="text-white/50 light:text-gray-500">{{ status ? $t(`backup.phase.${status.phase}`, status.phase) : '' }}<span v-if="status?.table"> · {{ status.table }}</span></p>
      </div>

      <div v-else-if="stage === 'done'" class="flex gap-3 py-2 text-sm">
        <CheckCircle2 class="h-5 w-5 shrink-0 text-emerald-400" />
        <div><p class="font-medium">{{ $t('backup.restoreDialog.doneTitle') }}</p><p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.doneBody') }}</p></div>
      </div>

      <div v-else class="flex gap-3 py-2 text-sm">
        <XCircle class="h-5 w-5 shrink-0 text-red-400" />
        <div><p class="font-medium">{{ $t('backup.restoreDialog.failedTitle') }}</p><p class="break-words text-white/60 light:text-gray-600">{{ error }}</p></div>
      </div>

      <DialogFooter>
        <Button v-if="stage === 'confirm'" variant="destructive" :disabled="!canConfirmRestore(typed)" data-testid="restore-start" @click="start">{{ $t('backup.restoreDialog.start') }}</Button>
        <Button v-else-if="stage === 'done'" data-testid="restore-sign-in" @click="signIn">{{ $t('backup.restoreDialog.signIn') }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
```

- [ ] **Step 4: `BackupView.vue`**

```vue
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Archive, Download, History, Loader2, RotateCcw, Trash2, Upload } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Progress } from '@/components/ui/progress'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { PageHeader, DeleteConfirmDialog, IconButton } from '@/components/shared'
import { backupsService, type BackupInfo, type BackupJob } from '@/services/api'
import { formatBytes, jobPercent, uploadInChunks } from '@/lib/backup'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'
import RestoreBackupDialog from './RestoreBackupDialog.vue'

const { t } = useI18n()
const backups = ref<BackupInfo[]>([])
const dir = ref('')
const job = ref<BackupJob | null>(null)
const loading = ref(true)
const includeFiles = ref(true)
const uploadPercent = ref<number | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const restoreName = ref<string | null>(null)
const restoreOpen = ref(false)
const deleteName = ref<string | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

const backupRunning = computed(() => !!job.value?.running && job.value.kind === 'backup')
const busy = computed(() => !!job.value?.running || uploadPercent.value !== null)

async function load() {
  try {
    const res = await backupsService.list()
    backups.value = res.data.data.backups
    dir.value = res.data.data.dir
    job.value = res.data.data.job
    if (job.value?.running) startPolling()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('backup.listTitle') })))
  } finally {
    loading.value = false
  }
}

function startPolling() {
  if (timer) return
  timer = setInterval(async () => {
    try {
      const res = await backupsService.job()
      job.value = res.data.data
      if (!job.value.running) {
        clearInterval(timer!)
        timer = null
        if (job.value.kind === 'backup') {
          if (job.value.error) toast.error(t('backup.failed', { error: job.value.error }))
          else toast.success(t('backup.finished', { name: job.value.name ?? '' }))
        }
        await load()
      }
    } catch {
      // keep polling
    }
  }, 1000)
}

async function backUpNow() {
  try {
    const res = await backupsService.create(includeFiles.value)
    job.value = res.data.data
    toast.success(t('backup.started'))
    startPolling()
  } catch (e) {
    toast.error(getErrorMessage(e, t('backup.failed', { error: '' })))
  }
}

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  uploadPercent.value = 0
  try {
    await uploadInChunks(file, {
      start: async (name, size) => (await backupsService.startUpload(name, size)).data.data,
      put: async (id, offset, chunk) => (await backupsService.putChunk(id, offset, chunk)).data.data,
      finish: async (id) => (await backupsService.finishUpload(id)).data.data,
      cancel: (id) => backupsService.cancelUpload(id),
    }, (sent, total) => { uploadPercent.value = Math.round((sent / total) * 100) })
    toast.success(t('backup.uploaded'))
    await load()
  } catch (err) {
    toast.error(getErrorMessage(err, t('backup.failed', { error: '' })))
  } finally {
    uploadPercent.value = null
  }
}

async function confirmDelete() {
  if (!deleteName.value) return
  deleting.value = true
  try {
    await backupsService.remove(deleteName.value)
    deleteOpen.value = false
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: deleteName.value })))
  } finally {
    deleting.value = false
  }
}

function openRestore(name: string) {
  restoreName.value = name
  restoreOpen.value = true
}

function when(b: BackupInfo) {
  return b.manifest ? formatDate(b.manifest.created_at, { dateStyle: 'medium', timeStyle: 'short' }) : '—'
}

onMounted(load)
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="flex h-full flex-col bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('backup.title')" :description="$t('backup.subtitle')" :icon="Archive" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20" back-link="/settings" />

    <ScrollArea class="flex-1">
      <div class="space-y-6 p-6">
        <Card>
          <CardHeader>
            <CardTitle>{{ $t('backup.createTitle') }}</CardTitle>
            <CardDescription>{{ $t('backup.createDesc') }}</CardDescription>
          </CardHeader>
          <CardContent class="space-y-4">
            <div v-if="backupRunning && job" class="space-y-2" data-testid="backup-progress">
              <div class="flex items-center gap-2 text-sm"><Loader2 class="h-4 w-4 animate-spin" />{{ $t(`backup.phase.${job.phase}`, job.phase) }}<span v-if="job.table" class="text-white/50 light:text-gray-500">· {{ job.table }}</span></div>
              <Progress :model-value="jobPercent(job)" class="h-2" />
            </div>
            <div v-else class="flex flex-wrap items-center justify-between gap-4">
              <label class="flex cursor-pointer items-center gap-2 text-sm text-white/70 light:text-gray-700">
                <Checkbox :checked="includeFiles" @update:checked="(v) => (includeFiles = v === true)" data-testid="backup-include-files" />
                {{ $t('backup.includeFiles') }}
              </label>
              <Button :disabled="busy" data-testid="backup-now" @click="backUpNow"><Archive class="mr-2 h-4 w-4" />{{ $t('backup.backUpNow') }}</Button>
            </div>
            <p v-if="dir" class="break-all font-mono text-xs text-white/40 light:text-gray-500">{{ $t('backup.savedIn', { dir }) }}</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div class="flex flex-wrap items-center justify-between gap-4">
              <div>
                <CardTitle>{{ $t('backup.listTitle') }}</CardTitle>
                <CardDescription>{{ $t('backup.listDesc') }}</CardDescription>
              </div>
              <div class="flex items-center gap-3">
                <span v-if="uploadPercent !== null" class="text-sm text-white/60 light:text-gray-600">{{ $t('backup.uploading', { percent: uploadPercent }) }}</span>
                <input ref="fileInput" type="file" accept=".zip,application/zip" class="hidden" @change="onFile" />
                <Button variant="outline" size="sm" :disabled="busy" data-testid="backup-upload" @click="fileInput?.click()"><Upload class="mr-2 h-4 w-4" />{{ $t('backup.upload') }}</Button>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            <div v-if="loading" class="flex justify-center py-10"><Loader2 class="h-6 w-6 animate-spin text-white/40 light:text-gray-400" /></div>
            <p v-else-if="backups.length === 0" class="py-10 text-center text-sm text-white/40 light:text-gray-500">{{ $t('backup.empty') }}</p>
            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>{{ $t('backup.colDate') }}</TableHead>
                  <TableHead>{{ $t('backup.colSize') }}</TableHead>
                  <TableHead>{{ $t('backup.colMadeOn') }}</TableHead>
                  <TableHead>{{ $t('backup.colFiles') }}</TableHead>
                  <TableHead class="text-right">{{ $t('common.actions') }}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="b in backups" :key="b.name" :data-testid="`backup-row-${b.name}`">
                  <TableCell>
                    <div class="font-medium">{{ when(b) }}</div>
                    <div class="flex items-center gap-1.5 font-mono text-xs text-white/40 light:text-gray-500">
                      {{ b.name }}
                      <span v-if="b.manifest?.kind === 'pre-restore'" class="inline-flex items-center gap-1 rounded bg-amber-500/10 px-1.5 py-0.5 font-sans text-[10px] font-medium uppercase text-amber-300 light:text-amber-700"><History class="h-3 w-3" />{{ $t('backup.preRestore') }}</span>
                    </div>
                    <div v-if="b.error" class="text-xs text-red-400">{{ $t('backup.unreadable', { error: b.error }) }}</div>
                  </TableCell>
                  <TableCell>{{ formatBytes(b.size) }}</TableCell>
                  <TableCell class="text-sm">{{ b.manifest ? `${b.manifest.app_version || '?'} · ${b.manifest.source_driver}` : '—' }}</TableCell>
                  <TableCell>{{ b.manifest?.include_files ? $t('backup.filesYes') : $t('backup.filesNo') }}</TableCell>
                  <TableCell>
                    <div class="flex items-center justify-end gap-1">
                      <a :href="backupsService.downloadUrl(b.name)" :download="b.name"><IconButton :icon="Download" :label="$t('backup.download')" class="h-8 w-8" /></a>
                      <IconButton v-if="b.manifest" :icon="RotateCcw" :label="$t('backup.restore')" class="h-8 w-8" :disabled="busy" @click="openRestore(b.name)" />
                      <IconButton :icon="Trash2" :label="$t('backup.delete')" variant="ghost" class="h-8 w-8 text-destructive" :disabled="busy" @click="deleteName = b.name; deleteOpen = true" />
                    </div>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>

    <RestoreBackupDialog v-model:open="restoreOpen" :name="restoreName" @finished="load" />
    <DeleteConfirmDialog v-model:open="deleteOpen" :title="$t('backup.deleteTitle')" :item-name="deleteName ?? ''" :description="$t('backup.deleteDesc')" :is-submitting="deleting" @confirm="confirmDelete" />
  </div>
</template>
```

- [ ] **Step 5: Run checks**

Run: `cd frontend && npx vue-tsc --noEmit 2>&1 | rg "error TS" | rg -i "backup|Restore" ; npx vue-tsc --noEmit 2>&1 | rg -c "error TS"; npx eslint src/views/settings/BackupView.vue src/views/settings/RestoreBackupDialog.vue src/lib/backup.ts; npx vitest run`
Expected: no errors mentioning the new files; total vue-tsc count still the baseline (20); eslint clean; vitest passes.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/views/settings/BackupView.vue frontend/src/views/settings/RestoreBackupDialog.vue frontend/src/router/index.ts frontend/src/components/layout/navigation.ts frontend/src/i18n/locales/en.json
git commit -m "feat(frontend): Settings → Backup & Restore page"
```

---

### Task 12: Docs and end-to-end check

**Files:**
- Create: `docs/src/content/docs/features/backup-restore.mdx` (match the front-matter of a sibling page in that folder; find one with `ls docs/src/content/docs/features`)
- Modify: the docs sidebar config if pages are listed explicitly (`rg -n "features/" docs/astro.config.*`)

- [ ] **Step 1: Write the docs page**

Sections, in plain English:
1. What a backup holds (all organizations; media and audio folders unless unticked; not `config.toml`, not S3 recordings) and where it is saved (`[backup] dir`).
2. Making, downloading, uploading and deleting backups from Settings → Backup & Restore (super admins only).
3. Restoring: what the dialog shows, the safety backup, maintenance mode, everyone signed out, stop separate workers.
4. Moving to another database: install whatomate on the new database (setup wizard), upload the backup in Settings → Backup & Restore, restore, sign in with your old account. Keep the same `encryption_key` (copy it from the old `config.toml`) or re-enter saved tokens and keys.
5. Command line: `whatomate backup [-no-files] [-o file]`, `whatomate restore [-yes] <file>` (stop the server first).
6. Docker: mount a volume for the backup folder.

- [ ] **Step 2: Full test run**

Run:
```bash
go test ./internal/backup ./internal/config ./internal/maintenance ./internal/websocket ./internal/worker -count=1
TEST_DB_DRIVER=sqlite go test ./internal/handlers ./internal/... -count=1
cd frontend && npx vitest run && npx eslint src && npx vue-tsc --noEmit 2>&1 | rg -c "error TS"
cd .. && make build-prod BINARY_NAME=/tmp/whatomate-bk
```
Expected: all pass; vue-tsc at the baseline; binary builds. With the cross-database env vars set, also run Task 6's test.

- [ ] **Step 3: Manual walkthrough (throwaway instance only)**

On a temp config with SQLite and a spare port (never the dev database or port 8080):
1. Create two organizations and a few contacts and messages; upload a media file.
2. Settings → Backup & Restore: back up with files; check the row, size and "made on".
3. Download it; delete it; upload the downloaded file; it reappears.
4. Change data (add an organization, delete a contact), then Restore with `RESTORE`: progress shows, the page goes to sign-in, the old data is back, the new organization is gone, a "Before restore" backup is listed.
5. SQLite → Postgres move: start a second throwaway instance on a fresh `whatomate_test_backup` Postgres database via the setup wizard, upload the backup, restore, sign in with the first instance's admin.
6. Screenshots of the page and the restore dialog in dark and light themes.

- [ ] **Step 4: Commit**

```bash
git add docs
git commit -m "docs: backup and restore"
```
