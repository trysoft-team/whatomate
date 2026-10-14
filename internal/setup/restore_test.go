package setup

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	return waitRestoreOn(t, e.s)
}

func waitRestoreOn(t *testing.T, s *server) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		code, out := call(t, s, "GET", "/api/setup/restore", nil)
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
	require.NoError(t, os.MkdirAll(filepath.Dir(e.dbPath), 0o750))
	used := openSQLite(t, e.dbPath)
	require.NoError(t, database.RunMigrationWithProgress(used, &config.DefaultAdminConfig{
		Email: "someone@example.com", Password: "password-123", FullName: "Someone", OrganizationName: "Theirs",
	}))
	code, out = call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	assert.Equal(t, 409, code, out)

	assert.NoFileExists(t, e.configPath)
	cand, _ := e.s.lookup(e.configPath)
	assert.NoFileExists(t, pendingPath(cand), "a refused restore leaves no marker")
}

// seedUsers gives the database at e.dbPath a user that is not in the backup.
func seedUsers(t *testing.T, e env) *gorm.DB {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(e.dbPath), 0o750))
	db := openSQLite(t, e.dbPath)
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{
		Email: "someone@example.com", Password: "password-123", FullName: "Someone", OrganizationName: "Theirs",
	}))
	return db
}

func sqliteTarget(path string) string {
	return databaseTarget(config.DatabaseConfig{Driver: config.DriverSQLite, Path: path})
}

func TestRestore_AcceptsDatabaseLeftByStoppedRestore(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, oldKey, false)
	db := seedUsers(t, e)
	cand, _ := e.s.lookup(e.configPath)
	// What a restore that stopped after loading the data leaves behind.
	require.NoError(t, markRestorePending(cand, sqliteTarget(e.dbPath)))

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	require.Equal(t, 200, code, out)
	require.Equal(t, false, waitRestore(t, e)["failed"])
	<-e.s.done

	assert.FileExists(t, e.configPath)
	assert.NoFileExists(t, pendingPath(cand), "the marker goes once the config is written")
	var emails []string
	require.NoError(t, db.Model(&models.User{}).Pluck("email", &emails).Error)
	assert.Equal(t, []string{"old@example.com"}, emails, "the tables were emptied before loading")
}

func TestRestore_RefusesDatabaseWithUsersMarkedForAnotherTarget(t *testing.T) {
	e := newEnv(t)
	src := makeBackup(t, e.dir, oldKey, false)
	seedUsers(t, e)
	cand, _ := e.s.lookup(e.configPath)
	require.NoError(t, markRestorePending(cand, sqliteTarget(filepath.Join(e.dir, "other.db"))))

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	assert.Equal(t, 409, code, out)
	assert.Contains(t, out["message"], "already has users")
	assert.True(t, restorePending(cand, sqliteTarget(filepath.Join(e.dir, "other.db"))), "the refusal leaves the marker alone")
}

func TestComplete_IgnoresRestoreMarker(t *testing.T) {
	e := newEnv(t)
	seedUsers(t, e)
	cand, _ := e.s.lookup(e.configPath)
	require.NoError(t, markRestorePending(cand, sqliteTarget(e.dbPath)))
	code, out := call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code, "a fresh setup never takes a database with users")
	assert.Contains(t, out["message"], "already has users")

	// An empty database: the fresh setup succeeds and drops the stale marker.
	body := completeBody(e)
	body["database"] = map[string]any{"driver": "sqlite", "path": filepath.Join(e.dir, "fresh.db")}
	code, out = call(t, e.s, "POST", "/api/setup/complete", body)
	require.Equal(t, 200, code, out)
	assert.NoFileExists(t, pendingPath(cand))
}

func TestRestore_RetryAfterFailureAfterDataLoaded(t *testing.T) {
	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, oldKey, false)
	cand, _ := e.s.lookup(e.configPath)
	// The data is committed; then writing the config fails.
	e.s.beforeWrite = func() { _ = os.WriteFile(e.configPath, []byte("[app]\n"), 0o600) }

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	require.Equal(t, 200, code, out)
	st := waitRestore(t, e)
	require.Equal(t, true, st["failed"], st)
	assert.Contains(t, st["error"], "already exists")
	assert.True(t, restorePending(cand, sqliteTarget(e.dbPath)))
	if os.PathSeparator == '/' {
		fi, err := os.Stat(pendingPath(cand))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	marker, err := os.ReadFile(pendingPath(cand))
	require.NoError(t, err)
	assert.Equal(t, sqliteTarget(e.dbPath)+"\n", string(marker), "only the database's identity")
	var n int64
	require.NoError(t, openSQLite(t, e.dbPath).Model(&models.User{}).Count(&n).Error)
	require.Equal(t, int64(1), n, "the backup's users are in the database")

	// The same session retries, and so does a restarted setup.
	require.NoError(t, os.Remove(e.configPath))
	e.s.beforeWrite = nil
	for _, s := range []*server{e.s, newServer(e.s.o, 4747, testToken)} {
		s.restoreAckWait = 0
		code, out = call(t, s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
		require.Equal(t, 200, code, out)
		require.Equal(t, false, waitRestoreOn(t, s)["failed"])
		<-s.done
		cfg, err := config.Load(e.configPath)
		require.NoError(t, err)
		assert.Equal(t, oldKey, cfg.App.EncryptionKey)
		assert.NoFileExists(t, pendingPath(cand))
		if s == e.s {
			// Make the restarted setup start from the same leftovers.
			require.NoError(t, os.Remove(e.configPath))
			require.NoError(t, markRestorePending(cand, sqliteTarget(e.dbPath)))
		}
	}
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

func TestRestore_RefusesFailedFreshAttemptWithOtherUsers(t *testing.T) {
	e := newEnv(t)
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

	db := openSQLite(t, e.dbPath)
	var admin models.User
	require.NoError(t, db.Where("email = ?", "ana@example.com").First(&admin).Error)
	require.NoError(t, db.Create(&models.User{OrganizationID: admin.OrganizationID, Email: "someone-else@example.com", FullName: "Someone Else"}).Error)

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	assert.Equal(t, 409, code, out)
}

func TestRestore_FilesFailStillWritesConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	e := newEnv(t)
	e.s.restoreAckWait = 0
	src := makeBackup(t, e.dir, oldKey, true)
	cand, _ := e.s.lookup(e.configPath)
	media := mediaDir(cand)
	require.NoError(t, os.MkdirAll(media, 0o750))
	require.NoError(t, os.Chmod(media, 0o500))
	t.Cleanup(func() { _ = os.Chmod(media, 0o750) })

	code, out := call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	require.Equal(t, 200, code, out)
	st := waitRestore(t, e)
	assert.Equal(t, false, st["failed"], st)
	assert.Equal(t, true, st["files_failed"], st)
	assert.NotEmpty(t, st["error"])
	<-e.s.done
	assert.FileExists(t, e.configPath)
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

	// Until the hand-off, setup is not finished yet; nothing else may start.
	code, _ = call(t, e.s, "POST", "/api/setup/complete", completeBody(e))
	assert.Equal(t, 409, code)
	code, _ = call(t, e.s, "POST", "/api/setup/restore", restoreBody(e, map[string]any{"path": src}, oldKey))
	assert.Equal(t, 409, code)

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

func TestRestore_IntoPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_BACKUP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_BACKUP_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("TEST_BACKUP_DATABASE_URL is not a valid URL")
	}
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
