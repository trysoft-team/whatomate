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
	if !mysqlScratchName.MatchString(name) {
		t.Errorf("refusing to drop database %q", name)
		return
	}
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
