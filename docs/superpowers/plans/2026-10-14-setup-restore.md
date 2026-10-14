# Setup Wizard Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the setup wizard set up a new install straight from a backup: choose "Restore from a backup" on Welcome, pick or upload a backup, restore it into the empty database, write the config and sign in with an account from the backup.

**Architecture:** The setup server (`internal/setup`) gains backup endpoints (chunked upload, list, inspect) and a background restore job that reuses `internal/backup` (`Restore`, `Inspect`). The chunked-upload logic moves from `internal/handlers` into `internal/backup` so the app and the wizard share it. The wizard frontend gets a mode choice on Welcome, a new Restore step and restore progress on the finishing screen.

**Tech Stack:** Go 1.25 (fasthttp, gorm v1.25.7, glebarez sqlite), Vue 3 + TypeScript + shadcn-vue, vitest.

**Spec:** `docs/superpowers/specs/2026-10-14-setup-restore-design.md`

## Global Constraints

- `CGO_ENABLED=0`, Go stays at 1.25, gorm stays at v1.25.7. No new Go modules; no mattn/go-sqlite3.
- Works for SQLite, Postgres and MySQL targets (`config.DriverSQLite`, `config.DriverPostgres`, `config.DriverMySQL`) and across them.
- Wizard `POST` bodies must be `application/json`; the config file is written last with `config.WriteFileExclusive`; a database with users is refused; endpoints answer only while setup runs.
- A database that already has users is refused for restore too (`errHasUsers`), except a user setup itself created in an earlier failed attempt (`reusableAdmin`).
- Encryption key: blank → new key; non-blank and not matching the backup's fingerprint → refused; backup without a fingerprint → any key or none.
- No safety backup in the wizard. No default admin created on the restore path.
- Files restore into `<app base>/uploads` and `<app base>/audio`; uploads land in `<app base>/backups`. App base = the candidate's base folder, or the working directory when it is empty (a fixed `-config` path), matching `config.ResolvePaths`.
- Upload limit 10240 MB; chunks at most 8 MB.
- English UI strings only in `frontend/src/i18n/locales/en.json`.
- Never print secrets (DSNs, passwords, keys). Tests never touch the dev database; Postgres tests only against `TEST_BACKUP_DATABASE_URL` whose database name starts with `whatomate_test`.
- Commit locally after each task; never push.

## Review Focus

1. **Chunk size vs the setup server's body limit:** fasthttp's default `MaxRequestBodySize` is 4 MB and the frontend sends 4 MB chunks; the setup server must accept 8 MB chunks. Pinned in Task 4 (`TestRun_AcceptsLargeUploadChunk`, a real HTTP PUT).
2. **Fixed `-config` path:** its candidate base is empty, so the app resolves `backups`, `uploads` and `audio` against the working directory; the wizard must use the same folders or the uploaded backup and restored files are invisible to the app. Pinned in Task 3 (`TestAppBase`).
3. **Hand-off race:** the setup server shuts down as soon as setup finishes; the page must still read the restore's final state (including `files_failed`). Pinned in Task 5 (`TestRestore_FinalStateReadBeforeHandoff`).
4. **Retry after a failed fresh attempt:** a database holding only the admin an earlier failed "Start fresh" attempt created must be accepted for restore. Pinned in Task 5 (`TestRestore_AfterFailedFreshAttempt`).
5. **Backup made without an encryption key:** any key the operator enters is accepted and written to the config. Pinned in Task 5 (`TestRestore_BackupWithoutKeyUsesOperatorKey`).

---

### Task 1: Shared upload engine and offline inspect in `internal/backup`

**Files:**
- Create: `internal/backup/upload.go`, `internal/backup/upload_test.go`
- Modify: `internal/backup/inspect.go` (add `InspectFile`), `internal/backup/inspect_test.go` (or the existing inspect test file)
- Modify: `internal/handlers/backup_upload.go` (use `backup.Uploads`), `internal/handlers/app.go:60` (`uploads backup.Uploads`)

**Interfaces:**
- Produces:
  - `backup.ChunkMax = 8 << 20`, `backup.DefaultMaxUploadMB = 10240`
  - `type backup.Uploads struct { Space func(dir string, need int64) error; ... }` with methods `Start(dir, name string, size int64, maxMB int) (string, error)`, `Put(id string, offset int64, body []byte) (int64, error)`, `Finish(id string) (string, error)` (returns the final path), `Cancel(id string)`
  - errors `ErrUploadNotFound`, `ErrNotBackupFile`, `ErrBadChunk`; types `*OffsetError{Received}`, `*TooLargeError{LimitMB}`, `*IncompleteError{Received, Size}`, `*SpaceError{Err}`
  - `backup.InspectFile(p, targetDriver, encryptionKey string) (Summary, error)`

- [ ] **Step 1: Write the failing tests**

`internal/backup/upload_test.go`:

```go
package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUploads() *Uploads {
	return &Uploads{Space: func(string, int64) error { return nil }}
}

func TestUploads_StartPutFinish(t *testing.T) {
	dir := t.TempDir()
	u := newUploads()
	id, err := u.Start(dir, "My Backup.zip", 6, 0)
	require.NoError(t, err)

	n, err := u.Put(id, 0, []byte("abc"))
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	n, err = u.Put(id, 3, []byte("def"))
	require.NoError(t, err)
	assert.EqualValues(t, 6, n)

	p, err := u.Finish(id)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "My-Backup.zip"), p)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "abcdef", string(b))
	fi, err := os.Stat(p)
	require.NoError(t, err)
	if os.PathSeparator == '/' {
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	_, err = u.Finish(id)
	assert.ErrorIs(t, err, ErrUploadNotFound)
}

func TestUploads_Refusals(t *testing.T) {
	dir := t.TempDir()
	u := newUploads()

	_, err := u.Start(dir, "notes.txt", 5, 0)
	assert.ErrorIs(t, err, ErrNotBackupFile)
	_, err = u.Start(dir, "b.zip", 0, 0)
	assert.ErrorIs(t, err, ErrNotBackupFile)

	var big *TooLargeError
	_, err = u.Start(dir, "b.zip", 2<<20, 1)
	require.ErrorAs(t, err, &big)
	assert.Equal(t, 1, big.LimitMB)

	u.Space = func(string, int64) error { return errors.New("disk full") }
	var sp *SpaceError
	_, err = u.Start(dir, "b.zip", 5, 0)
	require.ErrorAs(t, err, &sp)
	u.Space = func(string, int64) error { return nil }

	id, err := u.Start(dir, "b.zip", 4, 0)
	require.NoError(t, err)
	var off *OffsetError
	_, err = u.Put(id, 2, []byte("ab"))
	require.ErrorAs(t, err, &off)
	assert.EqualValues(t, 0, off.Received)
	_, err = u.Put(id, 0, []byte("abcde"))
	assert.ErrorIs(t, err, ErrBadChunk)
	_, err = u.Put(id, 0, nil)
	assert.ErrorIs(t, err, ErrBadChunk)
	_, err = u.Put("nope", 0, []byte("a"))
	assert.ErrorIs(t, err, ErrUploadNotFound)

	_, err = u.Put(id, 0, []byte("ab"))
	require.NoError(t, err)
	var inc *IncompleteError
	_, err = u.Finish(id)
	require.ErrorAs(t, err, &inc)
	assert.EqualValues(t, 2, inc.Received)
	assert.EqualValues(t, 4, inc.Size)
}

func TestUploads_Cancel(t *testing.T) {
	dir := t.TempDir()
	u := newUploads()
	id, err := u.Start(dir, "b.zip", 4, 0)
	require.NoError(t, err)
	u.Cancel(id)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
	_, err = u.Put(id, 0, []byte("a"))
	assert.ErrorIs(t, err, ErrUploadNotFound)
}
```

Add to `internal/backup/inspect_test.go` (its `makeBackup(t)` helper writes a backup made with encryption key `"key-a"`):

```go
func TestInspectFile_NeedsNoDatabase(t *testing.T) {
	p, _ := makeBackup(t)
	sum, err := InspectFile(p, "postgres", "key-a")
	require.NoError(t, err)
	assert.Equal(t, "postgres", sum.TargetDriver)
	assert.True(t, sum.KeyMatches)

	sum, err = InspectFile(p, "mysql", "other")
	require.NoError(t, err)
	assert.False(t, sum.KeyMatches)
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `CGO_ENABLED=0 go test ./internal/backup -run 'TestUploads|TestInspectFile' -count=1`
Expected: FAIL (undefined: Uploads, InspectFile).

- [ ] **Step 3: Implement `internal/backup/upload.go`**

```go
package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ChunkMax is the largest upload chunk accepted.
const ChunkMax = 8 << 20

// DefaultMaxUploadMB is the upload limit when none is configured.
const DefaultMaxUploadMB = 10240

var (
	ErrUploadNotFound = errors.New("upload not found")
	ErrNotBackupFile  = errors.New("choose a whatomate backup (.zip)")
	ErrBadChunk       = errors.New("bad chunk size")
)

// OffsetError means a chunk did not start where the upload ends.
type OffsetError struct{ Received int64 }

func (e *OffsetError) Error() string { return fmt.Sprintf("expected offset %d", e.Received) }

// TooLargeError means the file is over the upload limit.
type TooLargeError struct{ LimitMB int }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("the backup is larger than the %d MB upload limit", e.LimitMB)
}

// IncompleteError means Finish was called before every byte arrived.
type IncompleteError struct{ Received, Size int64 }

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("received %d of %d bytes", e.Received, e.Size)
}

// SpaceError means the disk has no room for the upload.
type SpaceError struct{ Err error }

func (e *SpaceError) Error() string { return e.Err.Error() }
func (e *SpaceError) Unwrap() error { return e.Err }

// Uploads tracks chunked uploads of backup files.
type Uploads struct {
	// Space checks free disk space; nil means EnsureSpace.
	Space func(dir string, need int64) error

	mu    sync.Mutex
	items map[string]*upload
}

type upload struct {
	mu       sync.Mutex
	dir      string
	name     string
	path     string
	size     int64
	received int64
}

func (u *Uploads) get(id string) *upload {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.items[id]
}

func (u *Uploads) take(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.items, id)
}

