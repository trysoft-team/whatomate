# First-run Setup Wizard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When no config file exists, `whatomate` serves a browser setup wizard on port 4747 (system check → database → admin), writes the config, and continues as the normal app in the same process.

**Architecture:** A new `internal/setup` package runs a small fasthttp server (setup API + the embedded Vue frontend) on a shared listener. On completion it writes the config, migrates the chosen database, creates the admin, and hands the socket to the normal startup in `cmd/whatomate/main.go`. The Vue app gets a `/setup` route guarded by `GET /api/setup/status`. Port 4747 becomes the default; super admins can change it in Settings → General.

**Tech Stack:** Go 1.25, fasthttp + fastglue, GORM (postgres / mysql / glebarez sqlite), `golang.org/x/sys`, testify; Vue 3 + TypeScript, vue-router, vue-i18n, reka-ui components, vitest.

**Spec:** `docs/superpowers/specs/2026-10-13-setup-wizard-design.md`

## Global Constraints

- Pure Go, `CGO_ENABLED=0`, Go 1.25. No new external services or modules (`golang.org/x/sys` and `github.com/jackc/pgx/v5` are already in `go.mod` as indirect; they may become direct). Do not bump gorm v1.25.7. Never use mattn/go-sqlite3 or gorm's cgo sqlite driver.
- Default port: `4747` for both the setup wizard and the app.
- Setup runs only when `config.Locate` finds no file; it never overwrites an existing config file.
- No setup code/token (user's decision). Setup endpoints exist only in setup mode.
- Config file mode `0600`, folders `0750`. Passwords, DSNs and generated secrets are never logged or echoed back.
- Database names created by setup must match `^[A-Za-z0-9_]{1,63}$`.
- SQL Server is not offered.
- Tests: PostgreSQL `whatomate_test` on port 5433 (`TEST_DATABASE_URL`, URL form `postgres://…`), SQLite via `TEST_DB_DRIVER=sqlite`, MySQL via `TEST_MYSQL_DSN`; MySQL/PostgreSQL database names created by tests must be `whatomate_test` or `whatomate_test_[0-9a-f]{8}` and are dropped afterwards. Multi-package database test runs use `-p 1`. No Playwright against the dev database.
- Frontend unit tests are `src/**/*.spec.ts` (vitest, node environment): files under test must not touch `window` at import time.
- Shell: non-interactive `cp -f` / `mv -f` / `rm -f`. Commit locally on `feat/setup-wizard` only. No push, no tags.

## Review Focus

1. Setup must never overwrite a config file that appeared after setup started → `TestComplete_RefusesExistingConfig` (Task 8).
2. Retrying after a failed config write must use the retry's password and organization name → `TestComplete_RetryAppliesNewAdminDetails` (Task 8).
3. Pointing the wizard at a database that already has other users must not add a super admin → `TestComplete_RejectsDatabaseWithUsers` (Task 8).
4. Passwords and secrets containing quotes, backslashes, newlines or non-ASCII must survive the config file round trip → `TestRender_RoundTripsAwkwardValues` (Task 5).
5. Changing the port in Settings must not touch `[database] port`, comments, or CRLF line endings → `TestSetServerPort_OnlyServerTable`, `TestSetServerPort_KeepsCRLF` (Task 2).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/config/config.go` | `DefaultPort`, `DefaultAdminConfig.OrganizationName` |
| `internal/config/locate.go` | `ErrNotFound`, `SetupTarget`, `Target` |
| `internal/config/portfile.go` | `WriteFileAtomic`, `SetServerPort`, `ReadServerPort` |
| `internal/database/database.go` | `CreateDefaultAdmin` skips when any user exists; organization name |
| `internal/database/provision.go` | `Probe`, `CreateDatabase`, `ValidDatabaseName` |
| `internal/netutil/port.go` | `PortFree` |
| `internal/setup/configfile.go` | `Values`, `Render`, `quote`, `newSecret`, `storedDBPath` |
| `internal/setup/origin.go` | `browserOrigin`, `environmentFor` |
| `internal/setup/fs.go` | `Writable`, `nearestExisting`, `fileExists` |
| `internal/setup/checks.go` | `Checker`, `CheckInput`, `CheckResult`, `NewChecker` |
| `internal/setup/sysinfo_*.go` | `hostSystem` per OS (memory, free disk) |
| `internal/setup/listener.go` | `Shared` listener with views |
| `internal/setup/server.go` | Setup HTTP handlers, `Run`, `Options`, `Result` |
| `internal/setup/complete.go` | `finish` (validate, migrate, admin, write config) |
| `internal/setup/browser.go` | `openBrowser`, `listenURLs`, `serveMoved` |
| `internal/handlers/setup_status.go` | Normal-mode `GET /api/setup/status` |
| `internal/handlers/server_settings.go` | `GET/PUT /api/admin/server-settings` |
| `cmd/whatomate/main.go` | No-args → server; setup hand-off; `App.ConfigPath` |
| `frontend/src/services/api.ts` | `setupService`, `serverSettingsService` + types |
| `frontend/src/lib/setup.ts` | Pure wizard logic |
| `frontend/src/lib/setupState.ts` | Cached setup status + redirect rule |
| `frontend/src/views/setup/*.vue` | Wizard screens |
| `frontend/src/router/index.ts` | `/setup` route and guard |
| `frontend/src/views/settings/SettingsView.vue` | Server card |
| `frontend/src/i18n/locales/en.json` | `setup.*`, `settings.server.*` strings |

---

### Task 1: Default port 4747

**Files:**
- Modify: `internal/config/config.go` (setDefaults, new const), `internal/config/config_test.go:32`
- Modify: `config.example.toml:15`, `Dockerfile:97`, `Dockerfile.goreleaser:54`, `docker-compose.yml:11`, `Makefile:170`, `frontend/vite.config.ts`, `frontend/package.json:14,17,18`, `frontend/e2e/helpers/audit.ts:3`, `frontend/e2e/helpers/api.ts:3`, `frontend/e2e/global-setup.ts:4`, `README.md`, `docs/src/content/docs/getting-started/*.mdx`, `docs/src/content/docs/api-reference/*.mdx`

**Interfaces:**
- Produces: `config.DefaultPort = 4747` (untyped const int)

- [ ] **Step 1: Change the test expectation first**

In `internal/config/config_test.go` line 32 change `assert.Equal(t, 8080, cfg.Server.Port)` to:

```go
	assert.Equal(t, config.DefaultPort, cfg.Server.Port)
	assert.Equal(t, 4747, config.DefaultPort)
```

(Lines 113 and 124 test an explicit `port = 8080` in a file; leave them.) If the test file is `package config` rather than `config_test`, drop the `config.` prefix.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/config/ -run 'TestLoad' -count=1`
Expected: FAIL (`undefined: config.DefaultPort` or `expected 4747, actual 8080`).

- [ ] **Step 3: Add the constant and use it**

In `internal/config/config.go`, next to `DefaultMaxOpenConns`:

```go
// DefaultPort is the port whatomate (and its setup wizard) listens on when
// [server] port is unset.
const DefaultPort = 4747
```

In `setDefaults`:

```go
	if cfg.Server.Port == 0 {
		cfg.Server.Port = DefaultPort
	}
```

- [ ] **Step 4: Run the config tests**

Run: `go test ./internal/config/ -count=1`
Expected: PASS.

- [ ] **Step 5: Change the port everywhere else**

- `config.example.toml`: `port = 4747`.
- `Dockerfile` and `Dockerfile.goreleaser`: `EXPOSE 4747`.
- `docker-compose.yml`: `- "4747:4747"`.
- `Makefile` line 170: `@echo "API :4747 (or [server] port)  |  UI :3000 (proxies /api → the API)"`.
- `frontend/vite.config.ts` `server.proxy`:

```ts
    proxy: {
      '/api': {
        target: process.env.WHATOMATE_API_URL ?? 'http://localhost:4747',
        changeOrigin: true
      },
      '/ws': {
        target: (process.env.WHATOMATE_API_URL ?? 'http://localhost:4747').replace(/^http/, 'ws'),
        ws: true
      }
    }
```

- `frontend/package.json` scripts `test`, `test:ui`, `test:debug`: `BASE_URL=http://localhost:4747`.
- `frontend/e2e/helpers/audit.ts`, `frontend/e2e/helpers/api.ts`, `frontend/e2e/global-setup.ts`: default `'http://localhost:4747'`.
- `README.md`, `docs/src/content/docs/getting-started/quickstart.mdx`, `configuration.mdx` and every `docs/src/content/docs/api-reference/*.mdx`: replace `8080` with `4747` (`rg -l 8080 README.md docs/src | xargs sed -i '' 's/8080/4747/g'` on macOS; check the diff — every hit is a port). Do not touch `internal/handlers/flow_endpoint_test.go` (its `localhost:8080` is a test Host header) or `docs/superpowers/**`.
- `configuration.mdx` line 472 now reads "proxies `/api` to `:4747`"; add after it: "Set `WHATOMATE_API_URL=http://localhost:8080` when your local config uses another port."

- [ ] **Step 6: Verify no stray port references**

Run: `rg -n '8080' --glob '!frontend/node_modules' --glob '!docs/superpowers/**' --glob '!**/dist/**' .`
Expected: only `internal/config/config_test.go` (explicit-port test) and `internal/handlers/flow_endpoint_test.go`.

Run: `go build ./... && cd frontend && npx vue-tsc --noEmit -p tsconfig.json`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add -A internal/config config.example.toml Dockerfile Dockerfile.goreleaser docker-compose.yml Makefile frontend/vite.config.ts frontend/package.json frontend/e2e README.md docs/src
git commit -m "feat: default port 4747"
```

---

### Task 2: Config — not-found error, setup target, port file editing

**Files:**
- Modify: `internal/config/locate.go`
- Create: `internal/config/portfile.go`, `internal/config/portfile_test.go`
- Test: `internal/config/locate_test.go`

**Interfaces:**
- Produces:
  - `var config.ErrNotFound error` — `errors.Is(err, config.ErrNotFound)` is true for every "no config file" error from `Locate` (missing `-config` file, missing `WHATOMATE_CONFIG` file, nothing found in the search).
  - `type config.SetupTarget struct { Fixed string; User, System []config.Source }`
  - `func config.Target(flagPath string) config.SetupTarget`
  - `func config.WriteFileAtomic(path string, data []byte, perm os.FileMode) error`
  - `func config.SetServerPort(path string, port int) error`
  - `func config.ReadServerPort(path string) (int, error)` — returns `DefaultPort` when the file has no `[server] port`.

- [ ] **Step 1: Write the failing locate tests**

Append to `internal/config/locate_test.go` (add `"errors"` to imports):

```go
func TestLocate_NotFoundErrorsMatchErrNotFound(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil)
	_, err := locate("", e)
	assert.True(t, errors.Is(err, ErrNotFound), "nothing found")

	_, err = locate(filepath.Join(root, "missing.toml"), e)
	assert.True(t, errors.Is(err, ErrNotFound), "missing -config file")

	e2, _ := fakeEnv(t, "linux", map[string]string{"WHATOMATE_CONFIG": "/nope.toml"})
	_, err = locate("", e2)
	assert.True(t, errors.Is(err, ErrNotFound), "missing WHATOMATE_CONFIG file")
}

func TestTarget_FlagIsFixed(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil)
	assert.Equal(t, SetupTarget{Fixed: filepath.Join(root, "custom.toml")}, target(filepath.Join(root, "custom.toml"), e))
	assert.Equal(t, filepath.Join(root, "cwd", "rel.toml"), target("rel.toml", e).Fixed)
}

func TestTarget_EnvIsFixed(t *testing.T) {
	e, root := fakeEnv(t, "linux", map[string]string{"WHATOMATE_CONFIG": "/env.toml"})
	assert.Equal(t, filepath.Join(root, "env.toml"), target("", e).Fixed)
}

func TestTarget_LinuxUserAndSystem(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil)
	tg := target("", e)
	assert.Empty(t, tg.Fixed)
	assert.Equal(t, []Source{{
		Path:    filepath.Join(root, "home/.config/whatomate/config.toml"),
		BaseDir: filepath.Join(root, "home/.config/whatomate"),
	}}, tg.User)
	assert.Equal(t, []Source{{
		Path:    filepath.Join(root, "etc/whatomate/config.toml"),
		BaseDir: filepath.Join(root, "var/lib/whatomate"),
	}}, tg.System)
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/config/ -run 'TestLocate_NotFound|TestTarget' -count=1`
Expected: FAIL to compile (`undefined: ErrNotFound`, `undefined: SetupTarget`).

- [ ] **Step 3: Implement in `locate.go`**

Add after `configFileName`:

```go
// ErrNotFound matches (errors.Is) the error Locate returns when no config
// file exists, as opposed to one that exists but can't be read.
var ErrNotFound = errors.New("config file not found")

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string        { return e.msg }
func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }
```

In `locate`, replace the three error returns (messages unchanged):

```go
			return Source{}, &notFoundError{fmt.Sprintf("config file %s not found", flagPath)}
```
```go
			return Source{}, &notFoundError{fmt.Sprintf("config file %s (from %s) not found", p, ConfigEnvVar)}
```
```go
	return Source{}, &notFoundError{"no config file found; looked in:\n  " + strings.Join(searched, "\n  ") +
		"\npass -config <path>, set " + ConfigEnvVar + ", or create one of these files from config.example.toml"}
```

Add:

```go
// SetupTarget lists where first-run setup may write a config file.
type SetupTarget struct {
	// Fixed is the file named by -config or WHATOMATE_CONFIG; setup writes
	// exactly there and relative paths stay relative to the working directory.
	Fixed string
	// User and System are the per-user and system locations, used when Fixed
	// is empty. BaseDir is each one's data folder.
	User, System []Source
}

// Target returns where setup may write the config file for flagPath.
func Target(flagPath string) SetupTarget {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return target(flagPath, locateEnv{goos: runtime.GOOS, getenv: os.Getenv, cwd: cwd, home: home})
}

func target(flagPath string, e locateEnv) SetupTarget {
	if flagPath != "" {
		if !filepath.IsAbs(flagPath) {
			flagPath = filepath.Join(e.cwd, flagPath)
		}
		return SetupTarget{Fixed: flagPath}
	}
	if p := e.getenv(ConfigEnvVar); p != "" {
		return SetupTarget{Fixed: e.abs(p)}
	}
	toSources := func(cs []candidate) []Source {
		out := make([]Source, 0, len(cs))
		for _, c := range cs {
			out = append(out, Source{Path: c.path, BaseDir: c.base})
		}
		return out
	}
	return SetupTarget{User: toSources(e.userCandidates()), System: toSources(e.systemCandidates())}
}
```

- [ ] **Step 4: Run the locate tests**

Run: `go test ./internal/config/ -count=1`
Expected: PASS (existing locate tests still pass; messages are unchanged).

- [ ] **Step 5: Write the failing port-file tests**

Create `internal/config/portfile_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

func TestSetServerPort_OnlyServerTable(t *testing.T) {
	p := writeTemp(t, "# top comment\n[server]\nhost = \"0.0.0.0\"\nport = 8080 # keep me\n\n[database]\nport = 5432\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "# top comment\n[server]\nhost = \"0.0.0.0\"\nport = 9000 # keep me\n\n[database]\nport = 5432\n", read(t, p))
}

func TestSetServerPort_AddsKeyToExistingTable(t *testing.T) {
	p := writeTemp(t, "[server]\nhost = \"0.0.0.0\"\n[jwt]\nsecret = \"x\"\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[server]\nport = 9000\nhost = \"0.0.0.0\"\n[jwt]\nsecret = \"x\"\n", read(t, p))
}

func TestSetServerPort_AddsTable(t *testing.T) {
	p := writeTemp(t, "[app]\nname = \"x\"")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[app]\nname = \"x\"\n\n[server]\nport = 9000\n", read(t, p))
}

func TestSetServerPort_IgnoresSubTablesAndSimilarKeys(t *testing.T) {
	p := writeTemp(t, "[server]\nports = 1\nport_x = 2\n[server.extra]\nport = 3\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[server]\nport = 9000\nports = 1\nport_x = 2\n[server.extra]\nport = 3\n", read(t, p))
}

func TestSetServerPort_KeepsCRLF(t *testing.T) {
	p := writeTemp(t, "[server]\r\nport = 8080\r\n[database]\r\nport = 5432\r\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[server]\r\nport = 9000\r\n[database]\r\nport = 5432\r\n", read(t, p))
}

func TestSetServerPort_KeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	p := writeTemp(t, "[server]\nport = 1\n")
	require.NoError(t, os.Chmod(p, 0o640))
	require.NoError(t, SetServerPort(p, 2))
	fi, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm())
}

func TestReadServerPort(t *testing.T) {
	port, err := ReadServerPort(writeTemp(t, "[server]\nport = 9100\n[database]\nport = 5432\n"))
	require.NoError(t, err)
	assert.Equal(t, 9100, port)

	port, err = ReadServerPort(writeTemp(t, "[database]\nport = 5432\n"))
	require.NoError(t, err)
	assert.Equal(t, DefaultPort, port)
}

func TestWriteFileAtomic_CreatesFolderAndMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "config.toml")
	require.NoError(t, WriteFileAtomic(p, []byte("x"), 0o600))
	assert.Equal(t, "x", read(t, p))
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left behind")
}
```

- [ ] **Step 6: Run to see them fail**

Run: `go test ./internal/config/ -run 'TestSetServerPort|TestReadServerPort|TestWriteFileAtomic' -count=1`
Expected: FAIL to compile (`undefined: SetServerPort`).

- [ ] **Step 7: Implement `internal/config/portfile.go`**

```go
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	tableHeader = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	portLine    = regexp.MustCompile(`^(\s*)port\s*=\s*([^#]*?)(\s*#.*)?$`)
)

// WriteFileAtomic writes data to path through a temp file in the same folder
// and a rename, creating the folder (0750) if needed. When the folder can't
// take a temp file but path itself is writable, it rewrites path in place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".whatomate-*.tmp")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return os.WriteFile(path, data, perm)
		}
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// splitLines splits s into lines that keep their "\n" or "\r\n" endings.
func splitLines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func lineEnding(line string) (body, end string) {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return line[:len(line)-2], "\r\n"
	case strings.HasSuffix(line, "\n"):
		return line[:len(line)-1], "\n"
	}
	return line, ""
}

// serverPortLine finds the [server] table header and its port line. Either
// index is -1 when missing.
func serverPortLine(lines []string) (header, port int) {
	header, port = -1, -1
	inServer := false
	for i, line := range lines {
		body, _ := lineEnding(line)
		if m := tableHeader.FindStringSubmatch(body); m != nil {
			inServer = m[1] == "server"
			if inServer && header < 0 {
				header = i
			}
			continue
		}
		if inServer && port < 0 && portLine.MatchString(body) {
			port = i
		}
	}
	return header, port
}

// SetServerPort sets port in the [server] table of the TOML file at path and
// keeps every other byte. It adds the key, or the table, when missing.
func SetServerPort(path string, port int) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	nl := "\n"
	if len(lines) > 0 {
		if _, end := lineEnding(lines[0]); end != "" {
			nl = end
		}
	}
	value := strconv.Itoa(port)
	header, at := serverPortLine(lines)
	switch {
	case at >= 0:
		body, end := lineEnding(lines[at])
		m := portLine.FindStringSubmatch(body)
		lines[at] = m[1] + "port = " + value + m[3] + end
	case header >= 0:
		lines = append(lines[:header+1], append([]string{"port = " + value + nl}, lines[header+1:]...)...)
	default:
		if n := len(lines); n > 0 {
			if _, end := lineEnding(lines[n-1]); end == "" {
				lines[n-1] += nl
			}
		}
		lines = append(lines, nl, "[server]"+nl, "port = "+value+nl)
	}
	return WriteFileAtomic(path, []byte(strings.Join(lines, "")), fi.Mode().Perm())
}

// ReadServerPort returns [server] port from the TOML file at path, or
// DefaultPort when the file doesn't set it.
func ReadServerPort(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := splitLines(string(raw))
	_, at := serverPortLine(lines)
	if at < 0 {
		return DefaultPort, nil
	}
	body, _ := lineEnding(lines[at])
	m := portLine.FindStringSubmatch(body)
	port, err := strconv.Atoi(strings.TrimSpace(m[2]))
	if err != nil {
		return 0, fmt.Errorf("[server] port in %s is not a number", path)
	}
	return port, nil
}
```

- [ ] **Step 8: Run the config tests**

Run: `go test ./internal/config/ -count=1 && go vet ./internal/config/`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/config
git commit -m "feat(config): ErrNotFound, setup target and [server] port editing"
```

---

### Task 3: First admin only on an empty database, with an organization name

**Files:**
- Modify: `internal/config/config.go` (`DefaultAdminConfig`), `internal/database/database.go` (`CreateDefaultAdmin`)
- Test: `internal/database/database_test.go`

**Interfaces:**
- Produces: `config.DefaultAdminConfig.OrganizationName string` (koanf `organization_name`); `database.CreateDefaultAdmin` does nothing when any user exists.

- [ ] **Step 1: Write the failing tests**

Append to `internal/database/database_test.go`:

```go
func TestCreateDefaultAdmin_SkipsWhenAnyUserExists(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)
	testutil.CreateTestUser(t, db, org.ID, testutil.WithEmail("someone@example.com"))

	require.NoError(t, database.CreateDefaultAdmin(db, &config.DefaultAdminConfig{
		Email: "admin@admin.com", Password: "admin", FullName: "Admin",
	}))

	var n int64
	require.NoError(t, db.Model(&models.User{}).Where("email = ?", "admin@admin.com").Count(&n).Error)
	assert.Zero(t, n, "a database with users must not get the default admin")
}

func TestCreateDefaultAdmin_UsesOrganizationName(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.CreateDefaultAdmin(db, &config.DefaultAdminConfig{
		Email: "owner@example.com", Password: "password1", FullName: "Owner", OrganizationName: "Acme Ltd",
	}))

	var org models.Organization
	require.NoError(t, db.First(&org).Error)
	assert.Equal(t, "Acme Ltd", org.Name)
}
```

- [ ] **Step 2: Run to see them fail**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/database/ -run 'TestCreateDefaultAdmin' -count=1`
Expected: FAIL to compile (`unknown field OrganizationName`).

- [ ] **Step 3: Implement**

`internal/config/config.go`:

```go
type DefaultAdminConfig struct {
	Email    string `koanf:"email"`
	Password string `koanf:"password"`
	FullName string `koanf:"full_name"`
	// OrganizationName names the organization created with the first admin;
	// empty means "Default Organization".
	OrganizationName string `koanf:"organization_name"`
}
```

`internal/database/database.go`, replace the doc comment and the email check at the top of `CreateDefaultAdmin`:

```go
// CreateDefaultAdmin creates the first organization and a super-admin user
// when the database has no users yet.
func CreateDefaultAdmin(db *gorm.DB, cfg *config.DefaultAdminConfig) error {
	var users int64
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		return fmt.Errorf("failed to count users: %w", err)
	}
	if users > 0 {
		return nil
	}
```

and in the organization creation:

```go
		orgName := cfg.OrganizationName
		if orgName == "" {
			orgName = "Default Organization"
		}
		org = models.Organization{
			BaseModel: models.BaseModel{ID: uuid.New()},
			Name:      orgName,
			Settings:  models.JSONB{},
		}
```

Update the comment above the `CreateDefaultAdmin` call in `RunMigrationWithProgress` to `// Create the first admin (only when there are no users)`.

- [ ] **Step 4: Run the database tests on SQLite and PostgreSQL**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/database/ -count=1 && go test ./internal/database/ -count=1`
Expected: PASS (existing `TestCreateDefaultAdmin_*` still pass: they truncate first).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/database/database.go internal/database/database_test.go
git commit -m "fix(database): create the default admin only when no users exist"
```

---

### Task 4: Probe and create PostgreSQL/MySQL databases

**Files:**
- Create: `internal/database/provision.go`, `internal/database/provision_test.go`
- Modify: `test/testutil/db.go` (add `PostgresConfigFromURL`)

**Interfaces:**
- Produces:
  - `func database.ValidDatabaseName(name string) bool`
  - `func database.Probe(cfg *config.DatabaseConfig) (exists bool, err error)` — `exists=false, err=nil` when the server accepts the login but the database is missing.
  - `func database.CreateDatabase(cfg *config.DatabaseConfig) error`
  - `func testutil.PostgresConfigFromURL(raw string) (*config.DatabaseConfig, error)`

- [ ] **Step 1: Add the test helper**

Append to `test/testutil/db.go` (imports: `net/url`, `strconv` if missing):

```go
// PostgresConfigFromURL turns TEST_DATABASE_URL (postgres://user:pass@host:port/name?sslmode=…)
// into a postgres DatabaseConfig. The database name must start with whatomate_test.
func PostgresConfigFromURL(raw string) (*config.DatabaseConfig, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("TEST_DATABASE_URL does not parse: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !strings.HasPrefix(name, "whatomate_test") {
		return nil, fmt.Errorf("TEST_DATABASE_URL names database %q; it must start with whatomate_test", name)
	}
	port := 5432
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return nil, fmt.Errorf("TEST_DATABASE_URL port: %w", err)
		}
	}
	password, _ := u.User.Password()
	ssl := u.Query().Get("sslmode")
	if ssl == "" {
		ssl = "disable"
	}
	return &config.DatabaseConfig{
		Driver: config.DriverPostgres, Host: u.Hostname(), Port: port,
		User: u.User.Username(), Password: password, Name: name, SSLMode: ssl,
	}, nil
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/database/provision_test.go`:

```go
package database_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestValidDatabaseName(t *testing.T) {
	for name, ok := range map[string]bool{
		"whatomate": true, "whatomate_test_1a2b3c4d": true, "A1_b": true,
		"": false, "has-dash": false, "has space": false, `q"uote`: false, "semi;colon": false,
		"x123456789012345678901234567890123456789012345678901234567890123": false, // 64 chars
	} {
		assert.Equal(t, ok, database.ValidDatabaseName(name), name)
	}
}

// serverConfig returns the configured test server for the current driver.
func serverConfig(t *testing.T) *config.DatabaseConfig {
	t.Helper()
	var (
		cfg *config.DatabaseConfig
		err error
	)
	switch testutil.Driver() {
	case config.DriverMySQL:
		raw := os.Getenv("TEST_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_MYSQL_DSN not set")
		}
		cfg, err = testutil.MySQLConfigFromDSN(raw)
	case config.DriverSQLite:
		t.Skip("Probe/CreateDatabase are for database servers")
	default:
		raw := os.Getenv("TEST_DATABASE_URL")
		if raw == "" {
			t.Skip("TEST_DATABASE_URL not set")
		}
		cfg, err = testutil.PostgresConfigFromURL(raw)
	}
	require.NoError(t, err)
	return cfg
}

func dropDatabase(t *testing.T, server *config.DatabaseConfig, name string) {
	t.Helper()
	db, err := database.Open(server, false)
	require.NoError(t, err)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	stmt := `DROP DATABASE IF EXISTS "` + name + `"`
	if server.Driver == config.DriverMySQL {
		stmt = "DROP DATABASE IF EXISTS `" + name + "`"
	}
	require.NoError(t, db.Exec(stmt).Error)
}

func TestProbeAndCreateDatabase(t *testing.T) {
	server := serverConfig(t)

	exists, err := database.Probe(server)
	require.NoError(t, err)
	assert.True(t, exists, "the test database exists")

	fresh := *server
	fresh.Name = fmt.Sprintf("whatomate_test_%08x", rand.Uint32())
	t.Cleanup(func() { dropDatabase(t, server, fresh.Name) })

	exists, err = database.Probe(&fresh)
	require.NoError(t, err, "a missing database is not an error")
	assert.False(t, exists)

	require.NoError(t, database.CreateDatabase(&fresh))
	exists, err = database.Probe(&fresh)
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestProbe_BadPasswordIsAnError(t *testing.T) {
	server := serverConfig(t)
	bad := *server
	bad.Password = server.Password + "-wrong"
	_, err := database.Probe(&bad)
	assert.Error(t, err)
}

func TestCreateDatabase_RejectsBadName(t *testing.T) {
	err := database.CreateDatabase(&config.DatabaseConfig{Driver: config.DriverPostgres, Name: `x"; DROP`})
	assert.ErrorContains(t, err, "letters, digits or underscores")
}
```

- [ ] **Step 3: Run to see them fail**

Run: `go test ./internal/database/ -run 'TestValidDatabaseName|TestProbe|TestCreateDatabase' -count=1`
Expected: FAIL to compile (`undefined: database.ValidDatabaseName`).

- [ ] **Step 4: Implement `internal/database/provision.go`**

```go
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/trysoft/whatomate/internal/config"
)

var databaseName = regexp.MustCompile(`^[A-Za-z0-9_]{1,63}$`)

// ValidDatabaseName reports whether CreateDatabase accepts name.
func ValidDatabaseName(name string) bool { return databaseName.MatchString(name) }

// Probe connects with cfg. It returns exists=false and no error when the
// server accepts the login but the database does not exist yet.
func Probe(cfg *config.DatabaseConfig) (exists bool, err error) {
	db, err := Open(cfg, false)
	if err != nil {
		if isUnknownDatabase(err) {
			return false, nil
		}
		return false, err
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	return true, nil
}

func isUnknownDatabase(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "3D000" // invalid_catalog_name
	}
	var myErr *mysqldrv.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Number == 1049 // ER_BAD_DB_ERROR
	}
	return false
}

// CreateDatabase creates cfg.Name on a PostgreSQL or MySQL server. It
// connects to the "postgres" maintenance database, or to no database on MySQL.
func CreateDatabase(cfg *config.DatabaseConfig) error {
	if !ValidDatabaseName(cfg.Name) {
		return fmt.Errorf("database name %q: use 1-63 letters, digits or underscores", cfg.Name)
	}
	var driver, dsn, stmt string
	switch cfg.Driver {
	case "", config.DriverPostgres:
		ssl := cfg.SSLMode
		if ssl == "" {
			ssl = "disable"
		}
		driver = "pgx"
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, ssl)
		stmt = `CREATE DATABASE "` + cfg.Name + `"`
	case config.DriverMySQL:
		server := *cfg
		server.Name = ""
		var err error
		if dsn, err = mysqlDSN(&server, ""); err != nil {
			return err
		}
		driver = "mysql"
		stmt = "CREATE DATABASE `" + cfg.Name + "` CHARACTER SET utf8mb4"
	default:
		return fmt.Errorf("creating a %s database is not supported", cfg.Driver)
	}
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("failed to create database %s: %w", cfg.Name, err)
	}
	return nil
}
```

(`pgx` is registered by `gorm.io/driver/postgres`, which imports `github.com/jackc/pgx/v5/stdlib`.)

- [ ] **Step 5: Run on every driver**

Run: `go mod tidy && go test ./internal/database/ -run 'TestValidDatabaseName|TestProbe|TestCreateDatabase' -count=1 -v`
Expected: PASS on PostgreSQL.

Run: `TEST_DB_DRIVER=mysql go test ./internal/database/ -run 'TestProbe|TestCreateDatabase' -count=1 -v` (with `TEST_MYSQL_DSN` set)
Expected: PASS. Then `TEST_DB_DRIVER=sqlite go test ./internal/database/ -count=1` → PASS (server tests skip).

Confirm `go.mod` shows no new module, only `golang.org/x/sys`/`pgx` moving out of `// indirect` if at all.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/database/provision.go internal/database/provision_test.go test/testutil/db.go
git commit -m "feat(database): probe and create PostgreSQL/MySQL databases"
```

---

### Task 5: Setup config file and browser origin

**Files:**
- Create: `internal/setup/configfile.go`, `internal/setup/configfile_test.go`, `internal/setup/origin.go`, `internal/setup/origin_test.go`

**Interfaces:**
- Produces (package `setup`, unexported unless noted):
  - `type Values struct { Environment string; Port int; AllowedOrigins string; Database config.DatabaseConfig; JWTSecret, EncryptionKey string }` (exported)
  - `func Render(v Values, now time.Time) []byte` (exported)
  - `func quote(s string) string`, `func newSecret(n int) (string, error)`, `func storedDBPath(path, base string) string`
  - `func browserOrigin(ctx *fasthttp.RequestCtx, port, listenPort int) (origin string, https bool)`
  - `func environmentFor(https bool) string`

- [ ] **Step 1: Write the failing tests**

`internal/setup/configfile_test.go`:

```go
package setup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
)

func loadRendered(t *testing.T, v Values) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, Render(v, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	return cfg
}

func TestRender_SQLite(t *testing.T) {
	cfg := loadRendered(t, Values{
		Environment: "development", Port: 4747, AllowedOrigins: "http://localhost:4747",
		Database:  config.DatabaseConfig{Driver: config.DriverSQLite, Path: "whatomate.db"},
		JWTSecret: "jwt-secret", EncryptionKey: "enc-key",
	})
	assert.Equal(t, "development", cfg.App.Environment)
	assert.Equal(t, "enc-key", cfg.App.EncryptionKey)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 4747, cfg.Server.Port)
	assert.Equal(t, "http://localhost:4747", cfg.Server.AllowedOrigins)
	assert.Equal(t, config.DriverSQLite, cfg.Database.Driver)
	assert.Equal(t, "whatomate.db", cfg.Database.Path)
	assert.Equal(t, "jwt-secret", cfg.JWT.Secret)
	assert.Empty(t, cfg.Redis.Host)
}

func TestRender_RoundTripsAwkwardValues(t *testing.T) {
	awkward := "p\"a\\ss\nword\t ü 🔑 \x01 '#"
	cfg := loadRendered(t, Values{
		Environment: "production", Port: 9000, AllowedOrigins: "https://chat.example.com",
		Database: config.DatabaseConfig{
			Driver: config.DriverPostgres, Host: "db.local", Port: 5433,
			User: "u\"ser", Password: awkward, Name: "whatomate", SSLMode: "require",
		},
		JWTSecret: awkward, EncryptionKey: awkward,
	})
	assert.Equal(t, awkward, cfg.Database.Password)
	assert.Equal(t, "u\"ser", cfg.Database.User)
	assert.Equal(t, awkward, cfg.JWT.Secret)
	assert.Equal(t, awkward, cfg.App.EncryptionKey)
	assert.Equal(t, 5433, cfg.Database.Port)
	assert.Equal(t, "require", cfg.Database.SSLMode)
	assert.Equal(t, "production", cfg.App.Environment)
}

func TestRender_HasNoAdminSection(t *testing.T) {
	out := string(Render(Values{Database: config.DatabaseConfig{Driver: config.DriverSQLite, Path: "x.db"}}, time.Now()))
	assert.NotContains(t, out, "default_admin")
	assert.NotContains(t, out, "[redis]")
}

func TestNewSecret(t *testing.T) {
	a, err := newSecret(48)
	require.NoError(t, err)
	b, err := newSecret(48)
	require.NoError(t, err)
	assert.Len(t, a, 64)
	assert.NotEqual(t, a, b)
}

func TestStoredDBPath(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "data", "whatomate")
	assert.Equal(t, "whatomate.db", storedDBPath(filepath.Join(base, "whatomate.db"), base))
	assert.Equal(t, filepath.Join("db", "w.db"), storedDBPath(filepath.Join(base, "db", "w.db"), base))
	other := filepath.Join(string(filepath.Separator), "elsewhere", "w.db")
	assert.Equal(t, other, storedDBPath(other, base))
	assert.Equal(t, other, storedDBPath(other, ""), "no base keeps the absolute path")
}
```

`internal/setup/origin_test.go`:

```go
package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

func reqCtx(host string, headers map[string]string) *fasthttp.RequestCtx {
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetHost(host)
	ctx.Request.SetRequestURI("/api/setup/complete")
	for k, v := range headers {
		ctx.Request.Header.Set(k, v)
	}
	return &ctx
}

func TestBrowserOrigin(t *testing.T) {
	cases := []struct {
		name, host     string
		headers        map[string]string
		port, listen   int
		origin         string
		https          bool
	}{
		{"same port", "localhost:4747", nil, 4747, 4747, "http://localhost:4747", false},
		{"port change", "192.168.1.5:4747", nil, 9000, 4747, "http://192.168.1.5:9000", false},
		{"ipv6", "[::1]:4747", nil, 9000, 4747, "http://[::1]:9000", false},
		{"proxy https keeps proxy host", "127.0.0.1:4747",
			map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "chat.example.com"}, 9000, 4747,
			"https://chat.example.com", true},
		{"proxy first value", "127.0.0.1:4747",
			map[string]string{"X-Forwarded-Proto": "https, http", "X-Forwarded-Host": "a.example.com:8443, b"}, 4747, 4747,
			"https://a.example.com:8443", true},
		{"default port dropped", "example.com:80", nil, 80, 80, "http://example.com", false},
	}
	for _, c := range cases {
		origin, https := browserOrigin(reqCtx(c.host, c.headers), c.port, c.listen)
		assert.Equal(t, c.origin, origin, c.name)
		assert.Equal(t, c.https, https, c.name)
	}
}

func TestEnvironmentFor(t *testing.T) {
	assert.Equal(t, "production", environmentFor(true))
	assert.Equal(t, "development", environmentFor(false))
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/setup/ -count=1`
Expected: FAIL to compile (`undefined: Render`).

- [ ] **Step 3: Implement `internal/setup/configfile.go`**

```go
// Package setup serves the first-run setup wizard when no config file exists.
package setup

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/config"
)

// Values are the settings setup writes to a new config file.
type Values struct {
	Environment    string
	Port           int
	AllowedOrigins string
	Database       config.DatabaseConfig
	JWTSecret      string
	EncryptionKey  string
}

// Render returns the TOML config file for v.
func Render(v Values, now time.Time) []byte {
	var b strings.Builder
	kv := func(k, val string) { b.WriteString(k + " = " + val + "\n") }
	fmt.Fprintf(&b, "# Written by whatomate setup on %s.\n# config.example.toml lists every option.\n", now.Format("2006-01-02"))

	b.WriteString("\n[app]\n")
	kv("environment", quote(v.Environment))
	kv("encryption_key", quote(v.EncryptionKey))

	b.WriteString("\n[server]\n")
	kv("host", quote("0.0.0.0"))
	kv("port", strconv.Itoa(v.Port))
	kv("allowed_origins", quote(v.AllowedOrigins))

	d := v.Database
	b.WriteString("\n[database]\n")
	kv("driver", quote(d.Driver))
	if d.Driver == config.DriverSQLite {
		kv("path", quote(d.Path))
	} else {
		kv("host", quote(d.Host))
		kv("port", strconv.Itoa(d.Port))
		kv("user", quote(d.User))
		kv("password", quote(d.Password))
		kv("name", quote(d.Name))
		if d.SSLMode != "" {
			kv("ssl_mode", quote(d.SSLMode))
		}
	}

	b.WriteString("\n[jwt]\n")
	kv("secret", quote(v.JWTSecret))
	return []byte(b.String())
}

// quote returns s as a TOML basic string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// newSecret returns n random bytes, base64url-encoded without padding.
func newSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// storedDBPath writes a SQLite path relative to base when it lies inside it,
// so the config keeps working if the data folder moves with it.
func storedDBPath(path, base string) string {
	if base == "" {
		return path
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}
```

- [ ] **Step 4: Implement `internal/setup/origin.go`**

```go
package setup

import (
	"net"
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"
)

// browserOrigin returns the origin the browser used for ctx and whether it
// was HTTPS. Without proxy headers, a chosen port other than listenPort
// replaces the origin's port.
func browserOrigin(ctx *fasthttp.RequestCtx, port, listenPort int) (string, bool) {
	scheme := "http"
	if ctx.IsTLS() {
		scheme = "https"
	}
	proxied := false
	if p := firstValue(ctx.Request.Header.Peek("X-Forwarded-Proto")); p != "" {
		scheme, proxied = strings.ToLower(p), true
	}
	host := string(ctx.Request.Header.Host())
	if h := firstValue(ctx.Request.Header.Peek("X-Forwarded-Host")); h != "" {
		host, proxied = h, true
	}
	hostname, hostPort := splitHost(host)
	if !proxied && port != listenPort {
		hostPort = strconv.Itoa(port)
	}
	if (scheme == "http" && hostPort == "80") || (scheme == "https" && hostPort == "443") {
		hostPort = ""
	}
	https := scheme == "https"
	if hostPort != "" {
		return scheme + "://" + net.JoinHostPort(hostname, hostPort), https
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	return scheme + "://" + hostname, https
}

func firstValue(v []byte) string {
	s := string(v)
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func splitHost(h string) (host, port string) {
	if host, port, err := net.SplitHostPort(h); err == nil {
		return host, port
	}
	return strings.Trim(h, "[]"), ""
}

// environmentFor picks the config environment: production forces Secure
// cookies, which browsers drop on plain http://<lan-ip>.
func environmentFor(https bool) string {
	if https {
		return "production"
	}
	return "development"
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/setup/ -count=1 && go vet ./internal/setup/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/setup
git commit -m "feat(setup): config file rendering and browser origin"
```

---

### Task 6: System checks

**Files:**
- Create: `internal/netutil/port.go`, `internal/netutil/port_test.go`, `internal/setup/fs.go`, `internal/setup/checks.go`, `internal/setup/checks_test.go`, `internal/setup/sysinfo_linux.go`, `internal/setup/sysinfo_darwin.go`, `internal/setup/sysinfo_windows.go`, `internal/setup/sysinfo_other.go`

**Interfaces:**
- Produces:
  - `func netutil.PortFree(port int) bool`
  - `func setup.Writable(dir string) bool`, `func nearestExisting(path string) string`, `func fileExists(path string) bool`
  - `type setup.Status string` with `setup.Pass`, `setup.Warn`, `setup.Fail`
  - `type setup.CheckResult struct { ID string; Status Status; Detail string }` (json `id`, `status`, `detail`)
  - `type setup.CheckInput struct { Port, ListenPort int; ConfigDir, DataDir string }`
  - `type setup.System interface { TotalMemory() (uint64, error); FreeDisk(path string) (uint64, error) }`
  - `type setup.Checker struct { Version string; System System; HTTP *http.Client; ProbeURL string; LookPath func(string) (string, error); Now func() time.Time; PortFree func(int) bool }`
  - `func setup.NewChecker(version string) *Checker`, `func (c *Checker) Run(ctx context.Context, in CheckInput) []CheckResult`
  - Check IDs in order: `system`, `folders`, `disk`, `memory`, `port`, `internet`, `clock`, `audio`.

- [ ] **Step 1: Write the failing tests**

`internal/netutil/port_test.go`:

```go
package netutil

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	assert.False(t, PortFree(port))
	require.NoError(t, ln.Close())
	assert.True(t, PortFree(port))
}
```

`internal/setup/checks_test.go`:

```go
package setup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSystem struct {
	mem, disk uint64
	err       error
}

func (f fakeSystem) TotalMemory() (uint64, error)  { return f.mem, f.err }
func (f fakeSystem) FreeDisk(string) (uint64, error) { return f.disk, f.err }

func testChecker(t *testing.T, sys System, serverTime time.Time, missing ...string) *Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", serverTime.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return &Checker{
		Version: "v-test", System: sys, HTTP: srv.Client(), ProbeURL: srv.URL,
		LookPath: func(name string) (string, error) {
			for _, m := range missing {
				if m == name {
					return "", errors.New("not found")
				}
			}
			return "/usr/bin/" + name, nil
		},
		Now:      func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) },
		PortFree: func(port int) bool { return port != 5000 },
	}
}

func byID(results []CheckResult) map[string]CheckResult {
	out := map[string]CheckResult{}
	for _, r := range results {
		out[r.ID] = r
	}
	return out
}

func TestRun_AllGood(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Date(2026, 10, 13, 12, 0, 30, 0, time.UTC))
	res := c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: filepath.Join(dir, "data")})
	ids := make([]string, 0, len(res))
	for _, r := range res {
		ids = append(ids, r.ID)
		assert.Equal(t, Pass, r.Status, r.ID+": "+r.Detail)
	}
	assert.Equal(t, []string{"system", "folders", "disk", "memory", "port", "internet", "clock", "audio"}, ids)
	assert.Contains(t, byID(res)["system"].Detail, "v-test")
}

func TestRun_Warnings(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 256 << 20, disk: 100 << 20}, time.Date(2026, 10, 13, 12, 10, 0, 0, time.UTC), "ffmpeg")
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))
	assert.Equal(t, Warn, r["disk"].Status)
	assert.Equal(t, Warn, r["memory"].Status)
	assert.Equal(t, Warn, r["clock"].Status)
	assert.Contains(t, r["clock"].Detail, "10m0s")
	assert.Equal(t, Warn, r["audio"].Status)
	assert.Contains(t, r["audio"].Detail, "ffmpeg")
	assert.NotContains(t, r["audio"].Detail, "opusenc")
}

func TestRun_UnknownSystemValuesWarn(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{err: errors.New("nope")}, time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))
	assert.Equal(t, Warn, r["disk"].Status)
	assert.Equal(t, Warn, r["memory"].Status)
}

func TestRun_Port(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Now())
	run := func(port int) CheckResult {
		return byID(c.Run(context.Background(), CheckInput{Port: port, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))["port"]
	}
	assert.Equal(t, Pass, run(4747).Status, "the port setup is using")
	assert.Equal(t, Pass, run(6000).Status)
	assert.Equal(t, Fail, run(5000).Status, "in use")
	assert.Equal(t, Fail, run(0).Status)
	assert.Equal(t, Fail, run(70000).Status)
}

func TestRun_NoInternet(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Now())
	c.ProbeURL = "http://127.0.0.1:1"
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))
	assert.Equal(t, Warn, r["internet"].Status)
	assert.Equal(t, Warn, r["clock"].Status)
}

func TestRun_FolderNotWritableFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	require.NoError(t, os.Mkdir(locked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Now())
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: filepath.Join(locked, "whatomate"), DataDir: dir}))
	assert.Equal(t, Fail, r["folders"].Status)
	assert.Contains(t, r["folders"].Detail, locked)
}

func TestWritable_CreatesNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b")
	assert.True(t, Writable(target))
	_, err := os.Stat(filepath.Join(dir, "a"))
	assert.True(t, os.IsNotExist(err), "Writable must not create folders")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "Writable must not leave files")
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/netutil/ ./internal/setup/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `internal/netutil/port.go`**

```go
// Package netutil holds small network helpers shared by setup and handlers.
package netutil

import (
	"fmt"
	"net"
)

// PortFree reports whether a TCP listener can bind port on all interfaces.
func PortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
```

- [ ] **Step 4: Implement `internal/setup/fs.go`**

```go
package setup

import (
	"os"
	"path/filepath"
)

// nearestExisting returns path or its closest existing parent folder.
func nearestExisting(path string) string {
	p := path
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// Writable reports whether dir, or its nearest existing parent when dir does
// not exist yet, accepts new files. It leaves nothing behind.
func Writable(dir string) bool {
	d := nearestExisting(dir)
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		return false
	}
	f, err := os.CreateTemp(d, ".whatomate-write-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
```

- [ ] **Step 5: Implement `internal/setup/checks.go`**

```go
package setup

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/netutil"
)

// Status is a check outcome. Fail blocks setup; Warn does not.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
)

const (
	minFreeDisk   = 1 << 30   // 1 GB
	minMemory     = 512 << 20 // 512 MB
	maxClockDrift = 2 * time.Minute
)

// CheckResult is one line of the system check.
type CheckResult struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// CheckInput is what the user chose so far.
type CheckInput struct {
	Port, ListenPort   int
	ConfigDir, DataDir string
}

// System reads machine facts; tests substitute it.
type System interface {
	TotalMemory() (uint64, error)
	FreeDisk(path string) (uint64, error)
}

// Checker runs the system checks.
type Checker struct {
	Version  string
	System   System
	HTTP     *http.Client
	ProbeURL string
	LookPath func(string) (string, error)
	Now      func() time.Time
	PortFree func(int) bool
}

// NewChecker returns a Checker for this machine.
func NewChecker(version string) *Checker {
	return &Checker{
		Version:  version,
		System:   hostSystem{},
		HTTP:     &http.Client{Timeout: 5 * time.Second},
		ProbeURL: "https://graph.facebook.com/",
		LookPath: exec.LookPath,
		Now:      time.Now,
		PortFree: netutil.PortFree,
	}
}

// Run returns every check, in display order.
func (c *Checker) Run(ctx context.Context, in CheckInput) []CheckResult {
	out := []CheckResult{
		{ID: "system", Status: Pass, Detail: fmt.Sprintf("%s/%s, whatomate %s", runtime.GOOS, runtime.GOARCH, c.Version)},
		c.folders(in),
		c.disk(in.DataDir),
		c.memory(),
		c.port(in),
	}
	internet, clock := c.internet(ctx)
	return append(out, internet, clock, c.audio())
}

func (c *Checker) folders(in CheckInput) CheckResult {
	dirs := []string{in.ConfigDir}
	if in.DataDir != in.ConfigDir {
		dirs = append(dirs, in.DataDir)
	}
	var bad []string
	for _, d := range dirs {
		if !Writable(d) {
			bad = append(bad, d)
		}
	}
	if len(bad) > 0 {
		return CheckResult{ID: "folders", Status: Fail, Detail: "not writable: " + strings.Join(bad, ", ")}
	}
	return CheckResult{ID: "folders", Status: Pass, Detail: strings.Join(dirs, ", ")}
}

func (c *Checker) disk(dir string) CheckResult {
	free, err := c.System.FreeDisk(dir)
	switch {
	case err != nil:
		return CheckResult{ID: "disk", Status: Warn, Detail: "could not read free space: " + err.Error()}
	case free < minFreeDisk:
		return CheckResult{ID: "disk", Status: Warn, Detail: fmt.Sprintf("%s free; at least 1 GB recommended", size(free))}
	}
	return CheckResult{ID: "disk", Status: Pass, Detail: size(free) + " free"}
}

func (c *Checker) memory() CheckResult {
	total, err := c.System.TotalMemory()
	switch {
	case err != nil:
		return CheckResult{ID: "memory", Status: Warn, Detail: "could not read memory: " + err.Error()}
	case total < minMemory:
		return CheckResult{ID: "memory", Status: Warn, Detail: fmt.Sprintf("%s RAM; at least 512 MB recommended", size(total))}
	}
	return CheckResult{ID: "memory", Status: Pass, Detail: size(total) + " RAM"}
}

func (c *Checker) port(in CheckInput) CheckResult {
	switch {
	case in.Port < 1 || in.Port > 65535:
		return CheckResult{ID: "port", Status: Fail, Detail: "port must be between 1 and 65535"}
	case in.Port == in.ListenPort:
		return CheckResult{ID: "port", Status: Pass, Detail: fmt.Sprintf("%d (setup is using it)", in.Port)}
	case c.PortFree(in.Port):
		return CheckResult{ID: "port", Status: Pass, Detail: fmt.Sprintf("%d is free", in.Port)}
	}
	return CheckResult{ID: "port", Status: Fail, Detail: fmt.Sprintf("%d is in use by another program", in.Port)}
}

func (c *Checker) internet(ctx context.Context) (CheckResult, CheckResult) {
	host := c.ProbeURL
	if u, err := url.Parse(c.ProbeURL); err == nil {
		host = u.Host
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ProbeURL, nil)
	if err != nil {
		return CheckResult{ID: "internet", Status: Warn, Detail: err.Error()},
			CheckResult{ID: "clock", Status: Warn, Detail: "not checked (no internet)"}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return CheckResult{ID: "internet", Status: Warn, Detail: "can't reach " + host + ": " + err.Error()},
			CheckResult{ID: "clock", Status: Warn, Detail: "not checked (no internet)"}
	}
	_ = resp.Body.Close()
	inet := CheckResult{ID: "internet", Status: Pass, Detail: host + " is reachable"}
	date, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return inet, CheckResult{ID: "clock", Status: Warn, Detail: "the server sent no time to compare"}
	}
	drift := c.Now().Sub(date)
	if drift < 0 {
		drift = -drift
	}
	if drift > maxClockDrift {
		return inet, CheckResult{ID: "clock", Status: Warn, Detail: "clock is off by " + drift.Round(time.Second).String()}
	}
	return inet, CheckResult{ID: "clock", Status: Pass, Detail: "within 2 minutes of internet time"}
}

func (c *Checker) audio() CheckResult {
	var missing []string
	for _, tool := range []string{"ffmpeg", "opusenc"} {
		if _, err := c.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return CheckResult{ID: "audio", Status: Warn, Detail: "not installed: " + strings.Join(missing, ", ") + " (calling hold music and TTS conversion won't work)"}
	}
	return CheckResult{ID: "audio", Status: Pass, Detail: "ffmpeg and opusenc found"}
}

func size(b uint64) string {
	const gb, mb = 1 << 30, 1 << 20
	if b >= gb {
		return fmt.Sprintf("%.1f GB", float64(b)/gb)
	}
	return fmt.Sprintf("%d MB", b/mb)
}
```

- [ ] **Step 6: Implement the per-OS system readers**

`internal/setup/sysinfo_linux.go`:

```go
//go:build linux

package setup

import "golang.org/x/sys/unix"

type hostSystem struct{}

func (hostSystem) TotalMemory() (uint64, error) {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0, err
	}
	return uint64(si.Totalram) * uint64(si.Unit), nil
}

func (hostSystem) FreeDisk(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(nearestExisting(path), &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
```

`internal/setup/sysinfo_darwin.go`:

```go
//go:build darwin

package setup

import "golang.org/x/sys/unix"

type hostSystem struct{}

func (hostSystem) TotalMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }

func (hostSystem) FreeDisk(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(nearestExisting(path), &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
```

`internal/setup/sysinfo_windows.go`:

```go
//go:build windows

package setup

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type hostSystem struct{}

type memoryStatusEx struct {
	Length, MemoryLoad                                    uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile    uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual      uint64
}

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func (hostSystem) TotalMemory() (uint64, error) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, err := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, err
	}
	return m.TotalPhys, nil
}

func (hostSystem) FreeDisk(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(nearestExisting(path))
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return free, nil
}
```

`internal/setup/sysinfo_other.go`:

```go
//go:build !linux && !darwin && !windows

package setup

import "errors"

type hostSystem struct{}

var errUnsupported = errors.New("not supported on this system")

func (hostSystem) TotalMemory() (uint64, error)    { return 0, errUnsupported }
func (hostSystem) FreeDisk(string) (uint64, error) { return 0, errUnsupported }
```

- [ ] **Step 7: Run tests and cross-compile**

Run: `go mod tidy && go test ./internal/netutil/ ./internal/setup/ -count=1 && go vet ./internal/setup/ ./internal/netutil/`
Expected: PASS.

Run: `for os in linux darwin windows freebsd; do CGO_ENABLED=0 GOOS=$os GOARCH=amd64 go build ./internal/setup/ || echo "FAIL $os"; done; CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./internal/setup/`
Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/netutil internal/setup
git commit -m "feat(setup): system checks"
```

---

### Task 7: Shared listener

**Files:**
- Create: `internal/setup/listener.go`, `internal/setup/listener_test.go`

**Interfaces:**
- Produces:
  - `func setup.Share(raw net.Listener) *Shared`
  - `func (s *Shared) View(owner bool) net.Listener` — takes connections until closed; closing an owner view also closes the socket.
  - `func (s *Shared) Close() error`

- [ ] **Step 1: Write the failing tests**

```go
package setup

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serveOnce(ln net.Listener, msg string) {
	c, err := ln.Accept()
	if err != nil {
		return
	}
	_, _ = c.Write([]byte(msg))
	_ = c.Close()
}

func dialRead(t *testing.T, addr net.Addr) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	b, err := io.ReadAll(c)
	require.NoError(t, err)
	return string(b)
}

func TestShared_ViewsTakeTurns(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := Share(raw)
	defer s.Close()

	first := s.View(false)
	go serveOnce(first, "first")
	assert.Equal(t, "first", dialRead(t, raw.Addr()))
	require.NoError(t, first.Close())

	second := s.View(true)
	go serveOnce(second, "second")
	assert.Equal(t, "second", dialRead(t, raw.Addr()))
	require.NoError(t, second.Close())

	_, err = net.DialTimeout("tcp", raw.Addr().String(), time.Second)
	assert.Error(t, err, "closing the owner view closes the socket")
}

func TestShared_ConnectionWaitsForNextView(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := Share(raw)
	defer s.Close()
	require.NoError(t, s.View(false).Close())

	go func() {
		time.Sleep(200 * time.Millisecond)
		serveOnce(s.View(true), "late")
	}()
	assert.Equal(t, "late", dialRead(t, raw.Addr()), "a connection made between views is served by the next one")
}

func TestShared_ClosedViewStopsAccepting(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := Share(raw)
	defer s.Close()
	v := s.View(false)
	done := make(chan error, 1)
	go func() {
		_, err := v.Accept()
		done <- err
	}()
	require.NoError(t, v.Close())
	select {
	case err := <-done:
		assert.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return after Close")
	}
	assert.Equal(t, raw.Addr(), v.Addr())
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/setup/ -run TestShared -count=1`
Expected: FAIL to compile (`undefined: Share`).

- [ ] **Step 3: Implement `internal/setup/listener.go`**

```go
package setup

import (
	"net"
	"sync"
)

// Shared accepts on one socket and hands each connection to whichever view
// is accepting, so the socket outlives the server that used it first.
// Connections that arrive while no view is accepting wait for the next one.
type Shared struct {
	raw   net.Listener
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// Share starts accepting on raw.
func Share(raw net.Listener) *Shared {
	s := &Shared{raw: raw, conns: make(chan net.Conn), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *Shared) loop() {
	for {
		c, err := s.raw.Accept()
		if err != nil {
			_ = s.Close()
			return
		}
		select {
		case s.conns <- c:
		case <-s.done:
			_ = c.Close()
			return
		}
	}
}

// Close closes the socket and every view.
func (s *Shared) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.raw.Close()
	})
	return err
}

// View returns a listener that takes connections from s until it is closed.
// Closing an owner view also closes s.
func (s *Shared) View(owner bool) net.Listener {
	return &view{s: s, owner: owner, done: make(chan struct{})}
}

type view struct {
	s     *Shared
	owner bool
	done  chan struct{}
	once  sync.Once
}

func (v *view) Accept() (net.Conn, error) {
	select {
	case <-v.done:
		return nil, net.ErrClosed
	case <-v.s.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case c := <-v.s.conns:
		return c, nil
	case <-v.done:
		return nil, net.ErrClosed
	case <-v.s.done:
		return nil, net.ErrClosed
	}
}

func (v *view) Close() error {
	v.once.Do(func() { close(v.done) })
	if v.owner {
		return v.s.Close()
	}
	return nil
}

func (v *view) Addr() net.Addr { return v.s.raw.Addr() }
```

- [ ] **Step 4: Run the tests (with the race detector)**

Run: `go test ./internal/setup/ -run TestShared -race -count=3`
Expected: PASS. (`-race` needs cgo; if the toolchain lacks it, run without `-race`.)

- [ ] **Step 5: Commit**

```bash
git add internal/setup/listener.go internal/setup/listener_test.go
git commit -m "feat(setup): shared listener for the setup-to-app hand-off"
```

---

### Task 8: Setup server, completion and `Run`

**Files:**
- Create: `internal/setup/server.go`, `internal/setup/complete.go`, `internal/setup/browser.go`, `internal/setup/server_test.go`, `internal/setup/run_test.go`

**Interfaces:**
- Consumes: Tasks 2–7 (`config.SetupTarget`, `config.WriteFileAtomic`, `config.DefaultAdminConfig.OrganizationName`, `database.Open/Probe/CreateDatabase/ValidDatabaseName/RunMigrationWithProgress`, `Render`, `browserOrigin`, `Checker`, `Shared`, `Writable`)
- Produces:
  - `type setup.Options struct { Version string; Target config.SetupTarget; Port int; Log logf.Logger; Checker *Checker; OpenBrowser func(string) error; MovedGrace time.Duration }`
  - `type setup.Result struct { Listener net.Listener }`
  - `func setup.Run(ctx context.Context, o Options) (Result, error)`
  - HTTP (envelope `{"status":"success","data":…}` / `{"status":"error","message":…}`):
    - `GET /api/setup/status`
    - `GET /api/setup/checks?port=&config_path=` → `{checks: CheckResult[]}`
    - `POST /api/setup/database/test` → `{ok, database_exists, error?}`
    - `POST /api/setup/database/create` → `{ok, error?}`
    - `POST /api/setup/complete` → `{url}`
  - Status JSON: `{needed, ready, url?, version?, listen_port?, config_path?, config_path_fixed?, candidates?: [{path, data_dir, scope: "user"|"system"|"fixed", writable}]}`

- [ ] **Step 1: Write the failing handler tests**

`internal/setup/server_test.go`:

```go
package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/logf"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type env struct {
	s          *server
	dir        string
	configPath string
	dbPath     string
}

func newEnv(t *testing.T) env {
	t.Helper()
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	o := Options{
		Version: "test",
		Log:     logf.New(logf.Opts{}),
		Target: config.SetupTarget{
			User:   []config.Source{{Path: filepath.Join(userDir, "config.toml"), BaseDir: userDir}},
			System: []config.Source{{Path: filepath.Join(dir, "missing-root", "etc", "config.toml"), BaseDir: filepath.Join(dir, "missing-root", "var")}},
		},
		Checker: &Checker{PortFree: func(int) bool { return true }},
	}
	// The system location is "not writable" for this test: its root is a file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "missing-root"), []byte("x"), 0o600))
	return env{s: newServer(o, 4747), dir: dir, configPath: filepath.Join(userDir, "config.toml"), dbPath: filepath.Join(userDir, "whatomate.db")}
}

func call(t *testing.T, s *server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(path)
	ctx.Request.Header.SetHost("localhost:4747")
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		ctx.Request.SetBody(b)
		ctx.Request.Header.SetContentType("application/json")
	}
	s.handle(&ctx)
	var out map[string]any
	_ = json.Unmarshal(ctx.Response.Body(), &out)
	return ctx.Response.StatusCode(), out
}

func data(out map[string]any) map[string]any {
	d, _ := out["data"].(map[string]any)
	return d
}

func completeBody(e env) map[string]any {
	return map[string]any{
		"config_path":  e.configPath,
		"port":         4747,
		"database":     map[string]any{"driver": "sqlite", "path": e.dbPath},
		"organization": "Acme Ltd",
		"admin":        map[string]any{"full_name": "Ana Owner", "email": "ana@example.com", "password": "correct-horse-1"},
	}
}

func openSQLite(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := database.Open(&config.DatabaseConfig{Driver: config.DriverSQLite, Path: path}, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestStatus_BeforeSetup(t *testing.T) {
	e := newEnv(t)
	code, out := call(t, e.s, "GET", "/api/setup/status", nil)
	require.Equal(t, 200, code)
	d := data(out)
	assert.Equal(t, true, d["needed"])
	assert.Equal(t, float64(4747), d["listen_port"])
	assert.Equal(t, e.configPath, d["config_path"], "system not writable, so the user folder is chosen")
	assert.Len(t, d["candidates"], 2)
}

func TestChecks_RejectsUnknownLocation(t *testing.T) {
	e := newEnv(t)
	code, _ := call(t, e.s, "GET", "/api/setup/checks?port=4747&config_path=/etc/passwd", nil)
	assert.Equal(t, 400, code)
}

func TestDatabaseTest_SQLite(t *testing.T) {
	e := newEnv(t)
	code, out := call(t, e.s, "POST", "/api/setup/database/test", map[string]any{"driver": "sqlite", "path": e.dbPath})
	require.Equal(t, 200, code)
	assert.Equal(t, true, data(out)["ok"])
	_, err := os.Stat(e.dbPath)
	assert.True(t, os.IsNotExist(err), "testing must not create the database file")

	code, _ = call(t, e.s, "POST", "/api/setup/database/test", map[string]any{"driver": "sqlite", "path": "relative.db"})
	assert.Equal(t, 400, code)
}

func TestComplete_SQLiteWritesConfigAndAdmin(t *testing.T) {
	e := newEnv(t)
	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	require.Equal(t, 200, code, out)
	assert.Equal(t, "http://localhost:4747", data(out)["url"])

	fi, err := os.Stat(e.configPath)
	require.NoError(t, err)
	if os.PathSeparator == '/' {
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	cfg, err := config.Load(e.configPath)
	require.NoError(t, err)
	assert.Equal(t, "whatomate.db", cfg.Database.Path, "relative to the data folder")
	assert.Equal(t, 4747, cfg.Server.Port)
	assert.Equal(t, "development", cfg.App.Environment)
	assert.Equal(t, "http://localhost:4747", cfg.Server.AllowedOrigins)
	assert.GreaterOrEqual(t, len(cfg.JWT.Secret), 32)
	assert.NotEmpty(t, cfg.App.EncryptionKey)
	raw, _ := os.ReadFile(e.configPath)
	assert.NotContains(t, string(raw), "correct-horse-1", "the admin password is not stored")

	db := openSQLite(t, e.dbPath)
	var user models.User
	require.NoError(t, db.Where("email = ?", "ana@example.com").First(&user).Error)
	assert.True(t, user.IsSuperAdmin)
	assert.Equal(t, "Ana Owner", user.FullName)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("correct-horse-1")))
	var org models.Organization
	require.NoError(t, db.First(&org, "id = ?", user.OrganizationID).Error)
	assert.Equal(t, "Acme Ltd", org.Name)

	select {
	case f := <-e.s.done:
		assert.Equal(t, finished{port: 4747, url: "http://localhost:4747"}, f)
	default:
		t.Fatal("completion was not signalled")
	}
	_, out = call(t, e.s, "GET", "/api/setup/status", nil)
	assert.Equal(t, false, data(out)["needed"])
	assert.Equal(t, false, data(out)["ready"])
	assert.Equal(t, "http://localhost:4747", data(out)["url"])

	code, _ = call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code, "setup completes once")
}

func TestComplete_RefusesExistingConfig(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(e.configPath), 0o750))
	require.NoError(t, os.WriteFile(e.configPath, []byte("[app]\n"), 0o600))
	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code)
	assert.Contains(t, out["message"], "already exists")
	raw, _ := os.ReadFile(e.configPath)
	assert.Equal(t, "[app]\n", string(raw))
}

func TestComplete_RejectsDatabaseWithUsers(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(e.dbPath), 0o750))
	db := openSQLite(t, e.dbPath)
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{Email: "other@example.com", Password: "password", FullName: "Other"}))

	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code)
	assert.Contains(t, out["message"], "already has users")
	assert.NoFileExists(t, e.configPath)
	var n int64
	require.NoError(t, db.Model(&models.User{}).Where("is_super_admin = ?", true).Count(&n).Error)
	assert.Equal(t, int64(1), n, "no second super admin")
}

func TestComplete_RetryAppliesNewAdminDetails(t *testing.T) {
	e := newEnv(t)
	// The config folder can't be created: its path is taken by a file.
	blocker := filepath.Dir(e.configPath)
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	body := completeBody(e)
	body["database"] = map[string]any{"driver": "sqlite", "path": filepath.Join(e.dir, "db", "whatomate.db")}
	code, _ := call(t, e.s, "POST", "/api/setup/complete", body)
	require.Equal(t, 500, code)

	require.NoError(t, os.Remove(blocker))
	body["organization"] = "Renamed Ltd"
	body["admin"] = map[string]any{"full_name": "Ana Two", "email": "ana@example.com", "password": "second-password"}
	code, out := call(t, e.s, "POST", "/api/setup/complete", body)
	require.Equal(t, 200, code, out)

	db := openSQLite(t, filepath.Join(e.dir, "db", "whatomate.db"))
	var users []models.User
	require.NoError(t, db.Find(&users).Error)
	require.Len(t, users, 1)
	assert.Equal(t, "Ana Two", users[0].FullName)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte("second-password")))
	var org models.Organization
	require.NoError(t, db.First(&org, "id = ?", users[0].OrganizationID).Error)
	assert.Equal(t, "Renamed Ltd", org.Name)
}

func TestComplete_ValidatesInput(t *testing.T) {
	e := newEnv(t)
	mutate := map[string]func(map[string]any){
		"bad port":        func(b map[string]any) { b["port"] = 0 },
		"unknown path":    func(b map[string]any) { b["config_path"] = filepath.Join(e.dir, "elsewhere.toml") },
		"no organization": func(b map[string]any) { b["organization"] = "  " },
		"bad email":       func(b map[string]any) { b["admin"] = map[string]any{"full_name": "A", "email": "nope", "password": "longenough"} },
		"short password":  func(b map[string]any) { b["admin"] = map[string]any{"full_name": "A", "email": "a@b.co", "password": "short"} },
		"no full name":    func(b map[string]any) { b["admin"] = map[string]any{"full_name": "", "email": "a@b.co", "password": "longenough"} },
		"bad driver":      func(b map[string]any) { b["database"] = map[string]any{"driver": "sqlserver"} },
		"pg without host": func(b map[string]any) { b["database"] = map[string]any{"driver": "postgres", "user": "u", "name": "n"} },
	}
	for name, m := range mutate {
		body := completeBody(e)
		m(body)
		code, _ := call(t, e.s, "POST", "/api/setup/complete", body)
		assert.Equal(t, 400, code, name)
	}
	assert.NoFileExists(t, e.configPath)
}

func TestUnknownAPIIs404(t *testing.T) {
	e := newEnv(t)
	code, _ := call(t, e.s, "GET", "/api/contacts", nil)
	assert.Equal(t, 404, code)
	code, _ = call(t, e.s, "GET", "/api/setup/complete", nil)
	assert.Equal(t, 404, code, "complete is POST only")
}
```

- [ ] **Step 2: Write the failing `Run` tests**

`internal/setup/run_test.go`:

```go
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/logf"
)

type started struct {
	url string
	res chan Result
	err chan error
}

func startRun(t *testing.T, ctx context.Context, dir string) started {
	t.Helper()
	urls := make(chan string, 1)
	st := started{res: make(chan Result, 1), err: make(chan error, 1)}
	userDir := filepath.Join(dir, "user")
	go func() {
		res, err := Run(ctx, Options{
			Version: "test", Port: 0, Log: logf.New(logf.Opts{}),
			Target:      config.SetupTarget{User: []config.Source{{Path: filepath.Join(userDir, "config.toml"), BaseDir: userDir}}},
			OpenBrowser: func(u string) error { urls <- u; return nil },
			MovedGrace:  3 * time.Second,
		})
		st.res <- res
		st.err <- err
	}()
	select {
	case st.url = <-urls:
	case <-time.After(5 * time.Second):
		t.Fatal("setup did not start")
	}
	return st
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	return resp
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func runBody(dir, url string, port int) map[string]any {
	return map[string]any{
		"config_path":  filepath.Join(dir, "user", "config.toml"),
		"port":         port,
		"database":     map[string]any{"driver": "sqlite", "path": filepath.Join(dir, "user", "whatomate.db")},
		"organization": "Acme",
		"admin":        map[string]any{"full_name": "Ana", "email": "ana@example.com", "password": "password-1"},
	}
}

func TestRun_SamePortHandsOverListener(t *testing.T) {
	dir := t.TempDir()
	st := startRun(t, context.Background(), dir)
	var port int
	_, err := fmt.Sscanf(st.url, "http://localhost:%d", &port)
	require.NoError(t, err)

	resp := postJSON(t, st.url+"/api/setup/complete", runBody(dir, st.url, port))
	require.Equal(t, 200, resp.StatusCode)
	_ = resp.Body.Close()

	res := <-st.res
	require.NoError(t, <-st.err)
	require.NotNil(t, res.Listener)
	assert.Equal(t, port, res.Listener.Addr().(*net.TCPAddr).Port)

	app := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("app") }}
	go func() { _ = app.Serve(res.Listener) }()
	defer func() { _ = app.Shutdown() }()
	get, err := http.Get(st.url + "/anything")
	require.NoError(t, err)
	b, _ := io.ReadAll(get.Body)
	_ = get.Body.Close()
	assert.Equal(t, "app", string(b), "the app serves on the setup socket")
}

func TestRun_PortChangeLeavesRedirect(t *testing.T) {
	dir := t.TempDir()
	st := startRun(t, context.Background(), dir)
	newPort := freePort(t)

	resp := postJSON(t, st.url+"/api/setup/complete", runBody(dir, st.url, newPort))
	require.Equal(t, 200, resp.StatusCode)
	_ = resp.Body.Close()

	res := <-st.res
	require.NoError(t, <-st.err)
	assert.Nil(t, res.Listener, "the app binds the new port itself")

	get, err := http.Get(st.url + "/api/setup/status")
	require.NoError(t, err)
	var out struct {
		Data statusResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(get.Body).Decode(&out))
	_ = get.Body.Close()
	assert.True(t, out.Data.Ready)
	assert.Equal(t, fmt.Sprintf("http://localhost:%d", newPort), out.Data.URL)
}

func TestRun_CancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := startRun(t, ctx, t.TempDir())
	cancel()
	assert.ErrorIs(t, <-st.err, context.Canceled)
	_, err := http.Get(st.url + "/api/setup/status")
	assert.Error(t, err, "the socket is closed")
}
```

- [ ] **Step 3: Run to see them fail**

Run: `go test ./internal/setup/ -run 'TestStatus|TestChecks|TestDatabaseTest|TestComplete|TestUnknownAPI|TestRun' -count=1`
Expected: FAIL to compile (`undefined: newServer`).

- [ ] **Step 4: Implement `internal/setup/server.go`**

```go
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/frontend"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/logf"
)

// Options configures Run.
type Options struct {
	Version string
	Target  config.SetupTarget
	// Port is tried first; a free port is used when it is busy.
	Port int
	Log  logf.Logger
	// Checker defaults to NewChecker(Version).
	Checker *Checker
	// OpenBrowser defaults to the platform opener.
	OpenBrowser func(url string) error
	// MovedGrace is how long the setup port redirects after a port change
	// (default 2 minutes).
	MovedGrace time.Duration
}

// Result is how normal startup continues after setup.
type Result struct {
	// Listener is the setup socket for the app to serve on when the chosen
	// port is the one setup used; nil when the app must bind a new port.
	Listener net.Listener
}

// Run serves the setup wizard until it completes or ctx ends.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.Checker == nil {
		o.Checker = NewChecker(o.Version)
	}
	if o.OpenBrowser == nil {
		o.OpenBrowser = openBrowser
	}
	if o.MovedGrace == 0 {
		o.MovedGrace = 2 * time.Minute
	}
	raw, err := listen(o.Port)
	if err != nil {
		return Result{}, err
	}
	port := raw.Addr().(*net.TCPAddr).Port
	shared := Share(raw)
	s := newServer(o, port)
	srv := &fasthttp.Server{Handler: s.handle, Name: "Whatomate setup", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(shared.View(false)) }()

	urls := listenURLs(port)
	o.Log.Info("No config file found. Open the setup wizard to finish installing", "url", urls[0])
	for _, u := range urls[1:] {
		o.Log.Info("The setup wizard is also reachable at", "url", u)
	}
	if err := o.OpenBrowser(urls[0]); err != nil {
		o.Log.Debug("Could not open a browser", "error", err)
	}

	select {
	case <-ctx.Done():
		_ = srv.Shutdown()
		_ = shared.Close()
		return Result{}, ctx.Err()
	case f := <-s.done:
		_ = srv.Shutdown()
		if f.port == port {
			return Result{Listener: shared.View(true)}, nil
		}
		serveMoved(shared.View(true), f.url, o.MovedGrace)
		return Result{}, nil
	}
}

func listen(port int) (net.Listener, error) {
	if ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port)); err == nil {
		return ln, nil
	}
	return net.Listen("tcp", ":0")
}

type finished struct {
	port int
	url  string
}

type candidateInfo struct {
	Path     string `json:"path"`
	DataDir  string `json:"data_dir"`
	Scope    string `json:"scope"`
	Writable bool   `json:"writable"`
	base     string
}

type statusResponse struct {
	Needed          bool            `json:"needed"`
	Ready           bool            `json:"ready"`
	URL             string          `json:"url,omitempty"`
	Version         string          `json:"version,omitempty"`
	ListenPort      int             `json:"listen_port,omitempty"`
	ConfigPath      string          `json:"config_path,omitempty"`
	ConfigPathFixed bool            `json:"config_path_fixed,omitempty"`
	Candidates      []candidateInfo `json:"candidates,omitempty"`
}

type server struct {
	o          Options
	listenPort int
	frontend   fasthttp.RequestHandler
	done       chan finished

	completeMu sync.Mutex // one complete at a time
	stateMu    sync.Mutex
	finished   *finished
}

func newServer(o Options, listenPort int) *server {
	fe := fasthttp.RequestHandler(notEmbedded)
	if frontend.IsEmbedded() {
		fe = frontend.Handler("")
	}
	return &server{o: o, listenPort: listenPort, frontend: fe, done: make(chan finished, 1)}
}

func notEmbedded(ctx *fasthttp.RequestCtx) {
	ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
	ctx.SetContentType("text/plain; charset=utf-8")
	ctx.SetBodyString("This build has no embedded frontend. Run `npm run dev` in frontend/ with WHATOMATE_API_URL set to this server's address.")
}

func (s *server) handle(ctx *fasthttp.RequestCtx) {
	h := &ctx.Response.Header
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	path := string(ctx.Path())
	switch {
	case path == "/api/setup/status" && ctx.IsGet():
		s.status(ctx)
	case path == "/api/setup/checks" && ctx.IsGet():
		s.checks(ctx)
	case path == "/api/setup/database/test" && ctx.IsPost():
		s.databaseTest(ctx)
	case path == "/api/setup/database/create" && ctx.IsPost():
		s.databaseCreate(ctx)
	case path == "/api/setup/complete" && ctx.IsPost():
		s.complete(ctx)
	case strings.HasPrefix(path, "/api/") || path == "/ws":
		writeError(ctx, fasthttp.StatusNotFound, "whatomate is not set up yet")
	default:
		s.frontend(ctx)
	}
}

func writeData(ctx *fasthttp.RequestCtx, data any) {
	b, _ := json.Marshal(map[string]any{"status": "success", "data": data})
	ctx.SetContentType("application/json")
	ctx.SetBody(b)
}

func writeError(ctx *fasthttp.RequestCtx, code int, msg string) {
	b, _ := json.Marshal(map[string]any{"status": "error", "message": msg, "data": nil})
	ctx.SetStatusCode(code)
	ctx.SetContentType("application/json")
	ctx.SetBody(b)
}

func (s *server) isFinished() *finished {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.finished
}

func (s *server) candidates() []candidateInfo {
	if f := s.o.Target.Fixed; f != "" {
		dir := filepath.Dir(f)
		return []candidateInfo{{Path: f, DataDir: dir, Scope: "fixed", Writable: Writable(dir)}}
	}
	var out []candidateInfo
	add := func(scope string, srcs []config.Source) {
		for _, c := range srcs {
			out = append(out, candidateInfo{
				Path: c.Path, DataDir: c.BaseDir, Scope: scope, base: c.BaseDir,
				Writable: Writable(filepath.Dir(c.Path)) && Writable(c.BaseDir),
			})
		}
	}
	add("user", s.o.Target.User)
	add("system", s.o.Target.System)
	return out
}

// defaultChoice prefers a writable system location, then a writable user
// location, then the first user location.
func defaultChoice(cands []candidateInfo) string {
	for _, scope := range []string{"fixed", "system", "user"} {
		for _, c := range cands {
			if c.Scope == scope && (c.Writable || scope == "fixed") {
				return c.Path
			}
		}
	}
	if len(cands) > 0 {
		return cands[0].Path
	}
	return ""
}

func (s *server) lookup(path string) (candidateInfo, bool) {
	for _, c := range s.candidates() {
		if c.Path == path {
			return c, true
		}
	}
	return candidateInfo{}, false
}

func (s *server) status(ctx *fasthttp.RequestCtx) {
	if f := s.isFinished(); f != nil {
		writeData(ctx, statusResponse{URL: f.url})
		return
	}
	cands := s.candidates()
	writeData(ctx, statusResponse{
		Needed: true, Version: s.o.Version, ListenPort: s.listenPort,
		ConfigPath: defaultChoice(cands), ConfigPathFixed: s.o.Target.Fixed != "", Candidates: cands,
	})
}

func (s *server) checks(ctx *fasthttp.RequestCtx) {
	args := ctx.QueryArgs()
	port, _ := strconv.Atoi(string(args.Peek("port")))
	c, ok := s.lookup(string(args.Peek("config_path")))
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	results := s.o.Checker.Run(context.Background(), CheckInput{
		Port: port, ListenPort: s.listenPort, ConfigDir: filepath.Dir(c.Path), DataDir: c.DataDir,
	})
	writeData(ctx, map[string]any{"checks": results})
}

type databaseResult struct {
	OK             bool   `json:"ok"`
	DatabaseExists bool   `json:"database_exists"`
	Error          string `json:"error,omitempty"`
}

func (s *server) databaseTest(ctx *fasthttp.RequestCtx) {
	var in databaseInput
	if err := json.Unmarshal(ctx.PostBody(), &in); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	c := in.config()
	if err := validateDatabase(c); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if c.Driver == config.DriverSQLite {
		dir := filepath.Dir(c.Path)
		if !Writable(dir) {
			writeData(ctx, databaseResult{Error: dir + " is not writable"})
			return
		}
		writeData(ctx, databaseResult{OK: true, DatabaseExists: fileExists(c.Path)})
		return
	}
	exists, err := database.Probe(&c)
	if err != nil {
		writeData(ctx, databaseResult{Error: err.Error()})
		return
	}
	writeData(ctx, databaseResult{OK: true, DatabaseExists: exists})
}

func (s *server) databaseCreate(ctx *fasthttp.RequestCtx) {
	var in databaseInput
	if err := json.Unmarshal(ctx.PostBody(), &in); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	c := in.config()
	if err := validateDatabase(c); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if c.Driver == config.DriverSQLite {
		writeError(ctx, fasthttp.StatusBadRequest, "SQLite databases are created automatically")
		return
	}
	if !database.ValidDatabaseName(c.Name) {
		writeError(ctx, fasthttp.StatusBadRequest, "database name: use 1-63 letters, digits or underscores")
		return
	}
	if err := database.CreateDatabase(&c); err != nil {
		writeData(ctx, databaseResult{Error: err.Error()})
		return
	}
	writeData(ctx, databaseResult{OK: true, DatabaseExists: true})
}

func (s *server) complete(ctx *fasthttp.RequestCtx) {
	s.completeMu.Lock()
	defer s.completeMu.Unlock()
	if s.isFinished() != nil {
		writeError(ctx, fasthttp.StatusConflict, "setup is already complete")
		return
	}
	var req completeRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	origin, https := browserOrigin(ctx, req.Port, s.listenPort)
	if code, err := s.finish(req, origin, https); err != nil {
		writeError(ctx, code, err.Error())
		return
	}
	f := finished{port: req.Port, url: origin}
	s.stateMu.Lock()
	s.finished = &f
	s.stateMu.Unlock()
	s.done <- f
	writeData(ctx, map[string]string{"url": origin})
}
```

- [ ] **Step 5: Implement `internal/setup/complete.go`**

```go
package setup

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type databaseInput struct {
	Driver   string `json:"driver"`
	Path     string `json:"path"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Name     string `json:"name"`
	SSLMode  string `json:"ssl_mode"`
}

type completeRequest struct {
	ConfigPath   string        `json:"config_path"`
	Port         int           `json:"port"`
	Database     databaseInput `json:"database"`
	Organization string        `json:"organization"`
	Admin        struct {
		FullName string `json:"full_name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	} `json:"admin"`
}

func (d databaseInput) config() config.DatabaseConfig {
	c := config.DatabaseConfig{
		Driver: strings.ToLower(strings.TrimSpace(d.Driver)), Path: strings.TrimSpace(d.Path),
		Host: strings.TrimSpace(d.Host), Port: d.Port, User: d.User, Password: d.Password,
		Name: strings.TrimSpace(d.Name), SSLMode: d.SSLMode,
	}
	switch c.Driver {
	case config.DriverPostgres:
		if c.Port == 0 {
			c.Port = 5432
		}
		if c.SSLMode == "" {
			c.SSLMode = "disable"
		}
	case config.DriverMySQL:
		if c.Port == 0 {
			c.Port = 3306
		}
	}
	return c
}

func validateDatabase(c config.DatabaseConfig) error {
	switch c.Driver {
	case config.DriverSQLite:
		if c.Path == "" || !filepath.IsAbs(c.Path) {
			return errors.New("SQLite needs an absolute file path")
		}
		return nil
	case config.DriverPostgres, config.DriverMySQL:
		switch {
		case c.Host == "":
			return errors.New("host is required")
		case c.Port < 1 || c.Port > 65535:
			return errors.New("database port must be between 1 and 65535")
		case c.User == "":
			return errors.New("user is required")
		case c.Name == "":
			return errors.New("database name is required")
		}
		return nil
	}
	return fmt.Errorf("unsupported database %q; choose sqlite, postgres or mysql", c.Driver)
}

// finish validates req, prepares the database and admin, and writes the
// config file last, so any failure leaves no config behind. It returns the
// HTTP status to use with the error.
func (s *server) finish(req completeRequest, origin string, https bool) (int, error) {
	cand, ok := s.lookup(req.ConfigPath)
	if !ok {
		return fasthttp.StatusBadRequest, errors.New("unknown config location")
	}
	if req.Port < 1 || req.Port > 65535 {
		return fasthttp.StatusBadRequest, errors.New("port must be between 1 and 65535")
	}
	if req.Port != s.listenPort && !s.o.Checker.PortFree(req.Port) {
		return fasthttp.StatusBadRequest, fmt.Errorf("port %d is in use by another program", req.Port)
	}
	dbCfg := req.Database.config()
	if err := validateDatabase(dbCfg); err != nil {
		return fasthttp.StatusBadRequest, err
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
	if fileExists(cand.Path) {
		return fasthttp.StatusConflict, fmt.Errorf("%s already exists; restart whatomate to use it", cand.Path)
	}

	if dbCfg.Driver == config.DriverSQLite {
		if err := os.MkdirAll(filepath.Dir(dbCfg.Path), 0o750); err != nil {
			return fasthttp.StatusInternalServerError, fmt.Errorf("create %s: %w", filepath.Dir(dbCfg.Path), err)
		}
	}
	db, err := database.Open(&dbCfg, false)
	if err != nil {
		return fasthttp.StatusBadRequest, fmt.Errorf("connect to the database: %w", err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	if err := ensureNoOtherUsers(db, addr.Address); err != nil {
		return fasthttp.StatusConflict, err
	}
	admin := config.DefaultAdminConfig{Email: addr.Address, Password: req.Admin.Password, FullName: fullName, OrganizationName: org}
	if err := database.RunMigrationWithProgress(db, &admin); err != nil {
		return fasthttp.StatusInternalServerError, fmt.Errorf("set up the database: %w", err)
	}
	if err := applyAdmin(db, admin); err != nil {
		return fasthttp.StatusInternalServerError, err
	}

	jwtSecret, err := newSecret(48)
	if err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	encKey, err := newSecret(32)
	if err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	stored := dbCfg
	if stored.Driver == config.DriverSQLite {
		stored.Path = storedDBPath(dbCfg.Path, cand.base)
	}
	if err := os.MkdirAll(cand.DataDir, 0o750); err != nil {
		return fasthttp.StatusInternalServerError, fmt.Errorf("create %s: %w", cand.DataDir, err)
	}
	body := Render(Values{
		Environment: environmentFor(https), Port: req.Port, AllowedOrigins: origin,
		Database: stored, JWTSecret: jwtSecret, EncryptionKey: encKey,
	}, time.Now())
	if err := config.WriteFileAtomic(cand.Path, body, 0o600); err != nil {
		return fasthttp.StatusInternalServerError, fmt.Errorf("write %s: %w", cand.Path, err)
	}
	s.o.Log.Info("Setup complete", "config", cand.Path)
	return fasthttp.StatusOK, nil
}

// ensureNoOtherUsers refuses a database with users other than email.
func ensureNoOtherUsers(db *gorm.DB, email string) error {
	if !db.Migrator().HasTable(&models.User{}) {
		return nil
	}
	var n int64
	if err := db.Model(&models.User{}).Where("email <> ?", email).Count(&n).Error; err != nil {
		return fmt.Errorf("check existing users: %w", err)
	}
	if n > 0 {
		return errors.New("database already has users; choose an empty database")
	}
	return nil
}

// applyAdmin sets the wizard's password, name and organization name on the
// admin, so a retry uses the retry's values.
func applyAdmin(db *gorm.DB, a config.DefaultAdminConfig) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(a.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	var user models.User
	if err := db.Where("email = ?", a.Email).First(&user).Error; err != nil {
		return fmt.Errorf("find admin user: %w", err)
	}
	if err := db.Model(&user).Updates(map[string]any{"password_hash": string(hash), "full_name": a.FullName}).Error; err != nil {
		return fmt.Errorf("update admin user: %w", err)
	}
	if err := db.Model(&models.Organization{}).Where("id = ?", user.OrganizationID).Update("name", a.OrganizationName).Error; err != nil {
		return fmt.Errorf("update organization: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Implement `internal/setup/browser.go`**

```go
package setup

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"time"

	"github.com/valyala/fasthttp"
)

// openBrowser asks the desktop to open url.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// listenURLs returns the localhost URL first, then one per LAN IPv4 address.
func listenURLs(port int) []string {
	urls := []string{fmt.Sprintf("http://localhost:%d", port)}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || ipn.IP.To4() == nil {
			continue
		}
		urls = append(urls, fmt.Sprintf("http://%s:%d", ipn.IP, port))
	}
	return urls
}

// serveMoved answers on the old setup port for grace, pointing at url.
func serveMoved(ln net.Listener, url string, grace time.Duration) {
	srv := &fasthttp.Server{Name: "Whatomate setup", Handler: func(ctx *fasthttp.RequestCtx) {
		if string(ctx.Path()) == "/api/setup/status" {
			writeData(ctx, statusResponse{Ready: true, URL: url})
			return
		}
		ctx.Redirect(url+string(ctx.RequestURI()), fasthttp.StatusFound)
	}}
	go func() { _ = srv.Serve(ln) }()
	time.AfterFunc(grace, func() { _ = srv.Shutdown() })
}
```

- [ ] **Step 7: Run the setup tests**

Run: `go test ./internal/setup/ -count=1 && go vet ./internal/setup/`
Expected: PASS. (Migrations print a progress bar to stdout; that's expected.)

If `TestRun_SamePortHandsOverListener` fails because `http.Get` reuses a keep-alive connection closed by the setup server's shutdown, set `DisableKeepAlives: true` on a dedicated `http.Client` in `postJSON` — the browser retries on its own; the test must not depend on reuse.

- [ ] **Step 8: Commit**

```bash
git add internal/setup
git commit -m "feat(setup): setup server, completion and listener hand-off"
```

---

### Task 9: Wire setup into the binary

**Files:**
- Modify: `cmd/whatomate/main.go`, `internal/handlers/app.go` (App struct)
- Create: `internal/handlers/setup_status.go`, `internal/handlers/setup_status_test.go`

**Interfaces:**
- Consumes: `config.ErrNotFound`, `config.Target`, `config.DefaultPort`, `setup.Run`, `setup.Options`
- Produces:
  - `handlers.App.ConfigPath string` — the config file the app loaded.
  - `func (a *App) SetupStatus(r *fastglue.Request) error` → `{needed:false, ready:true}`; route `GET /api/setup/status` (public).
  - `loadConfig(flagPath, lo) (*config.Config, string)` in `main.go`.

- [ ] **Step 1: Write the failing handler test**

`internal/handlers/setup_status_test.go`:

```go
package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestSetupStatus_DoneInNormalMode(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	req := testutil.NewGETRequest(t)
	require.NoError(t, app.SetupStatus(req))
	var resp struct {
		Data struct {
			Needed bool `json:"needed"`
			Ready  bool `json:"ready"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.False(t, resp.Data.Needed)
	assert.True(t, resp.Data.Ready)
}
```

- [ ] **Step 2: Run to see it fail**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/handlers/ -run TestSetupStatus -count=1`
Expected: FAIL to compile (`app.SetupStatus undefined`).

- [ ] **Step 3: Implement the handler and the App field**

`internal/handlers/setup_status.go`:

```go
package handlers

import "github.com/zerodha/fastglue"

// SetupStatus answers the frontend's first-run check once whatomate is set up.
func (a *App) SetupStatus(r *fastglue.Request) error {
	return r.SendEnvelope(map[string]bool{"needed": false, "ready": true})
}
```

In `internal/handlers/app.go`, add to `App` after `Config`:

```go
	// ConfigPath is the config file the app loaded ("" when none).
	ConfigPath string
```

- [ ] **Step 4: Wire `main.go`**

1. `main()` — no arguments runs the server:

```go
func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"server"}
	}

	switch args[0] {
	case "server":
		runServer(args[1:])
	case "worker":
		runWorker(args[1:])
	case "config":
		runConfig(args[1:])
	case "version":
		fmt.Printf("Whatomate %s (built %s)\n", Version, BuildTime)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}
```

2. `printUsage`: change `whatomate <command> [options]` to `whatomate [command] [options]` and add after the Commands list:

```
With no command, whatomate runs "server". When no config file is found,
server opens a setup wizard in the browser (port 4747) and writes one.
```

3. `runServer`, replace `cfg := loadConfig(*configPath, lo)` with:

```go
	var handoff net.Listener
	if _, err := config.Locate(*configPath); errors.Is(err, config.ErrNotFound) {
		handoff = runSetup(*configPath, lo)
	}
	cfg, cfgPath := loadConfig(*configPath, lo)
```

4. In the `handlers.App{...}` literal add `ConfigPath: cfgPath,`.

5. Replace the server start goroutine:

```go
	// Start server in goroutine
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	go func() {
		var err error
		if handoff != nil {
			lo.Info("Server listening", "address", handoff.Addr().String())
			err = server.Serve(handoff)
		} else {
			lo.Info("Server listening", "address", addr)
			err = server.ListenAndServe(addr)
		}
		if err != nil {
			lo.Fatal("Server failed", "error", err)
		}
	}()
```

6. Add `runSetup` next to `loadConfig`:

```go
// runSetup serves the first-run setup wizard. It returns the setup socket
// for the app to keep serving on, or nil when the app binds its own port.
func runSetup(flagPath string, lo logf.Logger) net.Listener {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, err := setup.Run(ctx, setup.Options{
		Version: Version,
		Target:  config.Target(flagPath),
		Port:    config.DefaultPort,
		Log:     lo,
	})
	if errors.Is(err, context.Canceled) {
		lo.Info("Setup cancelled; nothing was written")
		os.Exit(0)
	}
	if err != nil {
		lo.Fatal("Setup failed", "error", err)
	}
	return res.Listener
}
```

7. `loadConfig` returns the path and explains a missing config:

```go
// loadConfig finds and loads the config file, anchoring relative paths in a
// config from the user or system folder to its base folder. It returns the
// config and the file it came from.
func loadConfig(flagPath string, lo logf.Logger) (*config.Config, string) {
	src, err := config.Locate(flagPath)
	if errors.Is(err, config.ErrNotFound) {
		lo.Fatal("No config file found. Run `whatomate server` once to open the setup wizard", "error", err)
	}
	if err != nil {
		lo.Fatal("Failed to find config", "error", err)
	}
	// ...existing Load / MkdirAll / ResolvePaths / log lines unchanged...
	return cfg, src.Path
}
```

8. `runWorker`: `cfg, _ := loadConfig(*configPath, lo)`.

9. Imports: add `"errors"`, `"net"`, `"github.com/trysoft/whatomate/internal/setup"`.

10. `setupRoutes`: next to `/health`, add `g.GET("/api/setup/status", app.SetupStatus)`, and in the auth-skip `if` add `path == "/api/setup/status" ||`.

- [ ] **Step 5: Build, vet and test**

Run: `gofmt -l cmd internal && go vet ./cmd/... ./internal/... && CGO_ENABLED=0 go build -o /tmp/whatomate-setup ./cmd/whatomate && TEST_DB_DRIVER=sqlite go test ./internal/handlers/ -run TestSetupStatus -count=1`
Expected: no gofmt output, PASS.

- [ ] **Step 6: Smoke-test the binary (no browser needed)**

```bash
tmp=$(mktemp -d) && cd "$tmp" && HOME="$tmp/home" XDG_CONFIG_HOME="$tmp/xdg" /tmp/whatomate-setup worker 2>&1 | head -3; cd -
```
Expected: "No config file found. Run `whatomate server` once to open the setup wizard".

```bash
tmp=$(mktemp -d) && (cd "$tmp" && HOME="$tmp/home" XDG_CONFIG_HOME="$tmp/xdg" /tmp/whatomate-setup -config "$tmp/c.toml" > "$tmp/log" 2>&1 &) ; sleep 2; curl -s localhost:4747/api/setup/status; pkill -f "whatomate-setup -config $tmp" ; rm -rf "$tmp"
```
Expected: JSON with `"needed":true` and `"config_path":"<tmp>/c.toml"`, `"config_path_fixed":true`. (If port 4747 is taken on this machine, read the URL from `$tmp/log`.) A browser tab may open; close it.

- [ ] **Step 7: Commit**

```bash
git add cmd/whatomate/main.go internal/handlers/app.go internal/handlers/setup_status.go internal/handlers/setup_status_test.go
git commit -m "feat: run the setup wizard when no config file exists"
```

---

### Task 10: Server settings API (port)

**Files:**
- Create: `internal/handlers/server_settings.go`, `internal/handlers/server_settings_test.go`
- Modify: `cmd/whatomate/main.go` (routes next to `/api/admin/meta-app`)

**Interfaces:**
- Consumes: `App.ConfigPath`, `config.SetServerPort`, `config.ReadServerPort`, `netutil.PortFree`, `a.requireSuperAdmin`, `a.decodeRequest`
- Produces:
  - `type handlers.ServerSettingsResponse struct { Port, ConfigPort int; ConfigPath string; Writable bool }` (json `port`, `config_port`, `config_path`, `writable`)
  - `GET /api/admin/server-settings` → `ServerSettingsResponse`; `PUT /api/admin/server-settings` `{port}` → `ServerSettingsResponse` (super admin only)

- [ ] **Step 1: Write the failing tests**

`internal/handlers/server_settings_test.go`:

```go
package handlers_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func serverSettingsApp(t *testing.T) (*handlers.App, string, uuid.UUID, uuid.UUID) {
	t.Helper()
	app := newTestApp(t)
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte("[server]\n# the port\nport = 4747\n\n[database]\nport = 5432\n"), 0o600))
	app.ConfigPath = p
	app.Config.Server.Port = 4747
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("srv-admin")), testutil.WithSuperAdmin())
	return app, p, org.ID, admin.ID
}

func settingsData(t *testing.T, req *fastglue.Request) handlers.ServerSettingsResponse {
	t.Helper()
	var resp struct {
		Data handlers.ServerSettingsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func TestServerSettings_RequiresSuperAdmin(t *testing.T) {
	app, _, orgID, _ := serverSettingsApp(t)
	user := testutil.CreateTestUser(t, app.DB, orgID, testutil.WithEmail(testutil.UniqueEmail("srv-user")))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, user.ID)
	require.NoError(t, app.GetServerSettings(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))

	put := testutil.NewJSONRequest(t, map[string]any{"port": 9000})
	testutil.SetAuthContext(put, orgID, user.ID)
	require.NoError(t, app.UpdateServerSettings(put))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(put))
}

func TestServerSettings_GetAndUpdate(t *testing.T) {
	app, path, orgID, adminID := serverSettingsApp(t)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, adminID)
	require.NoError(t, app.GetServerSettings(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	got := settingsData(t, req)
	assert.Equal(t, handlers.ServerSettingsResponse{Port: 4747, ConfigPort: 4747, ConfigPath: path, Writable: true}, got)

	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	free := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	put := testutil.NewJSONRequest(t, map[string]any{"port": free})
	testutil.SetAuthContext(put, orgID, adminID)
	require.NoError(t, app.UpdateServerSettings(put))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(put))
	updated := settingsData(t, put)
	assert.Equal(t, 4747, updated.Port, "the running port is unchanged until restart")
	assert.Equal(t, free, updated.ConfigPort)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "# the port\n")
	assert.Contains(t, string(raw), "[database]\nport = 5432\n")
}

func TestServerSettings_RejectsBadPorts(t *testing.T) {
	app, _, orgID, adminID := serverSettingsApp(t)
	busy, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer busy.Close()

	for _, port := range []int{0, 70000, busy.Addr().(*net.TCPAddr).Port} {
		put := testutil.NewJSONRequest(t, map[string]any{"port": port})
		testutil.SetAuthContext(put, orgID, adminID)
		require.NoError(t, app.UpdateServerSettings(put))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(put), port)
	}
}

func TestServerSettings_NoConfigFile(t *testing.T) {
	app, _, orgID, adminID := serverSettingsApp(t)
	app.ConfigPath = ""
	put := testutil.NewJSONRequest(t, map[string]any{"port": 4747})
	testutil.SetAuthContext(put, orgID, adminID)
	require.NoError(t, app.UpdateServerSettings(put))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(put))
}
```

If `newTestApp` shares one `App` config between parallel tests, do not use `t.Parallel()` here — these tests mutate `app.Config.Server.Port` and `app.ConfigPath`.

- [ ] **Step 2: Run to see them fail**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/handlers/ -run TestServerSettings -count=1`
Expected: FAIL to compile (`app.GetServerSettings undefined`).

- [ ] **Step 3: Implement `internal/handlers/server_settings.go`**

```go
package handlers

import (
	"fmt"
	"os"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/netutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ServerSettingsResponse describes the install-wide server settings.
type ServerSettingsResponse struct {
	// Port is the port the app is running on; ConfigPort is the one in the
	// config file, which takes effect after a restart.
	Port       int    `json:"port"`
	ConfigPort int    `json:"config_port"`
	ConfigPath string `json:"config_path"`
	Writable   bool   `json:"writable"`
}

func (a *App) serverSettings() ServerSettingsResponse {
	resp := ServerSettingsResponse{Port: a.Config.Server.Port, ConfigPort: a.Config.Server.Port, ConfigPath: a.ConfigPath}
	if a.ConfigPath == "" {
		return resp
	}
	if p, err := config.ReadServerPort(a.ConfigPath); err == nil {
		resp.ConfigPort = p
	}
	if f, err := os.OpenFile(a.ConfigPath, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		resp.Writable = true
	}
	return resp
}

// GetServerSettings returns the server port and config file. Super admin only.
func (a *App) GetServerSettings(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	return r.SendEnvelope(a.serverSettings())
}

// UpdateServerSettings saves a new port to the config file; it applies after
// a restart. Super admin only.
func (a *App) UpdateServerSettings(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	var req struct {
		Port int `json:"port"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Port < 1 || req.Port > 65535 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Port must be between 1 and 65535", nil, "")
	}
	if a.ConfigPath == "" {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "No config file is loaded", nil, "")
	}
	if req.Port != a.Config.Server.Port && !netutil.PortFree(req.Port) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, fmt.Sprintf("Port %d is in use by another program", req.Port), nil, "")
	}
	if err := config.SetServerPort(a.ConfigPath, req.Port); err != nil {
		a.Log.Error("Failed to save server port", "path", a.ConfigPath, "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, fmt.Sprintf("Could not save %s: %v", a.ConfigPath, err), nil, "")
	}
	return r.SendEnvelope(a.serverSettings())
}
```

In `cmd/whatomate/main.go`, after the `/api/admin/meta-app` routes:

```go
	g.GET("/api/admin/server-settings", app.GetServerSettings)
	g.PUT("/api/admin/server-settings", app.UpdateServerSettings)
```

- [ ] **Step 4: Run tests on SQLite and PostgreSQL**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/handlers/ -run 'TestServerSettings|TestSetupStatus' -count=1 && go test ./internal/handlers/ -run 'TestServerSettings|TestSetupStatus' -count=1 && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/server_settings.go internal/handlers/server_settings_test.go cmd/whatomate/main.go
git commit -m "feat: server port setting for super admins"
```

---

### Task 11: Frontend services, wizard logic and route guard

**Files:**
- Modify: `frontend/src/services/api.ts`, `frontend/src/router/index.ts`
- Create: `frontend/src/lib/setup.ts`, `frontend/src/lib/setup.spec.ts`, `frontend/src/lib/setupState.ts`, `frontend/src/lib/setupState.spec.ts`

**Interfaces:**
- Produces (TypeScript):
  - `api.ts`: types `SetupCandidate`, `SetupStatus`, `CheckStatus`, `SetupCheck`, `SetupDriver`, `SetupDatabase`, `SetupDatabaseResult`, `SetupCompleteRequest`, `ServerSettings`; `setupService.{status, checks, testDatabase, createDatabase, complete}`; `serverSettingsService.{get, update}`.
  - `lib/setup.ts`: `SETUP_STEPS`, `SetupStep`, `hasFailedCheck`, `defaultDatabasePort`, `validPort`, `AdminForm`, `AdminErrors`, `adminErrors`, `passwordStrength`, `databaseReady`, `AfterSetup`, `afterSetup`, `defaultSQLitePath`.
  - `lib/setupState.ts`: `setupNeeded(fetchNeeded?)`, `resetSetupNeeded()`, `setupRedirect(needed, routeName)`.
  - Route `{ path: '/setup', name: 'setup' }`.

- [ ] **Step 1: Add the API types and services**

Append to `frontend/src/services/api.ts`:

```ts
export interface SetupCandidate {
  path: string
  data_dir: string
  scope: 'user' | 'system' | 'fixed'
  writable: boolean
}

export interface SetupStatus {
  needed: boolean
  ready?: boolean
  url?: string
  version?: string
  listen_port?: number
  config_path?: string
  config_path_fixed?: boolean
  candidates?: SetupCandidate[]
}

export type CheckStatus = 'pass' | 'warn' | 'fail'

export interface SetupCheck {
  id: string
  status: CheckStatus
  detail: string
}

export type SetupDriver = 'sqlite' | 'postgres' | 'mysql'

export interface SetupDatabase {
  driver: SetupDriver
  path?: string
  host?: string
  port?: number
  user?: string
  password?: string
  name?: string
  ssl_mode?: string
}

export interface SetupDatabaseResult {
  ok: boolean
  database_exists: boolean
  error?: string
}

export interface SetupCompleteRequest {
  config_path: string
  port: number
  database: SetupDatabase
  organization: string
  admin: { full_name: string; email: string; password: string }
}

export const setupService = {
  status: () => api.get<{ data: SetupStatus }>('/setup/status'),
  checks: (params: { port: number; config_path: string }) =>
    api.get<{ data: { checks: SetupCheck[] } }>('/setup/checks', { params, timeout: 20000 }),
  testDatabase: (db: SetupDatabase) => api.post<{ data: SetupDatabaseResult }>('/setup/database/test', db),
  createDatabase: (db: SetupDatabase) => api.post<{ data: SetupDatabaseResult }>('/setup/database/create', db),
  complete: (req: SetupCompleteRequest) =>
    api.post<{ data: { url: string } }>('/setup/complete', req, { timeout: 600000 }),
}

export interface ServerSettings {
  port: number
  config_port: number
  config_path: string
  writable: boolean
}

export const serverSettingsService = {
  get: () => api.get<{ data: ServerSettings }>('/admin/server-settings'),
  update: (port: number) => api.put<{ data: ServerSettings }>('/admin/server-settings', { port }),
}
```

- [ ] **Step 2: Write the failing logic tests**

`frontend/src/lib/setup.spec.ts`:

```ts
import { describe, expect, it } from 'vitest'
import {
  adminErrors, afterSetup, databaseReady, defaultDatabasePort, defaultSQLitePath,
  hasFailedCheck, passwordStrength, validPort, type AdminForm,
} from './setup'

const goodAdmin: AdminForm = {
  organization: 'Acme', fullName: 'Ana', email: 'ana@example.com', password: 'longenough', confirm: 'longenough',
}

describe('setup logic', () => {
  it('blocks only on failed checks', () => {
    expect(hasFailedCheck([{ id: 'a', status: 'pass', detail: '' }, { id: 'b', status: 'warn', detail: '' }])).toBe(false)
    expect(hasFailedCheck([{ id: 'a', status: 'fail', detail: '' }])).toBe(true)
  })

  it('knows default ports and valid ports', () => {
    expect(defaultDatabasePort('postgres')).toBe(5432)
    expect(defaultDatabasePort('mysql')).toBe(3306)
    expect(defaultDatabasePort('sqlite')).toBeUndefined()
    expect(validPort(4747)).toBe(true)
    expect(validPort(0)).toBe(false)
    expect(validPort(65536)).toBe(false)
    expect(validPort(80.5)).toBe(false)
  })

  it('validates the admin form', () => {
    expect(adminErrors(goodAdmin)).toEqual({})
    expect(adminErrors({ ...goodAdmin, organization: ' ' }).organization).toBe('setup.admin.errors.organization')
    expect(adminErrors({ ...goodAdmin, organization: 'x'.repeat(101) }).organization).toBe('setup.admin.errors.organizationLength')
    expect(adminErrors({ ...goodAdmin, fullName: '' }).fullName).toBe('setup.admin.errors.fullName')
    expect(adminErrors({ ...goodAdmin, email: 'nope' }).email).toBe('setup.admin.errors.email')
    expect(adminErrors({ ...goodAdmin, password: 'short', confirm: 'short' }).password).toBe('setup.admin.errors.passwordLength')
    expect(adminErrors({ ...goodAdmin, confirm: 'different!' }).confirm).toBe('setup.admin.errors.confirm')
  })

  it('rates password strength', () => {
    expect(passwordStrength('short')).toBe(0)
    expect(passwordStrength('alllowercase')).toBe(2)
    expect(passwordStrength('lowercase')).toBe(1)
    expect(passwordStrength('MixedCase1!')).toBe(3)
  })

  it('needs a passing test before the database step continues', () => {
    expect(databaseReady({ driver: 'sqlite', path: '/d/w.db' }, { ok: true, database_exists: false })).toBe(true)
    expect(databaseReady({ driver: 'sqlite', path: '/d/w.db' }, null)).toBe(false)
    const pg = { driver: 'postgres' as const, host: 'h', port: 5432, user: 'u', name: 'n' }
    expect(databaseReady(pg, { ok: true, database_exists: true })).toBe(true)
    expect(databaseReady(pg, { ok: true, database_exists: false })).toBe(false)
    expect(databaseReady(pg, { ok: false, database_exists: false })).toBe(false)
    expect(databaseReady({ ...pg, host: '' }, { ok: true, database_exists: true })).toBe(false)
  })

  it('builds the default SQLite path with the folder separator', () => {
    expect(defaultSQLitePath('/var/lib/whatomate')).toBe('/var/lib/whatomate/whatomate.db')
    expect(defaultSQLitePath('C:\\ProgramData\\whatomate')).toBe('C:\\ProgramData\\whatomate\\whatomate.db')
    expect(defaultSQLitePath('/x/')).toBe('/x/whatomate.db')
  })

  it('decides where to go after setup', () => {
    const here = 'http://localhost:4747'
    expect(afterSetup({ needed: true }, here)).toEqual({ kind: 'wait' })
    expect(afterSetup({ needed: false, ready: false, url: here }, here)).toEqual({ kind: 'wait' })
    expect(afterSetup({ needed: false, ready: true }, here)).toEqual({ kind: 'login' })
    expect(afterSetup({ needed: false, ready: true, url: here }, here)).toEqual({ kind: 'login' })
    expect(afterSetup({ needed: false, ready: true, url: 'http://localhost:9000' }, here))
      .toEqual({ kind: 'moved', url: 'http://localhost:9000' })
  })
})
```

`frontend/src/lib/setupState.spec.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { resetSetupNeeded, setupNeeded, setupRedirect } from './setupState'

afterEach(() => resetSetupNeeded())

describe('setup state', () => {
  it('asks the server once', async () => {
    const fetchNeeded = vi.fn().mockResolvedValue(true)
    expect(await setupNeeded(fetchNeeded)).toBe(true)
    expect(await setupNeeded(fetchNeeded)).toBe(true)
    expect(fetchNeeded).toHaveBeenCalledTimes(1)
  })

  it('treats a failed check as "set up"', async () => {
    expect(await setupNeeded(() => Promise.reject(new Error('offline')))).toBe(false)
  })

  it('redirects to and away from setup', () => {
    expect(setupRedirect(true, 'login')).toBe('/setup')
    expect(setupRedirect(true, 'setup')).toBeNull()
    expect(setupRedirect(false, 'setup')).toBe('/login')
    expect(setupRedirect(false, 'dashboard')).toBeNull()
  })
})
```

- [ ] **Step 3: Run to see them fail**

Run: `cd frontend && npx vitest run src/lib/setup.spec.ts src/lib/setupState.spec.ts`
Expected: FAIL (`Failed to resolve import "./setup"`).

- [ ] **Step 4: Implement `frontend/src/lib/setup.ts`**

```ts
import type { SetupCheck, SetupDatabase, SetupDatabaseResult, SetupDriver, SetupStatus } from '@/services/api'

export const SETUP_STEPS = ['welcome', 'checks', 'database', 'admin'] as const
export type SetupStep = (typeof SETUP_STEPS)[number]

export function hasFailedCheck(checks: SetupCheck[]): boolean {
  return checks.some(c => c.status === 'fail')
}

export function defaultDatabasePort(driver: SetupDriver): number | undefined {
  if (driver === 'postgres') return 5432
  if (driver === 'mysql') return 3306
  return undefined
}

export function validPort(port: number): boolean {
  return Number.isInteger(port) && port >= 1 && port <= 65535
}

export interface AdminForm {
  organization: string
  fullName: string
  email: string
  password: string
  confirm: string
}

export type AdminErrors = Partial<Record<keyof AdminForm, string>>

const EMAIL = /^[^\s@]+@[^\s@]+$/

/** adminErrors returns an i18n key per invalid field. */
export function adminErrors(f: AdminForm): AdminErrors {
  const e: AdminErrors = {}
  const org = f.organization.trim()
  if (!org) e.organization = 'setup.admin.errors.organization'
  else if (org.length > 100) e.organization = 'setup.admin.errors.organizationLength'
  if (!f.fullName.trim()) e.fullName = 'setup.admin.errors.fullName'
  if (!EMAIL.test(f.email.trim())) e.email = 'setup.admin.errors.email'
  if (f.password.length < 8) e.password = 'setup.admin.errors.passwordLength'
  if (f.confirm !== f.password) e.confirm = 'setup.admin.errors.confirm'
  return e
}

/** passwordStrength: 0 too short, 1 weak, 2 fair, 3 strong. */
export function passwordStrength(pw: string): 0 | 1 | 2 | 3 {
  if (pw.length < 8) return 0
  let score = 1
  if (pw.length >= 12) score++
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw)) score++
  if (/\d/.test(pw) && /[^A-Za-z0-9]/.test(pw)) score++
  return Math.min(score, 3) as 1 | 2 | 3
}

