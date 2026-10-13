package store_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

func TestMissingTables_MigratedDB(t *testing.T) {
	assert.Empty(t, store.MissingTables(testutil.SetupTestDB(t)))
}

// tempSQLite opens a private SQLite database so tables can be dropped without
// touching the shared test databases.
func tempSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Open(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "tables.db"),
	}, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestMissingTables_ReportsDroppedTable(t *testing.T) {
	db := tempSQLite(t)
	require.NoError(t, db.AutoMigrate(&models.KVEntry{}, &models.ScheduledItem{}, &models.CampaignJob{}))
	assert.Empty(t, store.MissingTables(db))

	require.NoError(t, db.Migrator().DropTable(&models.ScheduledItem{}))
	assert.Equal(t, []string{"scheduled_items"}, store.MissingTables(db))
}

func TestMissingTables_EmptyDB(t *testing.T) {
	assert.Equal(t, []string{"kv_entries", "scheduled_items", "campaign_jobs"}, store.MissingTables(tempSQLite(t)))
}