// Start creates an empty partial file in dir for a backup of size bytes.
// maxMB <= 0 means DefaultMaxUploadMB.
func (u *Uploads) Start(dir, name string, size int64, maxMB int) (string, error) {
	if !strings.HasSuffix(strings.ToLower(name), ".zip") || size <= 0 {
		return "", ErrNotBackupFile
	}
	if maxMB <= 0 {
		maxMB = DefaultMaxUploadMB
	}
	if size > int64(maxMB)<<20 {
		return "", &TooLargeError{LimitMB: maxMB}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	space := u.Space
	if space == nil {
		space = EnsureSpace
	}
	if err := space(dir, size); err != nil {
		return "", &SpaceError{Err: err}
	}
	id := uuid.NewString()
	p := filepath.Join(dir, UploadPrefix+id+".part")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	u.mu.Lock()
	if u.items == nil {
		u.items = map[string]*upload{}
	}
	u.items[id] = &upload{dir: dir, name: name, path: p, size: size}
	u.mu.Unlock()
	return id, nil
}

// Put appends body at offset and returns the bytes received so far.
func (u *Uploads) Put(id string, offset int64, body []byte) (int64, error) {
	up := u.get(id)
	if up == nil {
		return 0, ErrUploadNotFound
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if offset != up.received {
		return up.received, &OffsetError{Received: up.received}
	}
	if len(body) == 0 || len(body) > ChunkMax || up.received+int64(len(body)) > up.size {
		return up.received, ErrBadChunk
	}
	f, err := os.OpenFile(up.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return up.received, err
	}
	n, err := f.Write(body)
	_ = f.Close()
	up.received += int64(n)
	return up.received, err
}

// Finish renames a complete upload to a free name in its folder and returns
// the new path. The caller checks the file and removes it if it is no backup.
func (u *Uploads) Finish(id string) (string, error) {
	up := u.get(id)
	if up == nil {
		return "", ErrUploadNotFound
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.received != up.size {
		return "", &IncompleteError{Received: up.received, Size: up.size}
	}
	u.take(id)
	final := filepath.Join(up.dir, UploadName(up.dir, up.name))
	if err := os.Rename(up.path, final); err != nil {
		_ = os.Remove(up.path)
		return "", err
	}
	return final, nil
}

// Cancel drops a partial upload.
func (u *Uploads) Cancel(id string) {
	up := u.get(id)
	if up == nil {
		return
	}
	up.mu.Lock()
	_ = os.Remove(up.path)
	up.mu.Unlock()
	u.take(id)
}
```

Add to `internal/backup/inspect.go` (imports `github.com/glebarez/sqlite` and `gorm.io/gorm/logger`):

```go
// InspectFile is Inspect without a live database, for a restore target of
// targetDriver that may not exist yet. Table and column names do not depend
// on the driver.
func InspectFile(p, targetDriver, encryptionKey string) (Summary, error) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return Summary{}, err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	sum, err := Inspect(p, db, encryptionKey)
	if err != nil {
		return Summary{}, err
	}
	sum.TargetDriver = targetDriver
	return sum, nil
}
```

- [ ] **Step 4: Run the new tests**

Run: `CGO_ENABLED=0 go test ./internal/backup -run 'TestUploads|TestInspectFile' -count=1`
Expected: PASS.

- [ ] **Step 5: Switch the app handlers to `backup.Uploads`**

In `internal/handlers/app.go` change the field to `uploads backup.Uploads` and, where the App is built (or lazily in the handlers), set `Space` so the test hook keeps working:

```go
a.uploads.Space = func(dir string, need int64) error { return ensureSpace(dir, need) }
```

(If the App has no constructor that runs before handlers, set it at the top of `StartBackupUpload` instead: `if a.uploads.Space == nil { a.uploads.Space = func(d string, n int64) error { return ensureSpace(d, n) } }`.)

Rewrite `internal/handlers/backup_upload.go` so every handler keeps its exact status codes and messages:

```go
package handlers

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func sendUploadError(r *fastglue.Request, err error) error {
	var off *backup.OffsetError
	var big *backup.TooLargeError
	var inc *backup.IncompleteError
	var sp *backup.SpaceError
	switch {
	case errors.Is(err, backup.ErrUploadNotFound):
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Upload not found", nil, "")
	case errors.Is(err, backup.ErrNotBackupFile):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose a whatomate backup (.zip)", nil, "")
	case errors.Is(err, backup.ErrBadChunk):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Bad chunk size", nil, "")
	case errors.As(err, &off):
		return r.SendErrorEnvelope(fasthttp.StatusConflict, fmt.Sprintf("Expected offset %d", off.Received), map[string]int64{"received": off.Received}, "")
	case errors.As(err, &big):
		return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge,
			fmt.Sprintf("The backup is larger than the %d MB upload limit ([backup] max_upload_mb)", big.LimitMB), nil, "")
	case errors.As(err, &inc):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, fmt.Sprintf("Received %d of %d bytes", inc.Received, inc.Size), nil, "")
	case errors.As(err, &sp):
		return r.SendErrorEnvelope(fasthttp.StatusInsufficientStorage, sp.Err.Error(), nil, "")
	}
	return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
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
	id, err := a.uploads.Start(a.Config.Backup.Dir, req.Name, req.Size, a.Config.Backup.MaxUploadMB)
	if err != nil {
		return sendUploadError(r, err)
	}
	return r.SendEnvelope(map[string]any{"id": id, "received": 0, "chunk_max": backup.ChunkMax})
}

// PutBackupUploadChunk appends one chunk at ?offset=N.
func (a *App) PutBackupUploadChunk(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	offset, err := strconv.ParseInt(string(r.RequestCtx.QueryArgs().Peek("offset")), 10, 64)
	if err != nil {
		offset = -1
	}
	n, err := a.uploads.Put(id, offset, r.RequestCtx.PostBody())
	if err != nil {
		return sendUploadError(r, err)
	}
	return r.SendEnvelope(map[string]int64{"received": n})
}

// FinishBackupUpload checks the uploaded file and adds it to the list.
func (a *App) FinishBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	final, err := a.uploads.Finish(id)
	if err != nil {
		return sendUploadError(r, err)
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
	a.uploads.Cancel(id)
	return r.SendEnvelope(map[string]string{"cancelled": id})
}
```

Replace any other use of the removed `uploadChunkMax` / `defaultMaxUploadMB` in `internal/handlers` with `backup.ChunkMax` / `backup.DefaultMaxUploadMB` (`rg -n "uploadChunkMax|defaultMaxUploadMB|uploadStore|backupUpload" internal/handlers`).

- [ ] **Step 6: Run the backup and handler tests**

Run: `CGO_ENABLED=0 go test ./internal/backup -count=1 && TEST_DB_DRIVER=sqlite CGO_ENABLED=0 go test ./internal/handlers -run 'Backup|Upload|Maintenance' -count=1`
Expected: PASS (existing upload handler tests unchanged and green).

- [ ] **Step 7: Commit**

```bash
git add internal/backup/upload.go internal/backup/upload_test.go internal/backup/inspect.go internal/backup/inspect_test.go internal/handlers/backup_upload.go internal/handlers/app.go
git commit -m "refactor(backup): share the chunked upload engine; inspect without a database"
```

---

### Task 2: Migrate without creating the default admin

**Files:**
- Modify: `internal/database/database.go:175-180` (`RunMigrationWithProgress`)
- Test: `internal/database/migrate_noadmin_test.go`

**Interfaces:**
- Produces: `database.RunMigrationWithProgress(db, nil)` creates every table, index and seed (roles, permissions) but no organization or user.

- [ ] **Step 1: Write the failing test**

`internal/database/migrate_noadmin_test.go`:

```go
package database

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/models"
)

func TestRunMigrationWithProgress_NilAdminCreatesNoUser(t *testing.T) {
	db, err := Open(&config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(t.TempDir(), "w.db")}, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	require.NoError(t, RunMigrationWithProgress(db, nil))

	var users, orgs int64
	require.NoError(t, db.Model(&models.User{}).Count(&users).Error)
	require.NoError(t, db.Model(&models.Organization{}).Count(&orgs).Error)
	assert.Zero(t, users)
	assert.Zero(t, orgs)
	for _, m := range GetMigrationModels() {
		assert.True(t, db.Migrator().HasTable(m.Model), m.Name)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `CGO_ENABLED=0 go test ./internal/database -run TestRunMigrationWithProgress_NilAdminCreatesNoUser -count=1`
Expected: FAIL (nil pointer dereference in `CreateDefaultAdmin`).

- [ ] **Step 3: Skip the admin when none is given**

In `RunMigrationWithProgress`, replace the admin block with:

```go
	// Create the first admin (only when there are no users). A nil adminCfg
	// skips it: a restore brings its own users.
	printProgress(currentStep, totalSteps)
	if adminCfg != nil {
		if err := CreateDefaultAdmin(silentDB, adminCfg); err != nil {
			fmt.Printf("\n  \033[31m✗ Setup failed\033[0m\n\n")
			return err
		}
	}
	currentStep++
```

- [ ] **Step 4: Run the database tests**

Run: `CGO_ENABLED=0 go test ./internal/database -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/database/database.go internal/database/migrate_noadmin_test.go
git commit -m "feat(database): migrate without creating the default admin"
```

---

### Task 3: Split the setup finish into reusable target checks and config writing

**Files:**
- Modify: `internal/setup/complete.go`
- Create: `internal/setup/target.go`, `internal/setup/target_test.go`

**Interfaces:**
- Produces (package `setup`, unexported):
  - `type targetRequest struct { ConfigPath string \`json:"config_path"\`; Port int \`json:"port"\`; Database databaseInput \`json:"database"\` }`
  - `type target struct { cand candidateInfo; port int; db config.DatabaseConfig }`
  - `func (s *server) checkTarget(req targetRequest) (target, int, error)`
  - `func openTarget(c config.DatabaseConfig) (*gorm.DB, func(), int, error)`
  - `func (s *server) writeConfig(tg target, origin string, https bool, encryptionKey string) (int, error)`
  - `func appBase(cand candidateInfo) string`, `func backupDir(cand candidateInfo) string`, `func mediaDir(cand candidateInfo) string`, `func audioDir(cand candidateInfo) string`
  - `func (s *server) finishSetup(f finished)`
  - `completeRequest` embeds `targetRequest` (JSON shape unchanged)

- [ ] **Step 1: Write the failing test (Review Focus 2)**

`internal/setup/target_test.go`:

```go
package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppBase(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	fixed := candidateInfo{Path: "/srv/w/config.toml", DataDir: "/srv/w", Scope: "fixed"}
	assert.Equal(t, cwd, appBase(fixed), "a fixed -config path resolves relative paths against the working directory")
	assert.Equal(t, filepath.Join(cwd, "backups"), backupDir(fixed))

	user := candidateInfo{Path: "/home/a/.config/whatomate/config.toml", DataDir: "/home/a/.local/share/whatomate", Scope: "user", base: "/home/a/.local/share/whatomate"}
	assert.Equal(t, "/home/a/.local/share/whatomate", appBase(user))
	assert.Equal(t, filepath.Join("/home/a/.local/share/whatomate", "uploads"), mediaDir(user))
	assert.Equal(t, filepath.Join("/home/a/.local/share/whatomate", "audio"), audioDir(user))
	assert.Equal(t, filepath.Join("/home/a/.local/share/whatomate", "backups"), backupDir(user))
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `CGO_ENABLED=0 go test ./internal/setup -run TestAppBase -count=1`
Expected: FAIL (undefined: appBase).

- [ ] **Step 3: Create `internal/setup/target.go`**

```go
package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// targetRequest is where a finish writes: the config location, the app port
// and the database.
type targetRequest struct {
	ConfigPath string        `json:"config_path"`
	Port       int           `json:"port"`
	Database   databaseInput `json:"database"`
}

type target struct {
	cand candidateInfo
	port int
	db   config.DatabaseConfig
}

// checkTarget validates req and returns the HTTP status to use with an error.
func (s *server) checkTarget(req targetRequest) (target, int, error) {
	cand, ok := s.lookup(req.ConfigPath)
	if !ok {
		return target{}, fasthttp.StatusBadRequest, errors.New("unknown config location")
	}
	if req.Port < 1 || req.Port > 65535 {
		return target{}, fasthttp.StatusBadRequest, errors.New("port must be between 1 and 65535")
	}
	if req.Port != s.listenPort && !s.o.Checker.PortFree(req.Port) {
		return target{}, fasthttp.StatusBadRequest, fmt.Errorf("port %d is in use by another program", req.Port)
	}
	dbCfg := req.Database.config()
	if err := validateDatabase(dbCfg); err != nil {
		return target{}, fasthttp.StatusBadRequest, err
	}
	// A directory counts too: the config file could never be written there.
	if _, err := os.Lstat(cand.Path); err == nil {
		return target{}, fasthttp.StatusConflict, configExists(cand.Path)
	}
	return target{cand: cand, port: req.Port, db: dbCfg}, 0, nil
}

// openTarget connects to the database, creating the SQLite folder first.
func openTarget(c config.DatabaseConfig) (*gorm.DB, func(), int, error) {
	if c.Driver == config.DriverSQLite {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o750); err != nil {
			return nil, func() {}, fasthttp.StatusInternalServerError, fmt.Errorf("create %s: %w", filepath.Dir(c.Path), err)
		}
	}
	db, err := database.Open(&c, false)
	if err != nil {
		return nil, func() {}, fasthttp.StatusBadRequest, fmt.Errorf("connect to the database: %w", err)
	}
	return db, func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}, 0, nil
}

