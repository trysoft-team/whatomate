package testutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
)

func TestDriver(t *testing.T) {
	for env, want := range map[string]string{"": "postgres", "postgres": "postgres", " SQLite ": "sqlite", "MySQL": "mysql"} {
		t.Setenv("TEST_DB_DRIVER", env)
		assert.Equal(t, want, Driver(), "TEST_DB_DRIVER=%q", env)
	}

	for _, env := range []string{"mariadb", "sqlserver", "sqlite3"} {
		t.Setenv("TEST_DB_DRIVER", env)
		assert.PanicsWithValue(t, `TEST_DB_DRIVER="`+env+`" is not supported; use "postgres", "sqlite" or "mysql"`, func() { Driver() }, env)
	}
}

func TestMySQLConfigFromDSN(t *testing.T) {
	cfg, err := MySQLConfigFromDSN("u:p@ss@tcp(db.example:3307)/whatomate_test_x?parseTime=true")
	require.NoError(t, err)
	assert.Equal(t, &config.DatabaseConfig{
		Driver: config.DriverMySQL, Host: "db.example", Port: 3307, User: "u", Password: "p@ss", Name: "whatomate_test_x",
		SSLMode: "disable", MaxOpenConns: 25, MaxIdleConns: 5, ConnMaxLifetime: 300,
	}, cfg)

	cfg, err = MySQLConfigFromDSN("u:p@tcp([::1]:3306)/whatomate_test")
	require.NoError(t, err)
	assert.Equal(t, "::1", cfg.Host)
	assert.Equal(t, 3306, cfg.Port)

	_, err = MySQLConfigFromDSN("u:p@tcp(db:3306)/whatomate")
	assert.EqualError(t, err, `TEST_MYSQL_DSN names database "whatomate"; it must start with whatomate_test`)
	_, err = MySQLConfigFromDSN("not a dsn")
	assert.ErrorContains(t, err, "TEST_MYSQL_DSN does not parse")
}
