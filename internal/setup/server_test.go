package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

const testToken = "test-setup-token"

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
	s := newServer(o, 4747, testToken)
	return env{s: s, dir: dir, configPath: filepath.Join(userDir, "config.toml"), dbPath: filepath.Join(userDir, "whatomate.db")}
}

func call(t *testing.T, s *server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	return callAs(t, s, method, path, "application/json", body)
}

func callAs(t *testing.T, s *server, method, path, contentType string, body any) (int, map[string]any) {
	t.Helper()
	return callWith(t, s, method, path, map[string]string{"Content-Type": contentType}, body)
}

// callWith sends the setup token unless headers set tokenHeader ("" sends
// none).
func callWith(t *testing.T, s *server, method, path string, headers map[string]string, body any) (int, map[string]any) {
	t.Helper()
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(path)
	ctx.Request.Header.SetHost("localhost:4747")
	all := map[string]string{tokenHeader: s.token}
	for k, v := range headers {
		all[k] = v
	}
	for k, v := range all {
		if v != "" {
			ctx.Request.Header.Set(k, v)
		}
	}
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		ctx.Request.SetBody(b)
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

func TestToken_RequiredOnSetupAPI(t *testing.T) {
	e := newEnv(t)
	src := filepath.Join(e.dir, "x.zip")
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/setup/checks?port=4747&config_path=" + e.configPath, nil},
		{"POST", "/api/setup/database/test", map[string]any{"driver": "sqlite", "path": e.dbPath}},
		{"POST", "/api/setup/complete", completeBody(e)},
		{"GET", "/api/setup/backups?config_path=" + e.configPath, nil},
		{"POST", "/api/setup/backup-uploads", map[string]any{"config_path": e.configPath, "name": "x.zip", "size": 5}},
		{"PUT", "/api/setup/backup-uploads/abc?offset=0", nil},
		{"POST", "/api/setup/backup/inspect", map[string]any{"config_path": e.configPath, "backup": map[string]any{"path": src}}},
		{"POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, "")},
		{"GET", "/api/setup/restore", nil},
	}
	for _, token := range []string{"", "wrong-token", testToken + "x"} {
		for _, r := range routes {
			headers := map[string]string{"Content-Type": "application/json", tokenHeader: token}
			code, out := callWith(t, e.s, r.method, r.path, headers, r.body)
			assert.Equal(t, 401, code, "%s %s with token %q", r.method, r.path, token)
			assert.Equal(t, "error", out["status"])
			assert.Contains(t, out["message"], "setup key")
		}
	}
	assert.NoFileExists(t, e.configPath)
	assert.NoFileExists(t, e.dbPath)

	code, _ := callWith(t, e.s, "GET", "/", map[string]string{tokenHeader: ""}, nil)
	assert.NotEqual(t, 401, code, "the page itself is public")
}

func TestStatus_WithoutTokenIsMinimal(t *testing.T) {
	e := newEnv(t)
	for _, token := range []string{"", "wrong-token"} {
		code, out := callWith(t, e.s, "GET", "/api/setup/status", map[string]string{tokenHeader: token}, nil)
		require.Equal(t, 200, code)
		assert.Equal(t, map[string]any{"needed": true, "ready": false, "token_required": true, "version": "test"}, data(out))
	}

	_, out := call(t, e.s, "GET", "/api/setup/status", nil)
	assert.Nil(t, data(out)["token_required"], "the right token sees the full status")
	assert.NotEmpty(t, data(out)["candidates"])

	e.s.finishSetup(finished{port: 4747, url: "http://localhost:4747"})
	_, out = callWith(t, e.s, "GET", "/api/setup/status", map[string]string{tokenHeader: ""}, nil)
	assert.Equal(t, map[string]any{"needed": false, "ready": false, "url": "http://localhost:4747"}, data(out), "the hand-off needs no token")
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
	assert.Equal(t, "production", cfg.App.Environment)
	assert.Equal(t, "*", cfg.Server.AllowedOrigins)
	assert.False(t, cfg.Cookie.Secure, "set up over plain HTTP, so login must work without HTTPS")
	userDir := filepath.Dir(e.configPath)
	assert.Equal(t, filepath.Join(userDir, "uploads"), cfg.Storage.LocalPath, "no shared folder: the data folder")
	assert.Equal(t, filepath.Join(userDir, "audio"), cfg.Calling.AudioDir)
	assert.DirExists(t, cfg.Storage.LocalPath)
	assert.DirExists(t, cfg.Calling.AudioDir)
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

func TestComplete_HTTPSKeepsSecureCookies(t *testing.T) {
	e := newEnv(t)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://chat.example.com", "X-Forwarded-Proto": "https"}
	code, out := callWith(t, e.s, "POST", "/api/setup/complete", headers, completeBody(e))
	require.Equal(t, 200, code, out)
	assert.Equal(t, "https://chat.example.com", data(out)["url"])
	cfg, err := config.Load(e.configPath)
	require.NoError(t, err)
	assert.Equal(t, "production", cfg.App.Environment)
	assert.Equal(t, "*", cfg.Server.AllowedOrigins)
	assert.True(t, cfg.Cookie.Secure)
	raw, _ := os.ReadFile(e.configPath)
	assert.NotContains(t, string(raw), "[cookie]")
}

func TestComplete_UsesSharedDataDir(t *testing.T) {
	e := newEnv(t)
	shared := filepath.Join(e.dir, "var", "whatomate")
	require.NoError(t, os.MkdirAll(filepath.Dir(shared), 0o750))
	e.s.o.SharedDataDir = shared
	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	require.Equal(t, 200, code, out)
	cfg, err := config.Load(e.configPath)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(shared, "uploads"), cfg.Storage.LocalPath)
	assert.Equal(t, filepath.Join(shared, "audio"), cfg.Calling.AudioDir)
	assert.DirExists(t, cfg.Storage.LocalPath)
	assert.DirExists(t, cfg.Calling.AudioDir)
}

func TestComplete_FallsBackWhenSharedDataDirUnwritable(t *testing.T) {
	e := newEnv(t)
	// The shared folder's parent is a file, so it can't be created.
	blocker := filepath.Join(e.dir, "blocked")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	e.s.o.SharedDataDir = filepath.Join(blocker, "whatomate")
	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	require.Equal(t, 200, code, out)
	cfg, err := config.Load(e.configPath)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(filepath.Dir(e.configPath), "uploads"), cfg.Storage.LocalPath)
	assert.Equal(t, filepath.Join(filepath.Dir(e.configPath), "audio"), cfg.Calling.AudioDir)
}

