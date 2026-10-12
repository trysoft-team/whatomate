package database_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/database/dialect"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Open(&config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "w.db")}, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{Email: "a@b.c", Password: "pw", FullName: "A"}))
	return db
}

func TestOpen_RejectsUnsupportedDrivers(t *testing.T) {
	for _, d := range []string{"sqlserver", "oracle"} {
		_, err := database.Open(&config.DatabaseConfig{Driver: d}, false)
		assert.EqualError(t, err, `database driver "`+d+`" is not supported yet`)
	}
}

func TestOpenSQLite_MigratesAndSeeds(t *testing.T) {
	db := openSQLite(t)
	assert.Equal(t, "sqlite", dialect.Of(db).Name())

	var n int64
	require.NoError(t, db.Model(&models.User{}).Count(&n).Error)
	assert.EqualValues(t, 1, n, "default admin seeded")
	require.NoError(t, db.Model(&models.UserOrganization{}).Count(&n).Error)
	assert.EqualValues(t, 1, n, "default admin linked to its organization")

	var cols []struct {
		Name string
		Type string
	}
	require.NoError(t, db.Raw("PRAGMA table_info(contacts)").Scan(&cols).Error)
	types := map[string]string{}
	for _, c := range cols {
		types[c.Name] = strings.ToLower(c.Type)
	}
	assert.Equal(t, "text", types["tags"], "jsonb mapped to text")
	assert.Equal(t, "text", types["id"], "uuid mapped to text")

	// Running migrations twice is a no-op.
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{Email: "a@b.c", Password: "pw", FullName: "A"}))
	require.NoError(t, db.Model(&models.UserOrganization{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

func TestOpenSQLite_RepeatMigrationRebuildsNothing(t *testing.T) {
	db := openSQLite(t)

	type schemaObject struct {
		Type     string
		Name     string
		SQL      string
		Rootpage int64
	}
	snapshot := func() []schemaObject {
		var objs []schemaObject
		require.NoError(t, db.Raw(`SELECT type, name, COALESCE(sql, '') AS sql, rootpage FROM sqlite_master ORDER BY name`).Scan(&objs).Error)
		return objs
	}

	ddl := regexp.MustCompile(`(?i)^\s*(CREATE TABLE|ALTER TABLE|DROP TABLE)|__temp`)
	var mu sync.Mutex
	var rebuilds []string
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:capture_ddl", func(tx *gorm.DB) {
		if sql := tx.Statement.SQL.String(); ddl.MatchString(sql) {
			mu.Lock()
			rebuilds = append(rebuilds, sql)
			mu.Unlock()
		}
	}))

	before := snapshot()
	require.NoError(t, database.RunMigrationWithProgress(db, &config.DefaultAdminConfig{Email: "a@b.c", Password: "pw", FullName: "A"}))
	assert.Empty(t, rebuilds, "second migration must not create, alter or copy tables")
	assert.Equal(t, before, snapshot())
}

func TestOpenSQLite_TimeArgsAreUTC(t *testing.T) {
	db := openSQLite(t)
	org := models.Organization{Name: "o", Slug: "o"}
	require.NoError(t, db.Create(&org).Error)
	local := time.FixedZone("CAT", 2*3600)
	since := time.Now().In(local).Add(-time.Minute)
	var n int64
	require.NoError(t, db.Model(&models.Organization{}).Where("id = ? AND created_at >= ?", org.ID, since).Count(&n).Error)
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

func TestOpenSQLite_ConcurrentTransactions(t *testing.T) {
	db := openSQLite(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- db.Transaction(func(tx *gorm.DB) error {
				return tx.Create(&models.Organization{Name: "o", Slug: fmt.Sprintf("tx-%d", i)}).Error
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

func TestOpenSQLite_QueryWhileRowsOpen(t *testing.T) {
	db := openSQLite(t)
	require.NoError(t, db.Create(&models.Organization{Name: "o", Slug: "o"}).Error)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.WithContext(ctx).Model(&models.Organization{}).Rows()
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var seen int
	for rows.Next() {
		seen++
		var n int64
		require.NoError(t, db.WithContext(ctx).Model(&models.Organization{}).Count(&n).Error)
		assert.Positive(t, n)
	}
	require.NoError(t, rows.Err())
	assert.Positive(t, seen)
}

func TestOpenSQLite_NewFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	db, err := database.Open(&config.DatabaseConfig{Driver: "sqlite", Path: path}, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.Exec("CREATE TABLE t (x INTEGER)").Error)
	require.NoError(t, db.Exec("INSERT INTO t VALUES (1)").Error)

	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if p != path && errors.Is(err, os.ErrNotExist) {
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), p)
	}
}

func TestOpenSQLite_KeepsExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	path := filepath.Join(t.TempDir(), "w.db")
	require.NoError(t, os.WriteFile(path, nil, 0o640))
	require.NoError(t, os.Chmod(path, 0o640))
	db, err := database.Open(&config.DatabaseConfig{Driver: "sqlite", Path: path}, false)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestOpenSQLite_MissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	_, err := database.Open(&config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(dir, "w.db")}, false)
	assert.EqualError(t, err, fmt.Sprintf("sqlite database directory %q does not exist", dir))
	_, statErr := os.Stat(dir)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "directory must not be created")
}