/** databaseReady: the fields are filled and the last test passed for them. */
export function databaseReady(db: SetupDatabase, result: SetupDatabaseResult | null): boolean {
  if (!result?.ok) return false
  if (db.driver === 'sqlite') return !!db.path
  return !!db.host && !!db.user && !!db.name && validPort(db.port ?? 0) && result.database_exists
}

export function defaultSQLitePath(dataDir: string): string {
  const sep = dataDir.includes('\\') ? '\\' : '/'
  return dataDir.replace(/[\\/]+$/, '') + sep + 'whatomate.db'
}

export type AfterSetup = { kind: 'wait' } | { kind: 'login' } | { kind: 'moved'; url: string }

/** afterSetup reads the status poll after Finish. */
export function afterSetup(status: SetupStatus, currentOrigin: string): AfterSetup {
  if (status.needed || !status.ready) return { kind: 'wait' }
  if (status.url) {
    const origin = new URL(status.url).origin
    if (origin !== currentOrigin) return { kind: 'moved', url: origin }
  }
  return { kind: 'login' }
}
```

- [ ] **Step 5: Implement `frontend/src/lib/setupState.ts`**

```ts
let pending: Promise<boolean> | null = null

async function fetchFromServer(): Promise<boolean> {
  const { setupService } = await import('@/services/api')
  const res = await setupService.status()
  return res.data.data.needed === true
}