func TestDefaultSharedDataDir(t *testing.T) {
	assert.Equal(t, "/var/whatomate", DefaultSharedDataDir("linux"))
	assert.Empty(t, DefaultSharedDataDir("darwin"))
	assert.Empty(t, DefaultSharedDataDir("windows"))
}

func TestComplete_RefusesDirectoryAtFixedPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.Mkdir(cfgPath, 0o750))
	s := newServer(Options{
		Version: "test", Log: logf.New(logf.Opts{}),
		Target:  config.SetupTarget{Fixed: cfgPath},
		Checker: &Checker{PortFree: func(int) bool { return true }},
	}, 4747, testToken)
	dbPath := filepath.Join(dir, "whatomate.db")
	body := completeBody(env{configPath: cfgPath, dbPath: dbPath})
	code, out := call(t, s, "POST", "/api/setup/complete", body)
	assert.Equal(t, 409, code)
	assert.Contains(t, out["message"], "already exists")
	assert.NoFileExists(t, dbPath, "nothing is migrated")
}

func TestComplete_RetryAfterLateMigrationFailure(t *testing.T) {
	e := newEnv(t)
	e.s.migrate = func(db *gorm.DB, a *config.DefaultAdminConfig) error {
		if err := database.RunMigrationWithProgress(db, a); err != nil {
			return err
		}
		return errors.New("seed widgets: disk full")
	}
	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	require.Equal(t, 500, code, out)
	assert.NoFileExists(t, e.configPath)

	e.s.migrate = database.RunMigrationWithProgress
	code, out = call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	require.Equal(t, 200, code, out)
	var users []models.User
	require.NoError(t, openSQLite(t, e.dbPath).Find(&users).Error)
	require.Len(t, users, 1)
	assert.Equal(t, "ana@example.com", users[0].Email)
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

func TestComplete_KeepsConfigThatAppearsDuringSetup(t *testing.T) {
	e := newEnv(t)
	e.s.beforeWrite = func() {
		require.NoError(t, os.WriteFile(e.configPath, []byte("[app]\n"), 0o600))
	}
	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code)
	assert.Contains(t, out["message"], "already exists")
	raw, _ := os.ReadFile(e.configPath)
	assert.Equal(t, "[app]\n", string(raw), "the other config is untouched")
	entries, err := os.ReadDir(filepath.Dir(e.configPath))
	require.NoError(t, err)
	for _, en := range entries {
		assert.NotContains(t, en.Name(), ".tmp", "no temp file left behind")
	}
	select {
	case <-e.s.done:
		t.Fatal("completion was signalled")
	default:
	}
}