func TestOpenSQLite_PoolSize(t *testing.T) {
	for _, tc := range []struct{ configured, want int }{{0, config.DefaultMaxOpenConns}, {4, 4}} {
		db, err := database.Open(&config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "w.db"), MaxOpenConns: tc.configured}, false)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		assert.Equal(t, tc.want, sqlDB.Stats().MaxOpenConnections, "max_open_conns = %d", tc.configured)
		require.NoError(t, sqlDB.Close())
	}
}

func TestOpenSQLite_PragmasOnEveryConnection(t *testing.T) {
	db := openSQLite(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		conn, err := sqlDB.Conn(ctx)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		var foreignKeys, busyTimeout int
		var journalMode string
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys))
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout))
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode))
		assert.Equal(t, 1, foreignKeys, "connection %d", i)
		assert.Equal(t, 10000, busyTimeout, "connection %d", i)
		assert.Equal(t, "wal", journalMode, "connection %d", i)
	}
	assert.GreaterOrEqual(t, sqlDB.Stats().OpenConnections, 3)
}

func TestOpenSQLite_NestedTransactionRollsBackToSavepoint(t *testing.T) {
	db := openSQLite(t)
	errInner := errors.New("inner")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Create(&models.Organization{Name: "outer", Slug: "outer"}).Error)
		err := tx.Transaction(func(tx2 *gorm.DB) error {
			require.NoError(t, tx2.Create(&models.Organization{Name: "inner", Slug: "inner"}).Error)
			return errInner
		})
		assert.ErrorIs(t, err, errInner)
		return nil
	}))
	var slugs []string
	require.NoError(t, db.Model(&models.Organization{}).Where("slug IN ?", []string{"outer", "inner"}).Pluck("slug", &slugs).Error)
	assert.Equal(t, []string{"outer"}, slugs)
}

func TestPostgresSchemaUnchanged(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	// This test alters the schema, so it only runs against a dedicated test database.
	if u, err := url.Parse(dsn); err != nil || !strings.HasSuffix(strings.TrimPrefix(u.Path, "/"), "_test") {
		t.Skip("TEST_DATABASE_URL does not name a *_test database")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	column := func(name string) (dataType, def string, err error) {
		err = db.Raw(`SELECT data_type, COALESCE(column_default, '') FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'contacts' AND column_name = ?`, name).
			Row().Scan(&dataType, &def)
		return dataType, def, err
	}

	if _, _, err := column("id"); errors.Is(err, sql.ErrNoRows) {
		require.NoError(t, database.AutoMigrate(db))
	}
	_, origDef, err := column("id")
	require.NoError(t, err)

	// Installs created before ids were generated in Go have this database default.
	require.NoError(t, db.Exec(`ALTER TABLE contacts ALTER COLUMN id SET DEFAULT gen_random_uuid()`).Error)
	t.Cleanup(func() {
		restore := `ALTER TABLE contacts ALTER COLUMN id DROP DEFAULT`
		if origDef != "" {
			restore = `ALTER TABLE contacts ALTER COLUMN id SET DEFAULT ` + origDef
		}
		assert.NoError(t, db.Exec(restore).Error)
	})

	require.NoError(t, database.AutoMigrate(db))

	idType, idDef, err := column("id")
	require.NoError(t, err)
	assert.Equal(t, "uuid", idType)
	assert.Equal(t, "gen_random_uuid()", idDef, "migration must not drop an existing id default")

	tagsType, _, err := column("tags")
	require.NoError(t, err)
	assert.Equal(t, "jsonb", tagsType)
}