/** setupNeeded asks the server once per page load whether first-run setup is pending. */
export function setupNeeded(fetchNeeded: () => Promise<boolean> = fetchFromServer): Promise<boolean> {
  if (!pending) pending = fetchNeeded().catch(() => false)
  return pending
}

export function resetSetupNeeded(): void {
  pending = null
}

/** setupRedirect returns where to send a navigation, or null to let it through. */
export function setupRedirect(needed: boolean, routeName: unknown): string | null {
  if (needed && routeName !== 'setup') return '/setup'
  if (!needed && routeName === 'setup') return '/login'
  return null
}
```

- [ ] **Step 6: Add the route and guard**

In `frontend/src/router/index.ts`, add the import `import { setupNeeded, setupRedirect } from '@/lib/setupState'` and the route before `/login`:

```ts
    {
      path: '/setup',
      name: 'setup',
      component: () => import('@/views/setup/SetupView.vue'),
      meta: { requiresAuth: false }
    },
```

At the top of `router.beforeEach(async (to, _from, next) => {`:

```ts
  const redirect = setupRedirect(await setupNeeded(), to.name)
  if (redirect) return next({ path: redirect })
  if (to.name === 'setup') return next()
```

Create a placeholder `frontend/src/views/setup/SetupView.vue` (replaced in Task 12) so the build resolves:

```vue
<template>
  <div />
</template>
```

- [ ] **Step 7: Run unit tests and type check**

Run: `cd frontend && npx vitest run && npx vue-tsc --noEmit -p tsconfig.json`
Expected: all specs PASS, no type errors.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/services/api.ts frontend/src/lib/setup.ts frontend/src/lib/setup.spec.ts frontend/src/lib/setupState.ts frontend/src/lib/setupState.spec.ts frontend/src/router/index.ts frontend/src/views/setup/SetupView.vue
git commit -m "feat(frontend): setup API client, wizard logic and route guard"
```

---

### Task 12: Wizard screens

**Files:**
- Create/replace: `frontend/src/views/setup/SetupView.vue`, `frontend/src/views/setup/SetupChecks.vue`, `frontend/src/views/setup/SetupDatabase.vue`, `frontend/src/views/setup/SetupAdmin.vue`
- Modify: `frontend/src/i18n/locales/en.json` (new top-level `"setup"` object)

**Interfaces:**
- Consumes: Task 11 (`setupService`, `lib/setup.ts`).
- Produces: the `/setup` page.

- [ ] **Step 1: Add the English strings**

Add a top-level `"setup"` key to `frontend/src/i18n/locales/en.json` (other locales fall back to English):

```json
  "setup": {
    "title": "Set up whatomate",
    "steps": { "welcome": "Welcome", "checks": "System check", "database": "Database", "admin": "Admin" },
    "back": "Back",
    "next": "Next",
    "welcome": {
      "title": "Welcome to whatomate",
      "body": "No configuration was found, so let's set this server up. You'll check the system, choose a database and create the first admin account. It takes a couple of minutes.",
      "version": "Version {version}",
      "start": "Start setup"
    },
    "checks": {
      "title": "System check",
      "subtitle": "Problems marked in red must be fixed before you continue. Warnings won't stop setup.",
      "rerun": "Run again",
      "port": "Port",
      "portHint": "whatomate will listen on this port after setup.",
      "location": "Config file",
      "locationHint": "Where whatomate saves its configuration. Data is stored in {dir}.",
      "scope": { "user": "This user", "system": "System-wide", "fixed": "Chosen at startup" },
      "notWritable": "not writable",
      "items": {
        "system": "System",
        "folders": "Folders writable",
        "disk": "Free disk space",
        "memory": "Memory",
        "port": "Port",
        "internet": "Internet (Meta Graph API)",
        "clock": "System clock",
        "audio": "Audio tools for calling"
      }
    },
    "database": {
      "title": "Database",
      "subtitle": "SQLite needs nothing else installed. Use PostgreSQL or MySQL if you already run one.",
      "drivers": { "sqlite": "SQLite", "postgres": "PostgreSQL", "mysql": "MySQL / MariaDB" },
      "path": "Database file",
      "host": "Host",
      "port": "Port",
      "user": "User",
      "password": "Password",
      "name": "Database name",
      "sslMode": "SSL mode",
      "test": "Test connection",
      "testing": "Testing…",
      "ok": "Connection works.",
      "missing": "The server answered, but database \"{name}\" doesn't exist yet.",
      "create": "Create database",
      "creating": "Creating…",
      "failed": "Connection failed: {error}"
    },
    "admin": {
      "title": "Admin account",
      "subtitle": "This account can manage every organization on this server.",
      "organization": "Organization name",
      "fullName": "Full name",
      "email": "Email",
      "password": "Password",
      "confirm": "Confirm password",
      "strength": { "tooShort": "Too short", "weak": "Weak", "fair": "Fair", "strong": "Strong" },
      "finish": "Finish setup",
      "errors": {
        "organization": "Enter an organization name.",
        "organizationLength": "Use 100 characters or fewer.",
        "fullName": "Enter your name.",
        "email": "Enter a valid email address.",
        "passwordLength": "Use at least 8 characters.",
        "confirm": "Passwords don't match."
      }
    },
    "finish": {
      "saving": "Setting up the database, admin account and config file…",
      "slow": "This can take a minute.",
      "starting": "Starting whatomate…",
      "httpNote": "You opened setup over plain HTTP, so whatomate runs in development mode (cookies work without HTTPS). Put it behind HTTPS and set environment = \"production\" before going live.",
      "failed": "Setup failed. Check the details and try again."
    }
  },
```

Run: `node -e "JSON.parse(require('fs').readFileSync('frontend/src/i18n/locales/en.json','utf8'))"` → no output.

- [ ] **Step 2: Write `SetupChecks.vue`**

```vue
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CheckCircle2, AlertTriangle, XCircle, Loader2, RefreshCw } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { setupService, type SetupCandidate, type SetupCheck } from '@/services/api'
import { hasFailedCheck, validPort } from '@/lib/setup'

const props = defineProps<{ candidates: SetupCandidate[]; fixed: boolean }>()
const port = defineModel<number>('port', { required: true })
const configPath = defineModel<string>('configPath', { required: true })
const emit = defineEmits<{ back: []; next: [] }>()
const { t } = useI18n()

const checks = ref<SetupCheck[]>([])
const loading = ref(false)
const failed = ref('')
let timer: ReturnType<typeof setTimeout> | undefined

const dataDir = computed(() => props.candidates.find(c => c.path === configPath.value)?.data_dir ?? '')
const canContinue = computed(() => !loading.value && checks.value.length > 0 && !hasFailedCheck(checks.value) && validPort(port.value))

async function run() {
  if (!validPort(port.value) || !configPath.value) return
  loading.value = true
  failed.value = ''
  try {
    const res = await setupService.checks({ port: port.value, config_path: configPath.value })
    checks.value = res.data.data.checks
  } catch (e: any) {
    failed.value = e?.response?.data?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

watch([port, configPath], () => {
  clearTimeout(timer)
  timer = setTimeout(run, 400)
})
onMounted(run)

const icon = { pass: CheckCircle2, warn: AlertTriangle, fail: XCircle }
const color = { pass: 'text-emerald-500', warn: 'text-amber-500', fail: 'text-red-500' }
</script>

<template>
  <div class="space-y-5">
    <div>
      <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.checks.title') }}</h3>
      <p class="text-sm text-white/50 light:text-gray-500">{{ t('setup.checks.subtitle') }}</p>
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
      <div class="space-y-2">
        <Label for="setup-port" class="text-white/70 light:text-gray-700">{{ t('setup.checks.port') }}</Label>
        <Input id="setup-port" v-model.number="port" type="number" min="1" max="65535" data-testid="setup-port" />
        <p class="text-xs text-white/40 light:text-gray-500">{{ t('setup.checks.portHint') }}</p>
      </div>
      <div class="space-y-2 sm:col-span-2">
        <Label class="text-white/70 light:text-gray-700">{{ t('setup.checks.location') }}</Label>
        <div class="space-y-1">
          <label
            v-for="c in candidates"
            :key="c.path"
            class="flex items-start gap-2 rounded-lg border border-white/[0.08] light:border-gray-200 p-2 text-sm cursor-pointer"
            :class="{ 'opacity-50 cursor-not-allowed': !c.writable && !fixed }"
          >
            <input v-model="configPath" type="radio" :value="c.path" :disabled="!c.writable && !fixed" class="mt-1" />
            <span class="min-w-0">
              <span class="block font-mono text-xs break-all text-white/80 light:text-gray-800">{{ c.path }}</span>
              <span class="text-xs text-white/40 light:text-gray-500">
                {{ t(`setup.checks.scope.${c.scope}`) }}<template v-if="!c.writable"> · {{ t('setup.checks.notWritable') }}</template>
              </span>
            </span>
          </label>
        </div>
        <p v-if="dataDir" class="text-xs text-white/40 light:text-gray-500">{{ t('setup.checks.locationHint', { dir: dataDir }) }}</p>
      </div>
    </div>

    <ul class="divide-y divide-white/[0.06] light:divide-gray-100 rounded-lg border border-white/[0.08] light:border-gray-200">
      <li v-for="c in checks" :key="c.id" class="flex items-start gap-3 p-3" :data-testid="`check-${c.id}`">
        <component :is="icon[c.status]" class="h-5 w-5 shrink-0" :class="color[c.status]" />
        <div class="min-w-0">
          <p class="text-sm font-medium text-white light:text-gray-900">{{ t(`setup.checks.items.${c.id}`) }}</p>
          <p class="text-xs text-white/50 light:text-gray-500 break-words">{{ c.detail }}</p>
        </div>
      </li>
      <li v-if="loading && checks.length === 0" class="flex items-center gap-2 p-3 text-sm text-white/50">
        <Loader2 class="h-4 w-4 animate-spin" />
      </li>
    </ul>
    <p v-if="failed" class="text-sm text-red-500">{{ failed }}</p>

    <div class="flex justify-between">
      <Button variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <div class="flex gap-2">
        <Button variant="outline" :disabled="loading" @click="run">
          <RefreshCw class="mr-2 h-4 w-4" :class="{ 'animate-spin': loading }" />{{ t('setup.checks.rerun') }}
        </Button>
        <Button :disabled="!canContinue" data-testid="setup-checks-next" @click="emit('next')">{{ t('setup.next') }}</Button>
      </div>
    </div>
  </div>
</template>
```

- [ ] **Step 3: Write `SetupDatabase.vue`**

```vue
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { setupService, type SetupDatabase, type SetupDatabaseResult, type SetupDriver } from '@/services/api'
import { databaseReady, defaultDatabasePort, defaultSQLitePath } from '@/lib/setup'

// The parent keeps the SQLite path in step with dataDir.
const props = defineProps<{ dataDir: string }>()
const db = defineModel<SetupDatabase>({ required: true })
const emit = defineEmits<{ back: []; next: [] }>()
const { t } = useI18n()

const drivers: SetupDriver[] = ['sqlite', 'postgres', 'mysql']
const result = ref<SetupDatabaseResult | null>(null)
const busy = ref<'' | 'test' | 'create'>('')

function choose(driver: SetupDriver) {
  db.value = driver === 'sqlite'
    ? { driver, path: defaultSQLitePath(props.dataDir) }
    : { driver, host: 'localhost', port: defaultDatabasePort(driver), user: '', password: '', name: 'whatomate', ...(driver === 'postgres' ? { ssl_mode: 'disable' } : {}) }
}

watch(db, () => { result.value = null }, { deep: true })

const ready = computed(() => databaseReady(db.value, result.value))
const showCreate = computed(() => db.value.driver !== 'sqlite' && result.value?.ok && !result.value.database_exists)

async function test() {
  busy.value = 'test'
  try {
    result.value = (await setupService.testDatabase(db.value)).data.data
  } catch (e: any) {
    result.value = { ok: false, database_exists: false, error: e?.response?.data?.message ?? String(e) }
  } finally {
    busy.value = ''
  }
}

async function create() {
  busy.value = 'create'
  try {
    const res = (await setupService.createDatabase(db.value)).data.data
    if (!res.ok) {
      result.value = res
      return
    }
  } catch (e: any) {
    result.value = { ok: false, database_exists: false, error: e?.response?.data?.message ?? String(e) }
    return
  } finally {
    busy.value = ''
  }
  await test()
}
</script>

<template>
  <div class="space-y-5">
    <div>
      <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.database.title') }}</h3>
      <p class="text-sm text-white/50 light:text-gray-500">{{ t('setup.database.subtitle') }}</p>
    </div>

    <div class="grid grid-cols-3 gap-2">
      <Button
        v-for="d in drivers"
        :key="d"
        :variant="db.driver === d ? 'default' : 'outline'"
        :data-testid="`setup-driver-${d}`"
        @click="choose(d)"
      >
        {{ t(`setup.database.drivers.${d}`) }}
      </Button>
    </div>

    <div v-if="db.driver === 'sqlite'" class="space-y-2">
      <Label for="db-path" class="text-white/70 light:text-gray-700">{{ t('setup.database.path') }}</Label>
      <Input id="db-path" v-model.trim="db.path" class="font-mono text-xs" />
    </div>
    <div v-else class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <div class="space-y-2">
        <Label for="db-host" class="text-white/70 light:text-gray-700">{{ t('setup.database.host') }}</Label>
        <Input id="db-host" v-model.trim="db.host" />
      </div>
      <div class="space-y-2">
        <Label for="db-port" class="text-white/70 light:text-gray-700">{{ t('setup.database.port') }}</Label>
        <Input id="db-port" v-model.number="db.port" type="number" min="1" max="65535" />
      </div>
      <div class="space-y-2">
        <Label for="db-user" class="text-white/70 light:text-gray-700">{{ t('setup.database.user') }}</Label>
        <Input id="db-user" v-model.trim="db.user" autocomplete="off" />
      </div>
      <div class="space-y-2">
        <Label for="db-password" class="text-white/70 light:text-gray-700">{{ t('setup.database.password') }}</Label>
        <Input id="db-password" v-model="db.password" type="password" autocomplete="new-password" />
      </div>
      <div class="space-y-2">
        <Label for="db-name" class="text-white/70 light:text-gray-700">{{ t('setup.database.name') }}</Label>
        <Input id="db-name" v-model.trim="db.name" />
      </div>
      <div v-if="db.driver === 'postgres'" class="space-y-2">
        <Label for="db-ssl" class="text-white/70 light:text-gray-700">{{ t('setup.database.sslMode') }}</Label>
        <select id="db-ssl" v-model="db.ssl_mode" class="h-9 w-full rounded-md border border-white/[0.1] bg-transparent px-3 text-sm light:border-gray-200">
          <option v-for="m in ['disable', 'require', 'verify-ca', 'verify-full']" :key="m" :value="m">{{ m }}</option>
        </select>
      </div>
    </div>

    <div class="flex flex-wrap items-center gap-3">
      <Button variant="outline" :disabled="busy !== ''" data-testid="setup-db-test" @click="test">
        <Loader2 v-if="busy === 'test'" class="mr-2 h-4 w-4 animate-spin" />
        {{ busy === 'test' ? t('setup.database.testing') : t('setup.database.test') }}
      </Button>
      <Button v-if="showCreate" :disabled="busy !== ''" data-testid="setup-db-create" @click="create">
        <Loader2 v-if="busy === 'create'" class="mr-2 h-4 w-4 animate-spin" />
        {{ busy === 'create' ? t('setup.database.creating') : t('setup.database.create') }}
      </Button>
    </div>
    <p v-if="result?.ok && (result.database_exists || db.driver === 'sqlite')" class="text-sm text-emerald-500">{{ t('setup.database.ok') }}</p>
    <p v-else-if="showCreate" class="text-sm text-amber-500">{{ t('setup.database.missing', { name: db.name }) }}</p>
    <p v-else-if="result && !result.ok" class="text-sm text-red-500 break-words">{{ t('setup.database.failed', { error: result.error }) }}</p>

    <div class="flex justify-between">
      <Button variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button :disabled="!ready" data-testid="setup-db-next" @click="emit('next')">{{ t('setup.next') }}</Button>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Write `SetupAdmin.vue`**

```vue
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { adminErrors, passwordStrength, type AdminForm } from '@/lib/setup'

const form = defineModel<AdminForm>({ required: true })
const emit = defineEmits<{ back: []; finish: [] }>()
const { t } = useI18n()

const submitted = ref(false)
const errors = computed(() => adminErrors(form.value))
const strength = computed(() => passwordStrength(form.value.password))
const strengthKeys = ['tooShort', 'weak', 'fair', 'strong'] as const
const strengthColor = ['bg-red-500', 'bg-red-500', 'bg-amber-500', 'bg-emerald-500']

function finish() {
  submitted.value = true
  if (Object.keys(errors.value).length === 0) emit('finish')
}

const fields: { key: keyof AdminForm; type: string; autocomplete: string }[] = [
  { key: 'organization', type: 'text', autocomplete: 'organization' },
  { key: 'fullName', type: 'text', autocomplete: 'name' },
  { key: 'email', type: 'email', autocomplete: 'email' },
  { key: 'password', type: 'password', autocomplete: 'new-password' },
  { key: 'confirm', type: 'password', autocomplete: 'new-password' },
]
</script>

<template>
  <form class="space-y-5" novalidate @submit.prevent="finish">
    <div>
      <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.admin.title') }}</h3>
      <p class="text-sm text-white/50 light:text-gray-500">{{ t('setup.admin.subtitle') }}</p>
    </div>

    <div v-for="f in fields" :key="f.key" class="space-y-2">
      <Label :for="`admin-${f.key}`" class="text-white/70 light:text-gray-700">{{ t(`setup.admin.${f.key}`) }}</Label>
      <Input :id="`admin-${f.key}`" v-model="form[f.key]" :type="f.type" :autocomplete="f.autocomplete" :data-testid="`setup-admin-${f.key}`" />
      <div v-if="f.key === 'password' && form.password" class="flex items-center gap-2">
        <div class="h-1.5 flex-1 rounded bg-white/[0.08] light:bg-gray-200 overflow-hidden">
          <div class="h-full transition-all" :class="strengthColor[strength]" :style="{ width: `${(strength / 3) * 100}%` }" />
        </div>
        <span class="text-xs text-white/50 light:text-gray-500">{{ t(`setup.admin.strength.${strengthKeys[strength]}`) }}</span>
      </div>
      <p v-if="submitted && errors[f.key]" class="text-xs text-red-500">{{ t(errors[f.key]!) }}</p>
    </div>

    <div class="flex justify-between">
      <Button type="button" variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button type="submit" data-testid="setup-finish">{{ t('setup.admin.finish') }}</Button>
    </div>
  </form>
</template>
```

- [ ] **Step 5: Replace `SetupView.vue`**

```vue
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessageSquare, Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { setupService, type SetupDatabase, type SetupStatus } from '@/services/api'
import { afterSetup, defaultSQLitePath, SETUP_STEPS, type AdminForm, type SetupStep } from '@/lib/setup'
import SetupChecks from './SetupChecks.vue'
import SetupDatabaseStep from './SetupDatabase.vue'
import SetupAdmin from './SetupAdmin.vue'

const { t } = useI18n()

const status = ref<SetupStatus | null>(null)
const step = ref<SetupStep>('welcome')
const port = ref(4747)
const configPath = ref('')
const database = ref<SetupDatabase>({ driver: 'sqlite', path: '' })
const admin = ref<AdminForm>({ organization: '', fullName: '', email: '', password: '', confirm: '' })
const phase = ref<'form' | 'saving' | 'starting'>('form')
const error = ref('')

const candidates = computed(() => status.value?.candidates ?? [])
const dataDir = computed(() => candidates.value.find(c => c.path === configPath.value)?.data_dir ?? '')
const stepIndex = computed(() => SETUP_STEPS.indexOf(step.value))
const plainHttp = window.location.protocol === 'http:'

// Keep the SQLite file in the chosen data folder unless the user typed a path.
watch(dataDir, (dir, old) => {
  const db = database.value
  if (dir && db.driver === 'sqlite' && (!db.path || db.path === defaultSQLitePath(old))) {
    database.value = { ...db, path: defaultSQLitePath(dir) }
  }
})

onMounted(async () => {
  const res = await setupService.status()
  status.value = res.data.data
  port.value = status.value.listen_port ?? 4747
  configPath.value = status.value.config_path ?? ''
})

function go(next: SetupStep) {
  error.value = ''
  step.value = next
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

function loginPath(): string {
  const base = (((window as any).__BASE_PATH__ as string | undefined) ?? '').replace(/\/$/, '')
  return `${base}/login`
}

async function waitForOrigin(origin: string) {
  for (;;) {
    try {
      await fetch(`${origin}/health`, { mode: 'no-cors', cache: 'no-store' })
      return
    } catch {
      await sleep(1000)
    }
  }
}

async function waitForApp() {
  for (;;) {
    try {
      const next = afterSetup((await setupService.status()).data.data, window.location.origin)
      if (next.kind === 'login') {
        window.location.assign(loginPath())
        return
      }
      if (next.kind === 'moved') {
        await waitForOrigin(next.url)
        window.location.assign(`${next.url}/login`)
        return
      }
    } catch {
      // the server is switching from setup to the app
    }
    await sleep(1000)
  }
}

async function finish() {
  phase.value = 'saving'
  error.value = ''
  try {
    await setupService.complete({
      config_path: configPath.value,
      port: port.value,
      database: database.value,
      organization: admin.value.organization.trim(),
      admin: { full_name: admin.value.fullName.trim(), email: admin.value.email.trim(), password: admin.value.password },
    })
  } catch (e: any) {
    phase.value = 'form'
    error.value = e?.response?.data?.message ?? t('setup.finish.failed')
    return
  }
  phase.value = 'starting'
  await waitForApp()
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-[#0a0a0b] light:bg-gradient-to-br light:from-gray-50 light:to-gray-100 p-4">
    <div class="w-full max-w-2xl rounded-2xl border border-white/[0.08] bg-white/[0.02] backdrop-blur light:bg-white light:border-gray-200 light:shadow-xl">
      <div class="p-8 pb-4 flex items-center gap-3">
        <div class="h-10 w-10 rounded-xl bg-gradient-to-br from-emerald-500 to-green-600 flex items-center justify-center shadow-lg shadow-emerald-500/20">
          <MessageSquare class="h-6 w-6 text-white" />
        </div>
        <h2 class="text-xl font-bold text-white light:text-gray-900">{{ t('setup.title') }}</h2>
      </div>

      <ol v-if="phase === 'form'" class="px-8 flex gap-2">
        <li v-for="(s, i) in SETUP_STEPS" :key="s" class="flex-1">
          <div class="h-1 rounded" :class="i <= stepIndex ? 'bg-emerald-500' : 'bg-white/[0.08] light:bg-gray-200'" />
          <p class="mt-1 text-xs" :class="i === stepIndex ? 'text-white light:text-gray-900' : 'text-white/40 light:text-gray-500'">{{ t(`setup.steps.${s}`) }}</p>
        </li>
      </ol>

      <div class="p-8">
        <div v-if="!status" class="flex justify-center py-8"><Loader2 class="h-6 w-6 animate-spin text-white/50" /></div>

        <div v-else-if="phase !== 'form'" class="space-y-4 py-6 text-center" data-testid="setup-progress">
          <Loader2 class="mx-auto h-8 w-8 animate-spin text-emerald-500" />
          <p class="text-white light:text-gray-900">{{ phase === 'saving' ? t('setup.finish.saving') : t('setup.finish.starting') }}</p>
          <p v-if="phase === 'saving'" class="text-sm text-white/50 light:text-gray-500">{{ t('setup.finish.slow') }}</p>
          <p v-if="plainHttp" class="text-xs text-amber-500 max-w-md mx-auto">{{ t('setup.finish.httpNote') }}</p>
        </div>

        <template v-else>
          <div v-if="step === 'welcome'" class="space-y-4">
            <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.welcome.title') }}</h3>
            <p class="text-white/60 light:text-gray-600">{{ t('setup.welcome.body') }}</p>
            <p class="text-xs text-white/40 light:text-gray-500">{{ t('setup.welcome.version', { version: status.version }) }}</p>
            <div class="flex justify-end">
              <Button data-testid="setup-start" @click="go('checks')">{{ t('setup.welcome.start') }}</Button>
            </div>
          </div>

          <SetupChecks
            v-else-if="step === 'checks'"
            v-model:port="port"
            v-model:config-path="configPath"
            :candidates="candidates"
            :fixed="!!status.config_path_fixed"
            @back="go('welcome')"
            @next="go('database')"
          />

          <SetupDatabaseStep
            v-else-if="step === 'database'"
            v-model="database"
            :data-dir="dataDir"
            @back="go('checks')"
            @next="go('admin')"
          />

          <SetupAdmin
            v-else
            v-model="admin"
            @back="go('database')"
            @finish="finish"
          />

          <p v-if="error" class="mt-4 text-sm text-red-500 break-words" data-testid="setup-error">{{ error }}</p>
        </template>
      </div>
    </div>
  </div>
</template>
```

- [ ] **Step 6: Type check, lint, unit tests, build**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json && npx eslint src/views/setup src/lib/setup.ts src/lib/setupState.ts && npx vitest run && npm run build`
Expected: no errors; build succeeds.

- [ ] **Step 7: Walk through the wizard in a browser**

Build the binary with the frontend (`make build-prod`, or `npm run build` then `CGO_ENABLED=0 go build -o /tmp/whatomate-setup ./cmd/whatomate` after copying `frontend/dist` the way the Makefile does). In a fresh temp folder, run `/tmp/whatomate-setup -config "$tmp/config.toml"` with `HOME`/`XDG_CONFIG_HOME` pointed at the temp folder, and use the cursor-ide-browser tools (not Playwright) to:

1. Open `http://localhost:4747`: the welcome screen shows; Start → system check lists eight checks.
2. Change the port to an in-use port (for example the dev server's 8080): the port check turns red and Next is disabled; set it back to 4747.
3. Database: SQLite preselected with `<tmp>/whatomate.db`; Test connection → "Connection works."; Next.
4. Admin: Finish with an empty form shows the field errors; fill it in; Finish → progress screen → the browser lands on `/login`; log in with the new admin.
5. Confirm `$tmp/config.toml` exists with mode 0600 and the server log shows "Server listening" on 4747 without a restart.

Take a screenshot of each screen for the hand-off. Stop the server by PID and delete the temp folder.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/views/setup frontend/src/i18n/locales/en.json
git commit -m "feat(frontend): setup wizard screens"
```

---

### Task 13: Settings → General server card

**Files:**
- Modify: `frontend/src/views/settings/SettingsView.vue`, `frontend/src/i18n/locales/en.json` (`settings.server`)

**Interfaces:**
- Consumes: `serverSettingsService`, `ServerSettings`, `validPort` (Task 11).

- [ ] **Step 1: Add the strings**

Inside the existing `"settings"` object in `en.json`:

```json
    "server": {
      "title": "Server",
      "desc": "Install-wide settings stored in the config file. Changes apply after a restart.",
      "port": "Port",
      "configFile": "Config file: {path}",
      "notWritable": "whatomate can't write {path}. Edit it as its owner, then restart.",
      "restartNeeded": "Saved port {port}. Restart whatomate to apply it.",
      "save": "Save port",
      "saved": "Port saved. Restart whatomate to apply it.",
      "invalidPort": "Enter a port between 1 and 65535.",
      "saveFailed": "Could not save the port."
    },
```

- [ ] **Step 2: Add the script logic**

In `SettingsView.vue` `<script setup>`: extend the `@/services/api` import with `serverSettingsService, type ServerSettings`, add `import { validPort } from '@/lib/setup'`, and add:

```ts
const serverSettings = ref<ServerSettings | null>(null)
const serverPort = ref(4747)
const isSavingServer = ref(false)

async function loadServerSettings() {
  if (!isSuperAdmin.value) return
  try {
    const res = await serverSettingsService.get()
    serverSettings.value = res.data.data
    serverPort.value = res.data.data.config_port
  } catch {
    serverSettings.value = null
  }
}

async function saveServerPort() {
  if (!validPort(serverPort.value)) {
    toast.error(t('settings.server.invalidPort'))
    return
  }
  isSavingServer.value = true
  try {
    const res = await serverSettingsService.update(serverPort.value)
    serverSettings.value = res.data.data
    toast.success(t('settings.server.saved'))
  } catch (e: any) {
    toast.error(e?.response?.data?.message ?? t('settings.server.saveFailed'))
  } finally {
    isSavingServer.value = false
  }
}
```

Call `loadServerSettings()` inside the existing `onMounted(async () => { … })` (line ~135), after the existing loads.

- [ ] **Step 3: Add the card**

In the General tab, directly before `<div v-if="orgID" class="mt-4">` (the `AuditLogPanel`), insert:

```vue
            <div
              v-if="isSuperAdmin && serverSettings"
              class="mt-4 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200"
              data-testid="server-settings-card"
            >
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.server.title') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.server.desc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="space-y-2 max-w-xs">
                  <Label for="server_port" class="text-white/70 light:text-gray-700">{{ $t('settings.server.port') }}</Label>
                  <Input id="server_port" v-model.number="serverPort" type="number" min="1" max="65535" :disabled="!serverSettings.writable" />
                </div>
                <p class="text-xs font-mono break-all text-white/40 light:text-gray-500">{{ $t('settings.server.configFile', { path: serverSettings.config_path }) }}</p>
                <p v-if="!serverSettings.writable" class="text-xs text-amber-500">{{ $t('settings.server.notWritable', { path: serverSettings.config_path }) }}</p>
                <p v-else-if="serverSettings.config_port !== serverSettings.port" class="text-xs text-amber-500">{{ $t('settings.server.restartNeeded', { port: serverSettings.config_port }) }}</p>
                <div class="flex justify-end">
                  <Button
                    variant="outline"
                    size="sm"
                    class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50"
                    :disabled="isSavingServer || !serverSettings.writable"
                    @click="saveServerPort"
                  >
                    <Loader2 v-if="isSavingServer" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.server.save') }}
                  </Button>
                </div>
              </div>
            </div>
```

- [ ] **Step 4: Type check, lint, build**

Run: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json && npx eslint src/views/settings/SettingsView.vue && npm run build`
Expected: no errors.

- [ ] **Step 5: Check it in the browser**

Against the running dev server (it loads `config.local.toml`): log in as a super admin, open Settings → General, confirm the Server card shows port 8080 and the config path. **Do not save** — that would change the user's local config. Saving is covered by the Task 10 tests. Screenshot the card. Log in as a non-super-admin (or check the DOM) to confirm the card is hidden.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/views/settings/SettingsView.vue frontend/src/i18n/locales/en.json
git commit -m "feat(frontend): server port card in Settings"
```

---

### Task 14: Docs and final verification

**Files:**
- Modify: `docs/src/content/docs/getting-started/quickstart.mdx`, `docs/src/content/docs/getting-started/configuration.mdx`, `README.md`, `.goreleaser.yml` (Homebrew caveats)

- [ ] **Step 1: Quickstart — first run**

In `quickstart.mdx`, make the binary/Homebrew path start with the wizard (replace the "copy config.example.toml, edit, run -migrate" steps for that path; keep the Docker section as it is):

```mdx
## First run

Start whatomate:

```bash
whatomate
```

With no config file, whatomate opens a setup wizard in your browser at
http://localhost:4747 (on a server without a desktop, open the URL it prints).
The wizard:

1. checks the system (folders, disk, memory, port, internet, clock, audio tools);
2. asks which database to use: SQLite (nothing to install), PostgreSQL or MySQL;
3. creates your organization and admin account;
4. writes the config file and starts whatomate on the same port.

:::caution
Until setup is finished, anyone who can reach port 4747 can complete it.
Run the first setup on a private network or behind a firewall, then expose
whatomate.
:::

To set up again, delete the config file (`whatomate config path` shows where
it is) and start whatomate.
```

Replace "login with `admin@admin.com` / `admin`" in the README and quickstart binary sections with "log in with the admin account you created in the setup wizard". The Docker sections, which ship a config, keep the default-admin note.

- [ ] **Step 2: Configuration docs**

In `configuration.mdx`, under the config search order section, add:

```mdx
If no file is found, `whatomate server` (or plain `whatomate`) starts the
setup wizard instead of exiting. The wizard writes the config to the system
folder when it can, otherwise to your user folder, with mode 0600. It
generates `[jwt] secret` and `[app] encryption_key`, and sets
`environment = "production"` only when the wizard was opened over HTTPS.

`whatomate worker` never runs the wizard.
```

Add an "Upgrading" note at the top of the `[server]` section: "The default port changed from 8080 to 4747. A config without `[server] port` now listens on 4747; set `port = 8080` to keep the old port (Docker users: update the published port too)."

In the `[server]` table row: "`host`, `port`: default `0.0.0.0:4747`. Super admins can change the port in Settings → General; it applies after a restart." Under `[default_admin]`, note that it is used only by `server -migrate` on a database with no users.

- [ ] **Step 3: Homebrew caveats**

Replace the body of `caveats:` in `.goreleaser.yml` (keep the notarization `{{- if … }}` block at the end):

```yaml
    caveats: |
      Start whatomate and finish setup in your browser:
        whatomate
      It opens http://localhost:4747 and asks which database to use
      (SQLite needs nothing else installed). The config is saved to
      #{HOMEBREW_PREFIX}/etc/whatomate/config.toml.

      Redis is optional: set [redis] host in config.toml to use one.
```

Run: `goreleaser check` if installed; otherwise `ruby -e 'require "yaml"; YAML.load_file(".goreleaser.yml")'`.

- [ ] **Step 4: Full quality gates**

```bash
gofmt -l cmd internal pkg test
go vet ./...
for os in linux darwin windows; do CGO_ENABLED=0 GOOS=$os GOARCH=amd64 go build -o /dev/null ./cmd/whatomate || echo "FAIL $os"; done
TEST_DB_DRIVER=sqlite go test -p 1 -count=1 ./...
go test -p 1 -count=1 ./...
TEST_DB_DRIVER=mysql go test -p 1 -count=1 ./internal/database/... ./internal/setup/...
cd frontend && npx vitest run && npx vue-tsc --noEmit -p tsconfig.json && npm run build
```
Expected: no gofmt output, every suite PASS, builds succeed. Report any test that is skipped for a missing env var.

- [ ] **Step 5: Restart the dev server on the new build**

The user's dev server runs from `config.local.toml` on port 8080 (ngrok forwards there). Rebuild with `make build-prod`, stop the old process by PID (`pgrep -f "whatomate serve[r]"`), start it again the same way, and confirm it logs `Loaded config` with `config.local.toml` and listens on 8080 — an existing config must not trigger setup.

- [ ] **Step 6: Commit and close the epic**

```bash
git add docs/src README.md .goreleaser.yml
git commit -m "docs: first-run setup wizard"
bd close whatomate-91g
```

Report: changed files, test results, screenshots from Task 12/13, and the suggested (not executed) push command.