func TestComplete_RefusesUserWithSameEmailItDidNotCreate(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(e.dbPath), 0o750))
	db := openSQLite(t, e.dbPath)
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{Email: "ana@example.com", Password: "old-password", FullName: "Old Ana"}))

	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code)
	assert.Equal(t, "This database already has users. Choose an empty database.", out["message"])
	assert.NoFileExists(t, e.configPath)
	var user models.User
	require.NoError(t, db.Where("email = ?", "ana@example.com").First(&user).Error)
	assert.Equal(t, "Old Ana", user.FullName)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("old-password")), "password unchanged")
}

func TestComplete_RetryWithDifferentEmail(t *testing.T) {
	e := newEnv(t)
	blocker := filepath.Dir(e.configPath)
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	dbPath := filepath.Join(e.dir, "db", "whatomate.db")
	body := completeBody(e)
	body["database"] = map[string]any{"driver": "sqlite", "path": dbPath}
	code, _ := call(t, e.s, "POST", "/api/setup/complete", body)
	require.Equal(t, 500, code)
	require.NoError(t, os.Remove(blocker))

	// A copy of the seeded database holds the same admin but is another
	// database, so setup didn't create its users.
	copyPath := filepath.Join(e.dir, "db", "copy.db")
	require.NoError(t, openSQLite(t, dbPath).Exec("VACUUM INTO ?", copyPath).Error)
	body["database"] = map[string]any{"driver": "sqlite", "path": copyPath}
	code, _ = call(t, e.s, "POST", "/api/setup/complete", body)
	require.Equal(t, 409, code, "a different database with users")

	body["database"] = map[string]any{"driver": "sqlite", "path": dbPath}
	body["admin"] = map[string]any{"full_name": "Ana", "email": "owner@example.com", "password": "second-password"}
	code, out := call(t, e.s, "POST", "/api/setup/complete", body)
	require.Equal(t, 200, code, out)

	var users []models.User
	require.NoError(t, openSQLite(t, dbPath).Find(&users).Error)
	require.Len(t, users, 1)
	assert.Equal(t, "owner@example.com", users[0].Email)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte("second-password")))
}

func TestPost_RequiresJSON(t *testing.T) {
	e := newEnv(t)
	dbBody := map[string]any{"driver": "sqlite", "path": e.dbPath}
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		code, out := callAs(t, e.s, "POST", "/api/setup/complete", ct, completeBody(e))
		assert.Equal(t, 415, code, ct)
		assert.Equal(t, "error", out["status"], ct)
		code, _ = callAs(t, e.s, "POST", "/api/setup/database/test", ct, dbBody)
		assert.Equal(t, 415, code, ct)
	}
	assert.NoFileExists(t, e.configPath)
	assert.NoFileExists(t, e.dbPath)

	code, out := callAs(t, e.s, "POST", "/api/setup/database/test", "Application/JSON; charset=utf-8", dbBody)
	assert.Equal(t, 200, code)
	assert.Equal(t, true, data(out)["ok"])
}

func TestComplete_ValidatesInput(t *testing.T) {
	e := newEnv(t)
	mutate := map[string]func(map[string]any){
		"bad port":        func(b map[string]any) { b["port"] = 0 },
		"unknown path":    func(b map[string]any) { b["config_path"] = filepath.Join(e.dir, "elsewhere.toml") },
		"no organization": func(b map[string]any) { b["organization"] = "  " },
		"bad email": func(b map[string]any) {
			b["admin"] = map[string]any{"full_name": "A", "email": "nope", "password": "longenough"}
		},
		"short password": func(b map[string]any) {
			b["admin"] = map[string]any{"full_name": "A", "email": "a@b.co", "password": "short"}
		},
		"password over 72 bytes": func(b map[string]any) {
			// 37 characters, 74 bytes: bcrypt reads at most 72 bytes.
			b["admin"] = map[string]any{"full_name": "A", "email": "a@b.co", "password": strings.Repeat("é", 37)}
		},
		"no full name": func(b map[string]any) {
			b["admin"] = map[string]any{"full_name": "", "email": "a@b.co", "password": "longenough"}
		},
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
	assert.NoFileExists(t, e.dbPath, "validation runs before any database work")
}

func TestComplete_LongPasswordMessage(t *testing.T) {
	e := newEnv(t)
	body := completeBody(e)
	body["admin"] = map[string]any{"full_name": "A", "email": "a@b.co", "password": strings.Repeat("x", 73)}
	code, out := call(t, e.s, "POST", "/api/setup/complete", body)
	assert.Equal(t, 400, code)
	assert.Equal(t, "password must be at most 72 bytes", out["message"])
}

func TestUnknownAPIIs404(t *testing.T) {
	e := newEnv(t)
	code, _ := call(t, e.s, "GET", "/api/contacts", nil)
	assert.Equal(t, 404, code)
	code, _ = call(t, e.s, "GET", "/api/setup/complete", nil)
	assert.Equal(t, 404, code, "complete is POST only")
}
