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

func TestDatabaseSize(t *testing.T) {
	for _, kind := range []string{config.DriverSQLite, config.DriverPostgres, config.DriverMySQL} {
		t.Run(kind, func(t *testing.T) {
			assert.Positive(t, databaseSize(openKind(t, kind)))
		})
	}
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
