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
