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
