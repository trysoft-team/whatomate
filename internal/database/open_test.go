package database_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	for _, d := range []string{"mysql", "sqlserver", "oracle"} {
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

	ddl := regexp.MustCompile("(?i)^\\s*(CREATE TABLE|ALTER TABLE|DROP TABLE)|__temp")
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

	beforeType, beforeDef, err := column("id")
	if errors.Is(err, sql.ErrNoRows) {
		// Fresh database: create the schema once so there is something to compare against.
		require.NoError(t, database.AutoMigrate(db))
		beforeType, beforeDef, err = column("id")
	}
	require.NoError(t, err)

	require.NoError(t, database.AutoMigrate(db))

	afterType, afterDef, err := column("id")
	require.NoError(t, err)
	assert.Equal(t, beforeType, afterType)
	assert.Equal(t, beforeDef, afterDef)
	assert.Equal(t, "uuid", afterType)

	tagsType, _, err := column("tags")
	require.NoError(t, err)
	assert.Equal(t, "jsonb", tagsType)
}