// writeConfig writes the config file for tg. A blank encryptionKey gets a
// new one.
func (s *server) writeConfig(tg target, origin string, https bool, encryptionKey string) (int, error) {
	jwtSecret, err := newSecret(48)
	if err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	encKey := encryptionKey
	if encKey == "" {
		if encKey, err = newSecret(32); err != nil {
			return fasthttp.StatusInternalServerError, err
		}
	}
	stored := tg.db
	if stored.Driver == config.DriverSQLite {
		stored.Path = storedDBPath(tg.db.Path, tg.cand.base)
	}
	if err := os.MkdirAll(tg.cand.DataDir, 0o750); err != nil {
		return fasthttp.StatusInternalServerError, fmt.Errorf("create %s: %w", tg.cand.DataDir, err)
	}
	body := Render(Values{
		Environment: environmentFor(https), Port: tg.port, AllowedOrigins: allowedOrigins(origin, https, s.urls(tg.port)),
		Database: stored, JWTSecret: jwtSecret, EncryptionKey: encKey,
	}, time.Now())
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	if err := config.WriteFileExclusive(tg.cand.Path, body, 0o600); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fasthttp.StatusConflict, configExists(tg.cand.Path)
		}
		return fasthttp.StatusInternalServerError, fmt.Errorf("write %s: %w", tg.cand.Path, err)
	}
	s.o.Log.Info("Setup complete", "config", tg.cand.Path)
	return fasthttp.StatusOK, nil
}

// finishSetup records f and hands over to the app.
func (s *server) finishSetup(f finished) {
	s.stateMu.Lock()
	s.finished = &f
	s.stateMu.Unlock()
	s.done <- f
}

// appBase is where the app resolves relative paths for this config
// location: its base folder, or the working directory for a fixed path
// (see config.ResolvePaths).
func appBase(cand candidateInfo) string {
	if cand.base != "" {
		return cand.base
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func backupDir(cand candidateInfo) string { return filepath.Join(appBase(cand), "backups") }
func mediaDir(cand candidateInfo) string  { return filepath.Join(appBase(cand), "uploads") }
func audioDir(cand candidateInfo) string  { return filepath.Join(appBase(cand), "audio") }
```

Check `config.go` defaults first (`rg -n '"./uploads"|"./audio"|"backups"' internal/config/config.go`); if a default differs from `uploads` / `audio` / `backups`, use the default's folder name.

- [ ] **Step 4: Rewrite `finish` and `complete` in `internal/setup/complete.go` on top of it**

Replace the `completeRequest` type with:

```go
type completeRequest struct {
	targetRequest
	Organization string `json:"organization"`
	Admin        struct {
		FullName string `json:"full_name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	} `json:"admin"`
}
```

Replace `finish` with:

```go
// finish validates req, prepares the database and admin, and writes the
// config file last, so any failure leaves no config behind. It returns the
// HTTP status to use with the error.
func (s *server) finish(req completeRequest, origin string, https bool) (int, error) {
	tg, code, err := s.checkTarget(req.targetRequest)
	if err != nil {
		return code, err
	}
	org := strings.TrimSpace(req.Organization)
	if org == "" || len(org) > 100 {
		return fasthttp.StatusBadRequest, errors.New("organization name is required (up to 100 characters)")
	}
	fullName := strings.TrimSpace(req.Admin.FullName)
	if fullName == "" {
		return fasthttp.StatusBadRequest, errors.New("full name is required")
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(req.Admin.Email))
	if err != nil {
		return fasthttp.StatusBadRequest, errors.New("enter a valid email address")
	}
	if len(req.Admin.Password) < 8 {
		return fasthttp.StatusBadRequest, errors.New("password must be at least 8 characters")
	}
	if len(req.Admin.Password) > 72 {
		return fasthttp.StatusBadRequest, errors.New("password must be at most 72 bytes")
	}

	db, closeDB, code, err := openTarget(tg.db)
	if err != nil {
		return code, err
	}
	defer closeDB()
	dbTarget := databaseTarget(tg.db)
	adminID, err := s.reusableAdmin(db, dbTarget)
	if errors.Is(err, errHasUsers) {
		return fasthttp.StatusConflict, err
	}
	if err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	admin := config.DefaultAdminConfig{Email: addr.Address, Password: req.Admin.Password, FullName: fullName, OrganizationName: org}
	migrateErr := s.migrate(db, &admin)
	if adminID == uuid.Nil {
		// The database had no users, so a user with this email is the one this
		// attempt created. Recording it even when a later step failed lets a
		// retry reuse this database.
		var user models.User
		if err := db.Where("email = ?", admin.Email).First(&user).Error; err == nil {
			adminID = user.ID
		} else if migrateErr == nil {
			return fasthttp.StatusInternalServerError, fmt.Errorf("find admin user: %w", err)
		}
	}
	if adminID != uuid.Nil {
		s.seeded = &seededAdmin{target: dbTarget, userID: adminID}
	}
	if migrateErr != nil {
		return fasthttp.StatusInternalServerError, fmt.Errorf("set up the database: %w", migrateErr)
	}
	if err := applyAdmin(db, adminID, admin); err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	return s.writeConfig(tg, origin, https, "")
}
```

In `server.go`'s `complete`, replace the four lines that set `s.finished` and send on `s.done` with `s.finishSetup(f)` (keep `writeData` after it). Remove imports that are now unused from `complete.go` (`io/fs`, `os`, `path/filepath`, `time` if unused — let `goimports`/the compiler tell you).

- [ ] **Step 5: Run the setup tests**

Run: `CGO_ENABLED=0 go test ./internal/setup -count=1`
Expected: PASS (all existing complete/status tests unchanged, plus `TestAppBase`).

- [ ] **Step 6: Commit**

```bash
git add internal/setup/target.go internal/setup/target_test.go internal/setup/complete.go internal/setup/server.go
git commit -m "refactor(setup): reusable target checks and config writing"
```

---

### Task 4: Setup endpoints for backup uploads, the backup list and inspect

**Files:**
- Create: `internal/setup/backups.go`, `internal/setup/backups_test.go`
- Modify: `internal/setup/server.go` (routes, `uploads backup.Uploads` field, fasthttp server limits)
- Test: `internal/setup/run_test.go` (large chunk over real HTTP)

**Interfaces:**
- Consumes: `backup.Uploads`, `backup.InspectFile`, `backup.List`, `backup.ValidName` (Task 1); `checkTarget`, `backupDir` (Task 3).
- Produces:
  - routes `POST /api/setup/backup-uploads`, `PUT|DELETE /api/setup/backup-uploads/{id}`, `POST /api/setup/backup-uploads/{id}/finish`, `GET /api/setup/backups?config_path=`, `POST /api/setup/backup/inspect`
  - `type backupRef struct { Name string \`json:"name,omitempty"\`; Path string \`json:"path,omitempty"\` }`
  - `func (s *server) resolveBackup(cand candidateInfo, ref backupRef) (string, error)`
  - `func isUploadChunk(method, path []byte) bool`
  - inspect answer `{name, size, manifest, target_driver, key_matches, key_given}`

- [ ] **Step 1: Write the failing tests**

`internal/setup/backups_test.go`:

```go
package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/valyala/fasthttp"
)

const oldKey = "old-encryption-key-0123456789abcdef"

// makeBackup writes a backup of a fresh database with one admin
// (old@example.com / old-password-1) and, with files, one media file. It
// returns the backup's path.
func makeBackup(t *testing.T, dir, key string, withFiles bool) string {
	t.Helper()
	src := openSQLite(t, filepath.Join(dir, "src", "src.db"))
	require.NoError(t, database.RunMigrationWithProgress(src, &config.DefaultAdminConfig{
		Email: "old@example.com", Password: "old-password-1", FullName: "Old Admin", OrganizationName: "Old Org",
	}))
	media := filepath.Join(dir, "src", "media")
	require.NoError(t, os.MkdirAll(media, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(media, "photo.jpg"), []byte("jpeg"), 0o600))
	info, err := backup.Create(context.Background(), src, filepath.Join(dir, "made"), backup.Options{
		Kind: backup.KindManual, AppVersion: "test", EncryptionKey: key, IncludeFiles: withFiles,
		Areas: backup.Areas(media, ""),
	}, nil)
	require.NoError(t, err)
	return filepath.Join(dir, "made", info.Name)
}

func putChunk(t *testing.T, s *server, id string, offset int, body []byte) (int, map[string]any) {
	t.Helper()
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod("PUT")
	ctx.Request.SetRequestURI("/api/setup/backup-uploads/" + id + "?offset=" + itoa(offset))
	ctx.Request.Header.SetHost("localhost:4747")
	ctx.Request.Header.SetContentType("application/octet-stream")
	ctx.Request.SetBody(body)
	s.handle(&ctx)
	var out map[string]any
	_ = jsonUnmarshal(ctx.Response.Body(), &out)
	return ctx.Response.StatusCode(), out
}

func TestSetupBackups_UploadListInspect(t *testing.T) {
	e := newEnv(t)
	src := makeBackup(t, e.dir, oldKey, true)
	b, err := os.ReadFile(src)
	require.NoError(t, err)

	code, out := call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": e.configPath, "name": "moved.zip", "size": len(b)})
	require.Equal(t, 200, code, out)
	id := data(out)["id"].(string)

	half := len(b) / 2
	code, out = putChunk(t, e.s, id, 0, b[:half])
	require.Equal(t, 200, code, out)
	code, _ = putChunk(t, e.s, id, 0, b[half:])
	assert.Equal(t, 409, code, "a chunk at the wrong offset is refused")
	code, out = putChunk(t, e.s, id, half, b[half:])
	require.Equal(t, 200, code, out)

	code, out = call(t, e.s, "POST", "/api/setup/backup-uploads/"+id+"/finish", map[string]any{})
	require.Equal(t, 200, code, out)
	assert.Equal(t, "moved.zip", data(out)["name"])
	cand, _ := e.s.lookup(e.configPath)
	assert.FileExists(t, filepath.Join(backupDir(cand), "moved.zip"))

	code, out = call(t, e.s, "GET", "/api/setup/backups?config_path="+e.configPath, nil)
	require.Equal(t, 200, code, out)
	list := data(out)["backups"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "moved.zip", list[0].(map[string]any)["name"])

	inspect := func(ref map[string]any, key string) (int, map[string]any) {
		return call(t, e.s, "POST", "/api/setup/backup/inspect", map[string]any{
			"config_path": e.configPath, "database": map[string]any{"driver": "postgres"}, "backup": ref, "encryption_key": key,
		})
	}
	code, out = inspect(map[string]any{"name": "moved.zip"}, "")
	require.Equal(t, 200, code, out)
	assert.Equal(t, "postgres", data(out)["target_driver"])
	assert.Equal(t, false, data(out)["key_given"])
	code, out = inspect(map[string]any{"name": "moved.zip"}, oldKey)
	require.Equal(t, 200, code, out)
	assert.Equal(t, true, data(out)["key_matches"])
	code, out = inspect(map[string]any{"path": src}, "wrong")
	require.Equal(t, 200, code, out)
	assert.Equal(t, false, data(out)["key_matches"])
	assert.Equal(t, true, data(out)["key_given"])
}

func TestSetupBackups_BadReferences(t *testing.T) {
	e := newEnv(t)
	for _, ref := range []map[string]any{
		{"name": "../x.zip"},
		{"name": "x.txt"},
		{"path": "relative/x.zip"},
		{"path": filepath.Join(e.dir, "x.txt")},
		{"path": filepath.Join(e.dir, "missing.zip")},
		{},
		{"name": "a.zip", "path": filepath.Join(e.dir, "a.zip")},
	} {
		code, _ := call(t, e.s, "POST", "/api/setup/backup/inspect", map[string]any{
			"config_path": e.configPath, "database": map[string]any{"driver": "sqlite"}, "backup": ref,
		})
		assert.Equal(t, 400, code, ref)
	}
}

func TestSetupBackups_UploadCancelAndLimits(t *testing.T) {
	e := newEnv(t)
	code, _ := call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": e.configPath, "name": "x.txt", "size": 5})
	assert.Equal(t, 400, code)
	code, _ = call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": "/nowhere/config.toml", "name": "x.zip", "size": 5})
	assert.Equal(t, 400, code)

	code, out := call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": e.configPath, "name": "x.zip", "size": 5})
	require.Equal(t, 200, code, out)
	id := data(out)["id"].(string)
	code, _ = call(t, e.s, "DELETE", "/api/setup/backup-uploads/"+id, nil)
	assert.Equal(t, 200, code)
	cand, _ := e.s.lookup(e.configPath)
	entries, _ := os.ReadDir(backupDir(cand))
	assert.Empty(t, entries)

	code, _ = call(t, e.s, "POST", "/api/setup/backup-uploads/"+id+"/finish", map[string]any{})
	assert.Equal(t, 404, code)
}

func TestIsUploadChunk(t *testing.T) {
	assert.True(t, isUploadChunk([]byte("PUT"), []byte("/api/setup/backup-uploads/abc")))
	assert.False(t, isUploadChunk([]byte("POST"), []byte("/api/setup/backup-uploads/abc/finish")))
	assert.False(t, isUploadChunk([]byte("PUT"), []byte("/api/setup/complete")))
}
```

Add small helpers at the bottom of `backups_test.go` (or reuse existing ones if the package already has them):

```go
func itoa(n int) string { return strconv.Itoa(n) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
```

(with `encoding/json` and `strconv` imported).

Add to `internal/setup/run_test.go` (Review Focus 1) a test that starts `Run` with the file's `startRun` helper, then sends a real HTTP PUT of an 8 MB chunk:

```go
func TestRun_AcceptsLargeUploadChunk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	st := startRun(t, ctx, dir)
	configPath := filepath.Join(dir, "user", "config.toml")
	body := map[string]any{"config_path": configPath, "name": "big.zip", "size": 8 << 20}
	resp := postJSON(t, st.url+"/api/setup/backup-uploads", body)
	require.Equal(t, 200, resp.StatusCode)
	var out struct{ Data struct{ ID string } }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	_ = resp.Body.Close()

	req, err := http.NewRequest("PUT", st.url+"/api/setup/backup-uploads/"+out.Data.ID+"?offset=0", bytes.NewReader(make([]byte, 8<<20)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/octet-stream")
	put, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = put.Body.Close()
	assert.Equal(t, 200, put.StatusCode)
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `CGO_ENABLED=0 go test ./internal/setup -run 'TestSetupBackups|TestIsUploadChunk|TestRun_AcceptsLargeUploadChunk' -count=1`
Expected: FAIL (404 / undefined: isUploadChunk).

- [ ] **Step 3: Implement `internal/setup/backups.go`**

```go
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
)

const uploadsPath = "/api/setup/backup-uploads"

// backupRef names a backup: a file in the backup folder, or a full path.
type backupRef struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// resolveBackup returns the backup file ref points at.
func (s *server) resolveBackup(cand candidateInfo, ref backupRef) (string, error) {
	var p string
	switch {
	case ref.Name != "" && ref.Path == "":
		if !backup.ValidName(ref.Name) {
			return "", errors.New("invalid backup name")
		}
		p = filepath.Join(backupDir(cand), ref.Name)
	case ref.Path != "" && ref.Name == "":
		p = filepath.Clean(strings.TrimSpace(ref.Path))
		if !filepath.IsAbs(p) || !strings.EqualFold(filepath.Ext(p), ".zip") {
			return "", errors.New("enter the full path to a .zip backup")
		}
	default:
		return "", errors.New("choose a backup")
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("no backup at %s", p)
	}
	return p, nil
}

func isUploadChunk(method, path []byte) bool {
	return string(method) == fasthttp.MethodPut && strings.HasPrefix(string(path), uploadsPath+"/")
}

func uploadError(ctx *fasthttp.RequestCtx, err error) {
	var off *backup.OffsetError
	var big *backup.TooLargeError
	var inc *backup.IncompleteError
	var sp *backup.SpaceError
	switch {
	case errors.Is(err, backup.ErrUploadNotFound):
		writeError(ctx, fasthttp.StatusNotFound, "Upload not found")
	case errors.Is(err, backup.ErrNotBackupFile):
		writeError(ctx, fasthttp.StatusBadRequest, "Choose a whatomate backup (.zip)")
	case errors.Is(err, backup.ErrBadChunk):
		writeError(ctx, fasthttp.StatusBadRequest, "Bad chunk size")
	case errors.As(err, &off):
		writeError(ctx, fasthttp.StatusConflict, fmt.Sprintf("Expected offset %d", off.Received))
	case errors.As(err, &big):
		writeError(ctx, fasthttp.StatusRequestEntityTooLarge, fmt.Sprintf("The backup is larger than the %d MB upload limit", big.LimitMB))
	case errors.As(err, &inc):
		writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Received %d of %d bytes", inc.Received, inc.Size))
	case errors.As(err, &sp):
		writeError(ctx, fasthttp.StatusInsufficientStorage, sp.Err.Error())
	default:
		writeError(ctx, fasthttp.StatusInternalServerError, err.Error())
	}
}

func (s *server) startUpload(ctx *fasthttp.RequestCtx) {
	var req struct {
		ConfigPath string `json:"config_path"`
		Name       string `json:"name"`
		Size       int64  `json:"size"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	cand, ok := s.lookup(req.ConfigPath)
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	id, err := s.uploads.Start(backupDir(cand), req.Name, req.Size, backup.DefaultMaxUploadMB)
	if err != nil {
		uploadError(ctx, err)
		return
	}
	writeData(ctx, map[string]any{"id": id, "received": 0, "chunk_max": backup.ChunkMax})
}

// uploadItem serves PUT and DELETE /backup-uploads/{id} and POST .../finish.
func (s *server) uploadItem(ctx *fasthttp.RequestCtx, rest string) {
	id, action, _ := strings.Cut(rest, "/")
	switch {
	case action == "" && ctx.IsPut():
		offset, err := strconv.ParseInt(string(ctx.QueryArgs().Peek("offset")), 10, 64)
		if err != nil {
			offset = -1
		}
		n, err := s.uploads.Put(id, offset, ctx.PostBody())
		if err != nil {
			uploadError(ctx, err)
			return
		}
		writeData(ctx, map[string]int64{"received": n})
	case action == "" && ctx.IsDelete():
		s.uploads.Cancel(id)
		writeData(ctx, map[string]string{"cancelled": id})
	case action == "finish" && ctx.IsPost():
		final, err := s.uploads.Finish(id)
		if err != nil {
			uploadError(ctx, err)
			return
		}
		sum, err := backup.InspectFile(final, "", "")
		if err != nil {
			_ = os.Remove(final)
			writeError(ctx, fasthttp.StatusUnprocessableEntity, err.Error())
			return
		}
		m := sum.Manifest
		writeData(ctx, backup.Info{Name: sum.Name, Size: sum.Size, Manifest: &m})
	default:
		writeError(ctx, fasthttp.StatusNotFound, "not found")
	}
}

func (s *server) listBackups(ctx *fasthttp.RequestCtx) {
	cand, ok := s.lookup(string(ctx.QueryArgs().Peek("config_path")))
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	dir := backupDir(cand)
	list, err := backup.List(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []backup.Info{}
	}
	writeData(ctx, map[string]any{"dir": dir, "backups": list})
}

type inspectRequest struct {
	ConfigPath    string        `json:"config_path"`
	Database      databaseInput `json:"database"`
	Backup        backupRef     `json:"backup"`
	EncryptionKey string        `json:"encryption_key"`
}

type inspectResult struct {
	backup.Summary
	KeyGiven bool `json:"key_given"`
}

func (s *server) inspectBackup(ctx *fasthttp.RequestCtx) {
	var req inspectRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	cand, ok := s.lookup(req.ConfigPath)
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	p, err := s.resolveBackup(cand, req.Backup)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(req.EncryptionKey)
	sum, err := backup.InspectFile(p, req.Database.config().Driver, key)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnprocessableEntity, err.Error())
		return
	}
	writeData(ctx, inspectResult{Summary: sum, KeyGiven: key != ""})
}
```

Check that `backup.List` returns an error wrapping `os.ErrNotExist` (or nil) for a missing folder; if it returns `nil, nil`, the `errors.Is` branch is simply unused.

- [ ] **Step 4: Wire the routes and server limits in `internal/setup/server.go`**

Add the field `uploads backup.Uploads` to `server` (import `github.com/trysoft/whatomate/internal/backup`). In `handle`, before the `strings.HasPrefix(path, "/api/")` case, add:

```go
	case path == "/api/setup/backups" && ctx.IsGet():
		s.listBackups(ctx)
	case path == uploadsPath && ctx.IsPost():
		s.startUpload(ctx)
	case strings.HasPrefix(path, uploadsPath+"/"):
		s.uploadItem(ctx, strings.TrimPrefix(path, uploadsPath+"/"))
	case path == "/api/setup/backup/inspect" && ctx.IsPost():
		s.inspectBackup(ctx)
```

In `Run`, build the fasthttp server with room for 8 MB chunks and time for slow links:

```go
	srv := &fasthttp.Server{
		Handler: s.handle, Name: "Whatomate setup",
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		// Upload chunks are up to backup.ChunkMax; the default limit is 4 MB.
		MaxRequestBodySize: 16 << 20,
		HeaderReceived: func(h *fasthttp.RequestHeader) fasthttp.RequestConfig {
			if isUploadChunk(h.Method(), h.RequestURI()) {
				return fasthttp.RequestConfig{ReadTimeout: 5 * time.Minute}
			}
			return fasthttp.RequestConfig{}
		},
	}
```

`h.RequestURI()` includes the query string; `isUploadChunk` only checks the prefix, so that is fine.

- [ ] **Step 5: Run the setup tests**

Run: `CGO_ENABLED=0 go test ./internal/setup -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/setup/backups.go internal/setup/backups_test.go internal/setup/server.go internal/setup/run_test.go
git commit -m "feat(setup): upload, list and inspect backups in the setup wizard"
```

---

### Task 5: The restore job

**Files:**
- Create: `internal/setup/restore.go`, `internal/setup/restore_test.go`
- Modify: `internal/setup/server.go` (routes, fields, `complete` refuses while a restore runs)

**Interfaces:**
- Consumes: `checkTarget`, `openTarget`, `writeConfig`, `finishSetup`, `mediaDir`, `audioDir`, `backupDir` (Task 3); `resolveBackup`, `backupRef` (Task 4); `database.RunMigrationWithProgress(db, nil)` (Task 2); `backup.Restore`, `backup.InspectFile`, `backup.ErrFiles`, `backup.Areas`, `backup.ProtectedPaths`.
- Produces:
  - `POST /api/setup/restore` (JSON `{config_path, port, database, backup, encryption_key}` → `{started: true}`)
  - `GET /api/setup/restore` → `restoreState`
  - `type restoreState struct { Running, Finished, Failed, FilesFailed bool; Phase, Table, Error string; Done, Total int64 }` with JSON names `running, phase, table, done, total, finished, failed, files_failed, error`
  - phases `"prepare"` and `"config"` in addition to the backup package's `verify`, `load`, `links`, `swap`

- [ ] **Step 1: Write the failing tests**

`internal/setup/restore_test.go`:

```go
package setup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func restoreBody(e env, ref map[string]any, key string) map[string]any {
	return map[string]any{
		"config_path":    e.configPath,
		"port":           4747,
		"database":       map[string]any{"driver": "sqlite", "path": e.dbPath},
		"backup":         ref,
		"encryption_key": key,
	}
}

// waitRestore polls the job until it finishes and reads the final state
// the way the page does.
func waitRestore(t *testing.T, e env) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		code, out := call(t, e.s, "GET", "/api/setup/restore", nil)
		require.Equal(t, 200, code, out)
		if data(out)["finished"] == true {
			return data(out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("restore did not finish")
	return nil
}

func copyIntoBackups(t *testing.T, e env, src, name string) {
	t.Helper()
	cand, _ := e.s.lookup(e.configPath)
	require.NoError(t, os.MkdirAll(backupDir(cand), 0o750))
	b, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(backupDir(cand), name), b, 0o600))
}

func TestRestore_FromBackupInFolderWithKey(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = time.Second
	copyIntoBackups(t, e, makeBackup(t, e.dir, oldKey, true), "old.zip")

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"name": "old.zip"}, oldKey))
	require.Equal(t, 200, code, out)
	st := waitRestore(t, e)
	assert.Equal(t, false, st["failed"], st)
	assert.Equal(t, false, st["files_failed"], st)

	select {
	case f := <-e.s.done:
		assert.Equal(t, 4747, f.port)
	case <-time.After(5 * time.Second):
		t.Fatal("setup did not hand over")
	}

	cfg, err := config.Load(e.configPath)
	require.NoError(t, err)
	assert.Equal(t, oldKey, cfg.App.EncryptionKey)

	db := openSQLite(t, e.dbPath)
	var user models.User
	require.NoError(t, db.Where("email = ?", "old@example.com").First(&user).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("old-password-1")))
	cand, _ := e.s.lookup(e.configPath)
	assert.FileExists(t, filepath.Join(mediaDir(cand), "photo.jpg"))
}

func TestRestore_ByPathWithoutKeyGetsNewKey(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, oldKey, false)

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, ""))
	require.Equal(t, 200, code, out)
	st := waitRestore(t, e)
	require.Equal(t, false, st["failed"], st)
	<-e.s.done

	cfg, err := config.Load(e.configPath)
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.App.EncryptionKey)
	assert.NotEqual(t, oldKey, cfg.App.EncryptionKey)
}

func TestRestore_BackupWithoutKeyUsesOperatorKey(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, "", false)

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, "my-own-key-0123456789"))
	require.Equal(t, 200, code, out)
	require.Equal(t, false, waitRestore(t, e)["failed"])
	<-e.s.done

	cfg, err := config.Load(e.configPath)
	require.NoError(t, err)
	assert.Equal(t, "my-own-key-0123456789", cfg.App.EncryptionKey)
}

func TestRestore_Refusals(t *testing.T) {
	e := newEnv(t)
	src := makeBackup(t, e.dir, oldKey, false)

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, "wrong-key"))
	assert.Equal(t, 400, code, out)
	assert.Contains(t, out["message"], "encryption key")

	code, _ = call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"name": "../x.zip"}, ""))
	assert.Equal(t, 400, code)

	// A database that already has users.
	used := openSQLite(t, e.dbPath)
	require.NoError(t, database.RunMigrationWithProgress(used, &config.DefaultAdminConfig{
		Email: "someone@example.com", Password: "password-123", FullName: "Someone", OrganizationName: "Theirs",
	}))
	code, out = call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	assert.Equal(t, 409, code, out)

	assert.NoFileExists(t, e.configPath)
}

func TestRestore_FailedJobLeavesNoConfigAndCanRetry(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, oldKey, false)
	realMigrate := e.s.migrate
	e.s.migrate = func(*gorm.DB, *config.DefaultAdminConfig) error { return errors.New("disk on fire") }

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	require.Equal(t, 200, code, out)
	st := waitRestore(t, e)
	assert.Equal(t, true, st["failed"])
	assert.Contains(t, st["error"], "disk on fire")
	assert.NoFileExists(t, e.configPath)

	e.s.migrate = realMigrate
	code, out = call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	require.Equal(t, 200, code, out)
	assert.Equal(t, false, waitRestore(t, e)["failed"])
	<-e.s.done
	assert.FileExists(t, e.configPath)
}

func TestRestore_AfterFailedFreshAttempt(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, oldKey, false)
	realMigrate := e.s.migrate
	e.s.migrate = func(db *gorm.DB, a *config.DefaultAdminConfig) error {
		if err := realMigrate(db, a); err != nil {
			return err
		}
		return errors.New("failed after creating the admin")
	}
	code, _ := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	require.Equal(t, 500, code)

	e.s.migrate = realMigrate
	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	require.Equal(t, 200, code, out)
	assert.Equal(t, false, waitRestore(t, e)["failed"])
	<-e.s.done

	db := openSQLite(t, e.dbPath)
	var n int64
	require.NoError(t, db.Model(&models.User{}).Where("email = ?", "ana@example.com").Count(&n).Error)
	assert.Zero(t, n, "the fresh attempt's admin is replaced by the backup's users")
}

func TestRestore_FinalStateReadBeforeHandoff(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = 30 * time.Second
	src := makeBackup(t, e.dir, oldKey, false)

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	require.Equal(t, 200, code, out)

	// Wait for the job to finish without reading its state through the API.
	require.Eventually(t, func() bool { return e.s.getRestore().Finished }, 2*time.Minute, 20*time.Millisecond)
	select {
	case <-e.s.done:
		t.Fatal("handed over before the page read the result")
	case <-time.After(200 * time.Millisecond):
	}
	st := waitRestore(t, e)
	assert.Equal(t, false, st["failed"])
	select {
	case <-e.s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("reading the final state did not release the hand-off")
	}
}

func TestComplete_RefusedWhileRestoreRuns(t *testing.T) {
	e := newEnv(t)
	e.s.setRestore(restoreState{Running: true, Phase: phasePrepare})
	code, _ := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code)
	code, _ = call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"name": "x.zip"}, ""))
	assert.Equal(t, 409, code)
}
```

Add an env-gated cross-database test in the same file:

```go
func TestRestore_IntoPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_BACKUP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_BACKUP_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	name := strings.TrimPrefix(u.Path, "/")
	require.True(t, strings.HasPrefix(name, "whatomate_test"), "test database name must start with whatomate_test")
	port, _ := strconv.Atoi(u.Port())
	pass, _ := u.User.Password()
	dbIn := map[string]any{"driver": "postgres", "host": u.Hostname(), "port": port, "user": u.User.Username(), "password": pass, "name": name, "ssl_mode": "disable"}

	// Start from an empty database.
	pg, err := database.Open(&config.DatabaseConfig{Driver: config.DriverPostgres, Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pass, Name: name, SSLMode: "disable"}, false)
	require.NoError(t, err)
	require.NoError(t, pg.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public").Error)
	if sqlDB, err := pg.DB(); err == nil {
		_ = sqlDB.Close()
	}

	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, oldKey, true)
	body := restoreBody(e, map[string]any{"path": src}, oldKey)
	body["database"] = dbIn
	code, out := call(t, e.s, "POST", "/api/setup/restore", body)
	require.Equal(t, 200, code, out)
	st := waitRestore(t, e)
	require.Equal(t, false, st["failed"], st)
	<-e.s.done
	assert.FileExists(t, e.configPath)
}
```

(imports `net/url`, `strconv`, `strings`). Never log `dsn` or `pass`.

- [ ] **Step 2: Run them to make sure they fail**

Run: `CGO_ENABLED=0 go test ./internal/setup -run 'TestRestore|TestComplete_RefusedWhileRestoreRuns' -count=1`
Expected: FAIL (404 / undefined: restoreState).

- [ ] **Step 3: Implement `internal/setup/restore.go`**

```go
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

const (
	phasePrepare = "prepare"
	phaseConfig  = "config"
)

type restoreRequest struct {
	targetRequest
	Backup        backupRef `json:"backup"`
	EncryptionKey string    `json:"encryption_key"`
}

// restoreState is the restore job's progress, polled by the page.
type restoreState struct {
	Running     bool   `json:"running"`
	Phase       string `json:"phase"`
	Table       string `json:"table,omitempty"`
	Done        int64  `json:"done"`
	Total       int64  `json:"total"`
	Finished    bool   `json:"finished"`
	Failed      bool   `json:"failed"`
	FilesFailed bool   `json:"files_failed"`
	Error       string `json:"error,omitempty"`
}

func (s *server) getRestore() restoreState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.restore
}

func (s *server) setRestore(st restoreState) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.restore = st
}

func (s *server) updateRestore(fn func(*restoreState)) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	fn(&s.restore)
}

// startRestore checks the request like a fresh finish, then restores in the
// background.
func (s *server) startRestore(ctx *fasthttp.RequestCtx) {
	s.completeMu.Lock()
	defer s.completeMu.Unlock()
	if s.isFinished() != nil {
		writeError(ctx, fasthttp.StatusConflict, "setup is already complete")
		return
	}
	if s.getRestore().Running {
		writeError(ctx, fasthttp.StatusConflict, "a restore is already running")
		return
	}
	var req restoreRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	tg, code, err := s.checkTarget(req.targetRequest)
	if err != nil {
		writeError(ctx, code, err.Error())
		return
	}
	path, err := s.resolveBackup(tg.cand, req.Backup)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(req.EncryptionKey)
	sum, err := backup.InspectFile(path, tg.db.Driver, key)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnprocessableEntity, err.Error())
		return
	}
	if key != "" && !sum.KeyMatches {
		writeError(ctx, fasthttp.StatusBadRequest, "This encryption key does not match the backup")
		return
	}
	db, closeDB, code, err := openTarget(tg.db)
	if err != nil {
		writeError(ctx, code, err.Error())
		return
	}
	if _, err := s.reusableAdmin(db, databaseTarget(tg.db)); err != nil {
		closeDB()
		if errors.Is(err, errHasUsers) {
			writeError(ctx, fasthttp.StatusConflict, err.Error())
		} else {
			writeError(ctx, fasthttp.StatusInternalServerError, err.Error())
		}
		return
	}
	origin, https := browserOrigin(ctx, req.Port, s.listenPort)
	s.setRestore(restoreState{Running: true, Phase: phasePrepare})
	go s.runRestore(db, closeDB, tg, path, key, finished{port: tg.port, url: origin}, https)
	writeData(ctx, map[string]bool{"started": true})
}

func (s *server) runRestore(db *gorm.DB, closeDB func(), tg target, path, key string, f finished, https bool) {
	defer closeDB()
	fail := func(err error) {
		s.o.Log.Error("Restoring the backup failed", "error", err)
		s.updateRestore(func(r *restoreState) {
			r.Running, r.Finished, r.Failed, r.Error = false, true, true, err.Error()
		})
	}
	if err := s.migrate(db, nil); err != nil {
		fail(fmt.Errorf("set up the database: %w", err))
		return
	}
	media, audio := mediaDir(tg.cand), audioDir(tg.cand)
	for _, d := range []string{media, audio} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			fail(err)
			return
		}
	}
	err := backup.Restore(context.Background(), db, path, backup.RestoreOptions{
		EncryptionKey: key,
		Areas:         backup.Areas(media, audio),
		Skip:          backup.ProtectedPaths(backupDir(tg.cand), tg.cand.Path, tg.db),
	}, func(p backup.Progress) {
		s.updateRestore(func(r *restoreState) { r.Phase, r.Table, r.Done, r.Total = p.Phase, p.Table, p.Done, p.Total })
	})
	filesFailed := errors.Is(err, backup.ErrFiles)
	if err != nil && !filesFailed {
		fail(err)
		return
	}
	s.updateRestore(func(r *restoreState) {
		r.Phase, r.Table, r.Done, r.Total = phaseConfig, "", 0, 0
		if filesFailed {
			r.FilesFailed, r.Error = true, err.Error()
		}
	})
	if _, err := s.writeConfig(tg, f.url, https, key); err != nil {
		fail(err)
		return
	}
	s.updateRestore(func(r *restoreState) { r.Running, r.Finished = false, true })
	// Give the page a moment to read the result before setup hands over.
	select {
	case <-s.restoreSeen:
	case <-time.After(s.restoreAckWait):
	}
	s.finishSetup(f)
}

func (s *server) restoreStatus(ctx *fasthttp.RequestCtx) {
	st := s.getRestore()
	if st.Finished && !st.Failed {
		select {
		case s.restoreSeen <- struct{}{}:
		default:
		}
	}
	writeData(ctx, st)
}
```

`TestRestore_FinalStateReadBeforeHandoff` relies on the `migrate` step and the restore running long enough that `Finished` is observed before the API read; it waits on `Finished` and then checks `done` stays empty, so it does not race.

- [ ] **Step 4: Wire it into `internal/setup/server.go`**

Add fields to `server`:

```go
	// restore is the background restore's progress. Guarded by stateMu.
	restore restoreState
	// restoreSeen is signalled when the page reads a finished restore.
	restoreSeen chan struct{}
	// restoreAckWait is how long a finished restore waits for the page.
	restoreAckWait time.Duration
```

In `newServer` set `restoreSeen: make(chan struct{}, 1), restoreAckWait: 5 * time.Second`.

In `handle`, add before the `/api/` fallback:

```go
	case path == "/api/setup/restore" && ctx.IsPost():
		s.startRestore(ctx)
	case path == "/api/setup/restore" && ctx.IsGet():
		s.restoreStatus(ctx)
```

In `complete`, right after the "already complete" check:

```go
	if s.getRestore().Running {
		writeError(ctx, fasthttp.StatusConflict, "a restore is running")
		return
	}
```

- [ ] **Step 5: Run the setup tests**

Run: `CGO_ENABLED=0 go test ./internal/setup -count=1 -race`
Expected: PASS.

Then the Postgres test (prints nothing secret):

Run: `source .superpowers/sdd/2026-10-14-setup-restore/testenv.sh && CGO_ENABLED=0 go test ./internal/setup -run TestRestore_IntoPostgres -count=1 -v 2>&1 | rg "^(=== RUN|--- |ok|FAIL|PASS)"`
Expected: PASS (or SKIP when the variable is not set).

- [ ] **Step 6: Commit**

```bash
git add internal/setup/restore.go internal/setup/restore_test.go internal/setup/server.go
git commit -m "feat(setup): restore a backup from the setup wizard"
```

---

### Task 6: Frontend helpers and setup API calls

**Files:**
- Modify: `frontend/src/lib/setup.ts`, `frontend/src/lib/setup.spec.ts`
- Modify: `frontend/src/services/api.ts` (types and `setupService`)

**Interfaces:**
- Consumes: the Task 4 and 5 endpoints; `BackupManifest`, `BackupInfo` types from `api.ts`.
- Produces:
  - `SetupMode = 'fresh' | 'restore'`, `RESTORE_STEPS`, `stepsFor(mode)`, `SetupStep` now includes `'restore'`
  - `KeyState = 'not-needed' | 'blank' | 'match' | 'mismatch'`, `keyState(summary, key)`, `restoreReady(summary, key)`
  - `RESTORE_PHASES = ['prepare', 'verify', 'load', 'links', 'swap', 'config'] as const`, `restorePhaseIndex(phase)`
  - types `SetupBackupRef`, `SetupBackupSummary`, `SetupRestoreStatus`, `SetupRestoreRequest`, `SetupRestoreChoice`
  - `setupService.backups`, `startUpload`, `putChunk`, `finishUpload`, `cancelUpload`, `inspectBackup`, `restore`, `restoreStatus`

- [ ] **Step 1: Write the failing spec**

Append to `frontend/src/lib/setup.spec.ts`:

```ts
import { keyState, restorePhaseIndex, restoreReady, stepsFor } from './setup'
import type { SetupBackupSummary } from '@/services/api'

function summary(fingerprint: string, matches: boolean): SetupBackupSummary {
  return {
    name: 'b.zip', size: 1, target_driver: 'sqlite', key_matches: matches, key_given: true,
    manifest: {
      format: 1, app_version: 'v1', kind: 'manual', created_at: '2026-01-01T00:00:00Z', source_driver: 'sqlite',
      key_fingerprint: fingerprint, include_files: false, tables: [],
    },
  }
}

describe('stepsFor', () => {
  it('swaps the last step', () => {
    expect(stepsFor('fresh')).toEqual(['welcome', 'checks', 'database', 'admin'])
    expect(stepsFor('restore')).toEqual(['welcome', 'checks', 'database', 'restore'])
  })
})

describe('keyState and restoreReady', () => {
  it('needs no key when the backup has none', () => {
    expect(keyState(summary('', true), '')).toBe('not-needed')
    expect(restoreReady(summary('', true), 'anything')).toBe(true)
  })
  it('allows a blank key with a warning', () => {
    expect(keyState(summary('abc', false), '  ')).toBe('blank')
    expect(restoreReady(summary('abc', false), '')).toBe(true)
  })
  it('blocks a key that does not match', () => {
    expect(keyState(summary('abc', false), 'wrong')).toBe('mismatch')
    expect(restoreReady(summary('abc', false), 'wrong')).toBe(false)
    expect(keyState(summary('abc', true), 'right')).toBe('match')
  })
  it('is not ready without a summary', () => {
    expect(restoreReady(null, '')).toBe(false)
  })
})

describe('restorePhaseIndex', () => {
  it('orders the phases', () => {
    expect(restorePhaseIndex('prepare')).toBe(0)
    expect(restorePhaseIndex('load')).toBe(2)
    expect(restorePhaseIndex('config')).toBe(5)
    expect(restorePhaseIndex('')).toBe(0)
  })
})
```

(If `setup.spec.ts` already imports from `./setup` or has `describe`/`expect` imported, merge the imports instead of duplicating them.)

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd frontend && npx vitest run src/lib/setup.spec.ts`
Expected: FAIL (`stepsFor` is not exported).

- [ ] **Step 3: Add the API types and calls**

In `frontend/src/services/api.ts`, after `SetupCompleteRequest` and before `setupService`:

```ts
export interface SetupBackupRef {
  name?: string
  path?: string
}

export interface SetupBackupSummary {
  name: string
  size: number
  manifest: BackupManifest
  target_driver: string
  key_matches: boolean
  key_given: boolean
}

export interface SetupRestoreChoice {
  backup: SetupBackupRef | null
  encryptionKey: string
}

export interface SetupRestoreRequest {
  config_path: string
  port: number
  database: SetupDatabase
  backup: SetupBackupRef
  encryption_key: string
}

export interface SetupRestoreStatus {
  running: boolean
  phase: string
  table?: string
  done: number
  total: number
  finished: boolean
  failed: boolean
  files_failed: boolean
  error?: string
}
```

Extend `setupService` (keep the existing members):

```ts
  backups: (configPath: string) =>
    api.get<{ data: { dir: string; backups: BackupInfo[] } }>('/setup/backups', { params: { config_path: configPath } }),
  startUpload: (configPath: string, name: string, size: number) =>
    api.post<{ data: { id: string; received: number; chunk_max: number } }>('/setup/backup-uploads', { config_path: configPath, name, size }),
  putChunk: (id: string, offset: number, chunk: Blob) =>
    api.put<{ data: { received: number } }>(`/setup/backup-uploads/${id}`, chunk, {
      params: { offset },
      headers: { 'Content-Type': 'application/octet-stream' },
      timeout: 120000,
    }),
  // The setup server takes only JSON POST bodies, so send an empty object.
  finishUpload: (id: string) => api.post<{ data: BackupInfo }>(`/setup/backup-uploads/${id}/finish`, {}, { timeout: 120000 }),
  cancelUpload: (id: string) => api.delete(`/setup/backup-uploads/${id}`),
  inspectBackup: (req: { config_path: string; database: SetupDatabase; backup: SetupBackupRef; encryption_key: string }) =>
    api.post<{ data: SetupBackupSummary }>('/setup/backup/inspect', req, { timeout: 60000 }),
  restore: (req: SetupRestoreRequest) => api.post<{ data: { started: boolean } }>('/setup/restore', req, { timeout: 120000 }),
  restoreStatus: () => api.get<{ data: SetupRestoreStatus }>('/setup/restore'),
```

- [ ] **Step 4: Add the helpers to `frontend/src/lib/setup.ts`**

Change the import line to also take `SetupBackupSummary`, then replace the `SETUP_STEPS` / `SetupStep` lines with:

```ts
export const SETUP_STEPS = ['welcome', 'checks', 'database', 'admin'] as const
export const RESTORE_STEPS = ['welcome', 'checks', 'database', 'restore'] as const
export type SetupStep = (typeof SETUP_STEPS)[number] | (typeof RESTORE_STEPS)[number]
export type SetupMode = 'fresh' | 'restore'

export function stepsFor(mode: SetupMode): readonly SetupStep[] {
  return mode === 'restore' ? RESTORE_STEPS : SETUP_STEPS
}
```

and append:

```ts
export type KeyState = 'not-needed' | 'blank' | 'match' | 'mismatch'

/** keyState: summary must come from inspecting with this key. */
export function keyState(summary: SetupBackupSummary, key: string): KeyState {
  if (!summary.manifest.key_fingerprint) return 'not-needed'
  if (!key.trim()) return 'blank'
  return summary.key_matches ? 'match' : 'mismatch'
}

export function restoreReady(summary: SetupBackupSummary | null, key: string): boolean {
  return !!summary && keyState(summary, key) !== 'mismatch'
}

export const RESTORE_PHASES = ['prepare', 'verify', 'load', 'links', 'swap', 'config'] as const

export function restorePhaseIndex(phase: string): number {
  return Math.max(0, RESTORE_PHASES.indexOf(phase as (typeof RESTORE_PHASES)[number]))
}
```

- [ ] **Step 5: Run the checks**

Run: `cd frontend && npx vitest run && npx vue-tsc --noEmit 2>&1 | rg -c "error TS"`
Expected: all specs pass; the vue-tsc count equals the baseline you record before this task (20 on this branch).

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/setup.ts frontend/src/lib/setup.spec.ts frontend/src/services/api.ts
git commit -m "feat(frontend): setup restore helpers and API calls"
```

---

### Task 7: Welcome choice, Restore step and restore progress

**Files:**
- Create: `frontend/src/views/setup/SetupRestore.vue`
- Modify: `frontend/src/views/setup/SetupView.vue`
- Modify: `frontend/src/i18n/locales/en.json` (`setup.*`)

**Interfaces:**
- Consumes: Task 6 helpers and `setupService` calls; `uploadInChunks`, `formatBytes` from `@/lib/backup`; `formatDateTime` from `@/lib/utils`.
- Produces: the user-facing flow.

- [ ] **Step 1: Strings**

In `en.json` under `setup`:
- `steps.restore`: `"Restore"`; `stepHints.restore`: `"Bring back a backup"`.
- In `welcome`: change `"body"` to `"No configuration was found, so let's set this server up. Start fresh with a new admin account, or restore a backup from another server."` and add:

```json
"modes": {
  "fresh": { "title": "Start fresh", "body": "Check the system, choose a database and create the first admin account." },
  "restore": { "title": "Restore from a backup", "body": "Move an existing whatomate here: choose a database and restore a backup made in Settings → Backup & Restore." }
},
"continue": "Continue"
```

- Add a `restore` object:

```json
"restore": {
  "title": "Restore a backup",
  "subtitle": "Everything in the backup is loaded into the database you chose. You then sign in with an account from the backup.",
  "tabUpload": "Upload a backup",
  "tabServer": "On this server",
  "chooseFile": "Choose a backup file (.zip)",
  "uploading": "Uploading {percent}%",
  "folder": "Backups in {dir}",
  "none": "No backups in this folder yet",
  "pathLabel": "Or enter the full path to a backup",
  "pathPlaceholder": "/path/to/whatomate-backup.zip",
  "usePath": "Use this file",
  "checking": "Checking the backup…",
  "made": "Made {date} with whatomate {version}",
  "move": "{from} → {to}",
  "rows": "{rows} rows in {tables} tables",
  "files": "Includes {count} files ({size})",
  "noFiles": "Database only",
  "keyLabel": "Old encryption key (optional)",
  "keyHint": "The encryption_key from the old server's config.toml. Saved WhatsApp tokens, AI keys and SSO secrets only work with it.",
  "keyMatch": "Matches the backup",
  "keyMismatch": "Does not match this backup",
  "keyBlank": "Without the old key, saved WhatsApp tokens, AI provider keys and SSO secrets will not work and must be entered again after signing in.",
  "keyNotNeeded": "This backup has no encrypted secrets; no key is needed.",
  "signInNote": "After the restore, sign in with an account from the backup.",
  "finish": "Restore and finish"
}
```

- In `finish` add:

```json
"restoreTitle": "Restoring the backup",
"restorePhases": {
  "prepare": "Preparing the database",
  "verify": "Checking the backup",
  "load": "Loading data",
  "links": "Linking records",
  "swap": "Putting files back",
  "config": "Saving settings"
},
"filesFailed": "The data was restored, but some files could not be put back: {error}"
```

- [ ] **Step 2: Create `frontend/src/views/setup/SetupRestore.vue`**

```vue
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Upload, HardDrive, Loader2, CheckCircle2, AlertTriangle, XCircle, KeyRound, FileArchive } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Progress } from '@/components/ui/progress'
import SetupFooter from './SetupFooter.vue'
import { setupService, type BackupInfo, type SetupBackupSummary, type SetupDatabase, type SetupRestoreChoice } from '@/services/api'
import { formatBytes, uploadInChunks } from '@/lib/backup'
import { keyState, restoreReady } from '@/lib/setup'
import { formatDateTime } from '@/lib/utils'

const props = defineProps<{ configPath: string; database: SetupDatabase }>()
const choice = defineModel<SetupRestoreChoice>({ required: true })
const emit = defineEmits<{ back: []; finish: [] }>()
const { t } = useI18n()

const tab = ref<'upload' | 'server'>('upload')
const dir = ref('')
const backups = ref<BackupInfo[]>([])
const typedPath = ref('')
const uploadPercent = ref<number | null>(null)
const summary = ref<SetupBackupSummary | null>(null)
const checking = ref(false)
const error = ref('')

const message = (e: any) => e?.response?.data?.message ?? String(e)

async function loadList() {
  try {
    const res = (await setupService.backups(props.configPath)).data.data
    dir.value = res.dir
    backups.value = res.backups
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(loadList)

let seq = 0
async function inspect(keepSummary: boolean) {
  const backup = choice.value.backup
  if (!keepSummary) summary.value = null
  if (!backup) return
  const mine = ++seq
  checking.value = true
  error.value = ''
  try {
    const res = (await setupService.inspectBackup({
      config_path: props.configPath, database: props.database, backup, encryption_key: choice.value.encryptionKey.trim(),
    })).data.data
    if (mine === seq) summary.value = res
  } catch (e) {
    if (mine === seq) {
      summary.value = null
      error.value = message(e)
    }
  } finally {
    if (mine === seq) checking.value = false
  }
}

let keyTimer: ReturnType<typeof setTimeout> | undefined
watch(() => choice.value.backup, () => inspect(false), { deep: true, immediate: true })
watch(() => choice.value.encryptionKey, () => {
  clearTimeout(keyTimer)
  checking.value = !!choice.value.backup
  keyTimer = setTimeout(() => inspect(true), 400)
})

function pick(name: string) {
  choice.value = { ...choice.value, backup: { name } }
}

function usePath() {
  const p = typedPath.value.trim()
  if (p) choice.value = { ...choice.value, backup: { path: p } }
}

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  uploadPercent.value = 0
  error.value = ''
  try {
    const info = (await uploadInChunks(file, {
      start: async (name, size) => (await setupService.startUpload(props.configPath, name, size)).data.data,
      put: async (id, offset, chunk) => (await setupService.putChunk(id, offset, chunk)).data.data,
      finish: async (id) => (await setupService.finishUpload(id)).data.data,
      cancel: (id) => setupService.cancelUpload(id),
    }, (sent, total) => { uploadPercent.value = Math.round((sent / total) * 100) })) as BackupInfo
    await loadList()
    pick(info.name)
  } catch (err) {
    error.value = message(err)
  } finally {
    uploadPercent.value = null
  }
}

const state = computed(() => (summary.value ? keyState(summary.value, choice.value.encryptionKey) : null))
const ready = computed(() => !checking.value && uploadPercent.value === null && restoreReady(summary.value, choice.value.encryptionKey))
const rows = computed(() => summary.value?.manifest.tables.reduce((n, tb) => n + tb.rows, 0) ?? 0)
const files = computed(() => Object.values(summary.value?.manifest.files ?? {}).reduce(
  (acc, f) => ({ count: acc.count + f.count, bytes: acc.bytes + f.bytes }), { count: 0, bytes: 0 }))
const selected = computed(() => choice.value.backup?.name ?? '')
const tabClass = (active: boolean) => active
  ? 'bg-emerald-500/10 text-emerald-300 ring-1 ring-emerald-500/40 light:text-emerald-700'
  : 'text-white/55 hover:bg-white/[0.04] light:text-gray-500 light:hover:bg-gray-100'
</script>

<template>
  <form class="flex flex-1 flex-col" novalidate @submit.prevent="ready && emit('finish')">
    <div class="flex-1 space-y-6 px-6 pb-8 sm:px-10">
      <div class="inline-flex gap-1 rounded-xl border border-white/[0.08] p-1 light:border-gray-200" role="tablist">
        <button type="button" role="tab" :aria-selected="tab === 'upload'" class="inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm transition-colors" :class="tabClass(tab === 'upload')" data-testid="setup-restore-tab-upload" @click="tab = 'upload'">
          <Upload class="h-4 w-4" />{{ t('setup.restore.tabUpload') }}
        </button>
        <button type="button" role="tab" :aria-selected="tab === 'server'" class="inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm transition-colors" :class="tabClass(tab === 'server')" data-testid="setup-restore-tab-server" @click="tab = 'server'">
          <HardDrive class="h-4 w-4" />{{ t('setup.restore.tabServer') }}
        </button>
      </div>

      <div v-if="tab === 'upload'" class="space-y-3">
        <label class="flex cursor-pointer items-center justify-center gap-2 rounded-2xl border border-dashed border-white/[0.15] px-4 py-8 text-sm text-white/70 transition-colors hover:border-emerald-500/50 light:border-gray-300 light:text-gray-600" :class="uploadPercent !== null ? 'pointer-events-none opacity-60' : ''">
          <FileArchive class="h-5 w-5" />{{ t('setup.restore.chooseFile') }}
          <input type="file" accept=".zip,application/zip" class="sr-only" data-testid="setup-restore-file" @change="onFile" />
        </label>
        <div v-if="uploadPercent !== null" class="space-y-1.5">
          <Progress :model-value="uploadPercent" class="h-2" />
          <p class="text-xs text-white/50 light:text-gray-500">{{ t('setup.restore.uploading', { percent: uploadPercent }) }}</p>
        </div>
      </div>

      <div v-else class="space-y-4">
        <div class="space-y-2">
          <p class="break-all text-xs text-white/45 light:text-gray-500">{{ t('setup.restore.folder', { dir }) }}</p>
          <p v-if="!backups.length" class="text-sm text-white/50 light:text-gray-500">{{ t('setup.restore.none') }}</p>
          <ul v-else class="max-h-56 space-y-1 overflow-y-auto">
            <li v-for="b in backups" :key="b.name">
              <button
                type="button"
                class="flex w-full items-center justify-between gap-3 rounded-xl border px-3 py-2 text-left text-sm transition-colors"
                :class="selected === b.name ? 'border-emerald-500/50 bg-emerald-500/[0.06]' : 'border-white/[0.08] hover:bg-white/[0.03] light:border-gray-200 light:hover:bg-gray-50'"
                :disabled="!!b.error"
                :data-testid="`setup-restore-backup-${b.name}`"
                @click="pick(b.name)"
              >
                <span class="min-w-0">
                  <span class="block truncate font-mono text-xs">{{ b.name }}</span>
                  <span class="block text-xs text-white/45 light:text-gray-500">{{ b.manifest ? formatDateTime(b.manifest.created_at) : b.error }}</span>
                </span>
                <span class="shrink-0 text-xs text-white/50 light:text-gray-500">{{ formatBytes(b.size) }}</span>
              </button>
            </li>
          </ul>
        </div>
        <div class="space-y-2">
          <Label for="setup-restore-path" class="text-white/70 light:text-gray-700">{{ t('setup.restore.pathLabel') }}</Label>
          <div class="flex gap-2">
            <Input id="setup-restore-path" v-model="typedPath" class="font-mono" :placeholder="t('setup.restore.pathPlaceholder')" data-testid="setup-restore-path" />
            <Button type="button" variant="outline" :disabled="!typedPath.trim()" @click="usePath">{{ t('setup.restore.usePath') }}</Button>
          </div>
        </div>
      </div>

      <div v-if="error" class="flex gap-2 rounded-xl border border-red-500/25 bg-red-500/[0.06] px-4 py-3 text-sm text-red-300 light:text-red-700" data-testid="setup-restore-error">
        <XCircle class="mt-0.5 h-4 w-4 shrink-0" /><span class="break-words">{{ error }}</span>
      </div>

      <p v-if="checking && !summary" class="flex items-center gap-2 text-sm text-white/55 light:text-gray-500">
        <Loader2 class="h-4 w-4 animate-spin" />{{ t('setup.restore.checking') }}
      </p>

      <div v-if="summary" class="space-y-4" data-testid="setup-restore-summary">
        <div class="space-y-1 rounded-xl border border-white/[0.08] bg-white/[0.02] p-4 text-sm light:border-gray-200 light:bg-gray-50">
          <p class="font-medium">{{ summary.name }}</p>
          <p class="text-white/60 light:text-gray-600">{{ t('setup.restore.move', { from: summary.manifest.source_driver, to: summary.target_driver }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ t('setup.restore.made', { date: formatDateTime(summary.manifest.created_at), version: summary.manifest.app_version || '?' }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ t('setup.restore.rows', { rows: rows.toLocaleString(), tables: summary.manifest.tables.length }) }}</p>
          <p class="text-white/60 light:text-gray-600">
            {{ summary.manifest.include_files ? t('setup.restore.files', { count: files.count, size: formatBytes(files.bytes) }) : t('setup.restore.noFiles') }}
          </p>
        </div>

        <div v-if="state !== 'not-needed'" class="space-y-2">
          <Label for="setup-restore-key" class="flex items-center gap-2 text-white/70 light:text-gray-700"><KeyRound class="h-3.5 w-3.5" />{{ t('setup.restore.keyLabel') }}</Label>
          <Input id="setup-restore-key" v-model="choice.encryptionKey" type="password" autocomplete="off" class="font-mono" data-testid="setup-restore-key" />
          <p class="text-xs text-white/45 light:text-gray-500">{{ t('setup.restore.keyHint') }}</p>
          <p v-if="checking" class="flex items-center gap-1.5 text-xs text-white/50"><Loader2 class="h-3 w-3 animate-spin" />{{ t('setup.restore.checking') }}</p>
          <p v-else-if="state === 'match'" class="flex items-center gap-1.5 text-xs text-emerald-400 light:text-emerald-600" data-testid="setup-restore-key-match"><CheckCircle2 class="h-3.5 w-3.5" />{{ t('setup.restore.keyMatch') }}</p>
          <p v-else-if="state === 'mismatch'" class="flex items-center gap-1.5 text-xs text-red-400 light:text-red-600" data-testid="setup-restore-key-mismatch"><XCircle class="h-3.5 w-3.5" />{{ t('setup.restore.keyMismatch') }}</p>
          <p v-else class="flex gap-2 rounded-xl border border-amber-500/20 bg-amber-500/[0.06] px-3 py-2 text-xs text-amber-300/90 light:text-amber-700"><AlertTriangle class="mt-0.5 h-3.5 w-3.5 shrink-0" />{{ t('setup.restore.keyBlank') }}</p>
        </div>
        <p v-else class="text-xs text-white/45 light:text-gray-500">{{ t('setup.restore.keyNotNeeded') }}</p>

        <p class="text-xs text-white/55 light:text-gray-500">{{ t('setup.restore.signInNote') }}</p>
      </div>
    </div>

    <SetupFooter>
      <Button type="button" variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button type="submit" :disabled="!ready" data-testid="setup-restore-finish">{{ t('setup.restore.finish') }}</Button>
    </SetupFooter>
  </form>
</template>
```

- [ ] **Step 3: Update `frontend/src/views/setup/SetupView.vue`**

Script changes:

```ts
import { RefreshCcw, Sparkles } from 'lucide-vue-next' // add to the existing lucide import
import type { SetupRestoreChoice, SetupRestoreStatus } from '@/services/api' // merge into the existing api import
import { RESTORE_PHASES, restorePhaseIndex, stepsFor, type SetupMode } from '@/lib/setup' // merge; drop SETUP_STEPS from the import if unused
import SetupRestore from './SetupRestore.vue'

const mode = ref<SetupMode>('fresh')
const steps = computed(() => stepsFor(mode.value))
const restoreChoice = ref<SetupRestoreChoice>({ backup: null, encryptionKey: '' })
const restoreStatus = ref<SetupRestoreStatus | null>(null)
```

- `phase` becomes `ref<'form' | 'saving' | 'restoring' | 'starting'>('form')`.
- `stepIndex` becomes `computed(() => steps.value.indexOf(step.value))`.
- Replace every `SETUP_STEPS` in the template with `steps`.
- `phases` becomes:

```ts
const phases = computed<{ label: string; state: PhaseState }[]>(() => {
  const starting = {
    label: t('setup.finish.starting'),
    state: (phase.value === 'starting' ? (startTimedOut.value ? 'failed' : 'running') : 'pending') as PhaseState,
  }
  if (mode.value === 'restore') {
    const current = phase.value === 'restoring' ? restorePhaseIndex(restoreStatus.value?.phase ?? '') : RESTORE_PHASES.length
    return [
      ...RESTORE_PHASES.map((p, i) => ({
        label: t(`setup.finish.restorePhases.${p}`),
        state: (i < current ? 'done' : i === current ? 'running' : 'pending') as PhaseState,
      })),
      starting,
    ]
  }
  return [
    { label: t('setup.finish.saving'), state: phase.value === 'saving' ? 'running' : 'done' },
    { ...starting, state: phase.value === 'saving' ? 'pending' : starting.state },
  ]
})
```

- Add the restore flow:

```ts
function chooseMode(m: SetupMode) {
  mode.value = m
  go('checks')
}

async function startRestore() {
  phase.value = 'restoring'
  error.value = ''
  restoreStatus.value = null
  try {
    await setupService.restore({
      config_path: configPath.value,
      port: port.value,
      database: database.value,
      backup: restoreChoice.value.backup!,
      encryption_key: restoreChoice.value.encryptionKey.trim(),
    })
  } catch (e: any) {
    phase.value = 'form'
    if (e?.response?.status === 409) step.value = 'database'
    error.value = e?.response?.data?.message ?? t('setup.finish.failed')
    return
  }
  for (;;) {
    try {
      const st = (await setupService.restoreStatus()).data.data
      restoreStatus.value = st
      if (st.finished) {
        if (st.failed) {
          phase.value = 'form'
          step.value = 'restore'
          error.value = st.error || t('setup.finish.failed')
          return
        }
        break
      }
    } catch {
      // setup has handed over to the app
      break
    }
    await sleep(1000)
  }
  phase.value = 'starting'
  await waitForApp()
}
```

Template changes:
- In the finishing block, the title becomes `{{ mode === 'restore' ? t('setup.finish.restoreTitle') : t('setup.finish.title') }}`; the `slow` line shows when `phase === 'saving' || phase === 'restoring'`. Under the phase list add:

```vue
<div v-if="restoreStatus?.files_failed" class="mt-6 flex max-w-md gap-2 rounded-xl border border-amber-500/20 bg-amber-500/[0.06] px-4 py-3 text-left text-xs text-amber-300/90 light:text-amber-700" data-testid="setup-files-failed">
  <AlertTriangle class="mt-0.5 h-3.5 w-3.5 shrink-0" />
  <span class="break-words">{{ t('setup.finish.filesFailed', { error: restoreStatus.error ?? '' }) }}</span>
</div>
```

- In the header, use `step === 'restore' ? t('setup.restore.title') : t(`setup.${step}.title`)` only if `setup.restore.title` would not already resolve through `setup.${step}.title` — it does (`setup.restore.title` exists), so no change is needed there.
- Replace the Welcome body (tiles and footer) with the mode cards:

```vue
<div v-if="step === 'welcome'" class="flex flex-1 flex-col">
  <div class="flex-1 px-6 pb-8 sm:px-10">
    <div class="grid gap-3 sm:grid-cols-2" role="radiogroup">
      <button
        v-for="m in (['fresh', 'restore'] as const)"
        :key="m"
        type="button"
        role="radio"
        :aria-checked="mode === m"
        class="rounded-2xl border p-5 text-left transition-colors"
        :class="mode === m ? 'border-emerald-500/60 bg-emerald-500/[0.06] ring-1 ring-emerald-500/40' : 'border-white/[0.07] bg-white/[0.02] hover:bg-white/[0.04] light:border-gray-200 light:bg-white light:hover:bg-gray-50'"
        :data-testid="`setup-mode-${m}`"
        @click="mode = m"
      >
        <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-emerald-500/10 text-emerald-400 ring-1 ring-emerald-500/20 light:text-emerald-600">
          <component :is="m === 'fresh' ? Sparkles : RefreshCcw" class="h-[18px] w-[18px]" />
        </div>
        <p class="mt-3 text-sm font-medium text-white light:text-gray-900">{{ t(`setup.welcome.modes.${m}.title`) }}</p>
        <p class="mt-1 text-xs leading-relaxed text-white/45 light:text-gray-500">{{ t(`setup.welcome.modes.${m}.body`) }}</p>
      </button>
    </div>
    <p class="mt-6 text-xs text-white/30 md:hidden light:text-gray-400">{{ t('setup.welcome.version', { version: status.version }) }}</p>
  </div>
  <SetupFooter>
    <span />
    <Button data-testid="setup-start" @click="chooseMode(mode)">{{ t('setup.welcome.continue') }}</Button>
  </SetupFooter>
</div>
```

Remove `welcomeTiles` and the `ShieldCheck`, `Database`, `UserRound` imports if they become unused, and the `setup.welcome.tiles` / `setup.welcome.start` strings only if nothing else uses them (`rg -n "welcome.tiles|welcome.start" frontend/src`).
- The Database step's `@next` goes to `mode === 'restore' ? 'restore' : 'admin'`: `@next="go(mode === 'restore' ? 'restore' : 'admin')"`.
- Before the final `SetupAdmin`, add:

```vue
<SetupRestore
  v-else-if="step === 'restore'"
  v-model="restoreChoice"
  :config-path="configPath"
  :database="database"
  @back="go('database')"
  @finish="startRestore"
/>
```

- [ ] **Step 4: Run the checks**

Run: `cd frontend && npx vitest run && npx eslint src/views/setup src/lib/setup.ts && npx vue-tsc --noEmit 2>&1 | rg -c "error TS"`
Expected: specs pass; eslint clean; vue-tsc count unchanged.

Then check every new key resolves (no raw `setup.` keys in the UI):

Run: `node -e "const j=require('./src/i18n/locales/en.json').setup;for(const k of ['steps.restore','stepHints.restore','welcome.modes.fresh.title','welcome.continue','restore.finish','finish.restorePhases.prepare','finish.filesFailed'])if(!k.split('.').reduce((o,p)=>o&&o[p],j))throw new Error(k)"` (from `frontend/`)
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/setup/SetupRestore.vue frontend/src/views/setup/SetupView.vue frontend/src/i18n/locales/en.json
git commit -m "feat(frontend): restore a backup from the setup wizard"
```

---

### Task 8: Docs and end-to-end check

**Files:**
- Modify: the setup/quickstart docs page (`rg -n "first-run|setup wizard" docs/src/content/docs --files-with-matches`) and `docs/src/content/docs/features/backup-restore.mdx` ("Moving to another database")

- [ ] **Step 1: Docs**

- Quickstart / first run: a short subsection "Restore from a backup" describing the Welcome choice, the two ways to pick a backup (upload, or a `.zip` in the data folder's `backups` folder or any full path), the optional old encryption key and what happens without it, that the database must have no users, and that you sign in with an account from the backup.
- Backup & Restore page, "Moving to another database": replace the throwaway-admin steps with: make and download a backup on the old server; start whatomate on the new server with no config; choose **Restore from a backup**; pick the database; upload the backup; paste the old `encryption_key` (from the old `config.toml`); restore; sign in with your old account. Keep one sentence that restoring from Settings on an installed server still works.

- [ ] **Step 2: Full test run**

```bash
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test ./internal/backup ./internal/database ./internal/setup ./internal/config -count=1
TEST_DB_DRIVER=sqlite CGO_ENABLED=0 go test ./internal/handlers -count=1
(source .superpowers/sdd/2026-10-14-setup-restore/testenv.sh && CGO_ENABLED=0 go test ./internal/setup -run TestRestore_IntoPostgres -count=1)
cd frontend && npx vitest run && npx eslint src/views/setup src/lib src/services && npx vue-tsc --noEmit 2>&1 | rg -c "error TS"
cd .. && make build-prod BINARY_NAME=/tmp/whatomate-sr && git status --short
```

Expected: all pass; vue-tsc at the baseline; binary at `/tmp/whatomate-sr`; no tracked build artefacts changed.

- [ ] **Step 3: Manual walkthrough (throwaway instances only)**

Never port 8080, never `config.local.toml`, never the dev database. Use temp folders under `/tmp` and spare ports.
1. Instance A: run `/tmp/whatomate-sr server -config /tmp/sr-a/config.toml` from `/tmp/sr-a`, finish setup with **Start fresh** (SQLite), add a contact and upload a media file, make a backup with files in Settings → Backup & Restore and download it.
2. Instance B: a fresh folder `/tmp/sr-b` and a spare port. On Welcome choose **Restore from a backup**, SQLite database, upload A's backup, check the summary, paste A's `encryption_key` (shows "Matches the backup"), restore. Progress shows, the page goes to sign-in; sign in with A's admin; the contact and media file are there; Settings → Backup & Restore lists the uploaded backup.
3. Repeat B against the `whatomate_test_backup` Postgres database (credentials from `testenv.sh`, never printed), using **On this server** with a full path.
4. Wrong key: shows "Does not match this backup" and the button stays disabled.
5. Screenshots of Welcome, the Restore step (summary + key states) and the restore progress, in dark and light themes.
Kill every process you started by PID.

- [ ] **Step 4: Commit**

```bash
git add docs
git commit -m "docs: restore a backup from the setup wizard"
```
