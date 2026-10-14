package database

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm/schema"
)

func TestMySQLDSN(t *testing.T) {
	dsn, err := mysqlDSN(&config.DatabaseConfig{Host: "db", Port: 3306, User: "u", Password: "p@ss:w/rd?&", Name: "app", SSLMode: "disable"}, "", 0)
	require.NoError(t, err)
	c, err := mysqldrv.ParseDSN(dsn)
	require.NoError(t, err)
	assert.Equal(t, "u", c.User)
	assert.Equal(t, "p@ss:w/rd?&", c.Passwd)
	assert.Equal(t, "db:3306", c.Addr)
	assert.Equal(t, "app", c.DBName)
	assert.True(t, c.ParseTime)
	assert.Equal(t, time.UTC, c.Loc)
	assert.True(t, c.ClientFoundRows)
	assert.Equal(t, "'+00:00'", c.Params["time_zone"])
	assert.Equal(t, mysqlSQLMode, c.Params["sql_mode"])
	assert.NotContains(t, c.Params["sql_mode"], "NO_ZERO_DATE")
	assert.Equal(t, "false", c.TLSConfig)
	assert.NotContains(t, c.Params, "transaction_isolation", "the probe connection does not know the server yet")
	assert.NotContains(t, c.Params, "tx_isolation")
}

func TestMySQLDSN_ReadCommitted(t *testing.T) {
	for _, tc := range []struct {
		mariaDB    bool
		set, unset string
	}{{false, "transaction_isolation", "tx_isolation"}, {true, "tx_isolation", "transaction_isolation"}} {
		dsn, err := mysqlDSN(&config.DatabaseConfig{Host: "db", Port: 3306}, mysqlIsolationVar(tc.mariaDB), 0)
		require.NoError(t, err)
		c, err := mysqldrv.ParseDSN(dsn)
		require.NoError(t, err)
		assert.Equal(t, "'READ-COMMITTED'", c.Params[tc.set], "mariaDB=%v", tc.mariaDB)
		assert.NotContains(t, c.Params, tc.unset, "mariaDB=%v", tc.mariaDB)
		assert.Equal(t, mysqlSQLMode, c.Params["sql_mode"])
	}
}

func TestMySQLDSN_IPv6Host(t *testing.T) {
	dsn, err := mysqlDSN(&config.DatabaseConfig{Host: "::1", Port: 3307}, "", 0)
	require.NoError(t, err)
	c, err := mysqldrv.ParseDSN(dsn)
	require.NoError(t, err)
	assert.Equal(t, "[::1]:3307", c.Addr)
}

func TestMySQLDSN_DialTimeout(t *testing.T) {
	cfg := &config.DatabaseConfig{Host: "db", Port: 3306}
	dsn, err := mysqlDSN(cfg, "", 0)
	require.NoError(t, err)
	c, err := mysqldrv.ParseDSN(dsn)
	require.NoError(t, err)
	assert.Zero(t, c.Timeout, "the app's connections keep the driver default")

	dsn, err = mysqlDSN(cfg, "", probeTimeout)
	require.NoError(t, err)
	c, err = mysqldrv.ParseDSN(dsn)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, c.Timeout)
}

func TestMySQLDSN_SSLModes(t *testing.T) {
	for mode, want := range map[string]string{"": "false", "disable": "false", "require": "skip-verify", "verify-ca": "true", "verify-full": "true", "preferred": "preferred"} {
		dsn, err := mysqlDSN(&config.DatabaseConfig{Host: "h", Port: 3306, SSLMode: mode}, "", 0)
		require.NoError(t, err, mode)
		c, err := mysqldrv.ParseDSN(dsn)
		require.NoError(t, err, mode)
		assert.Equal(t, want, c.TLSConfig, mode)
	}
	_, err := mysqlDSN(&config.DatabaseConfig{SSLMode: "bogus"}, "", 0)
	assert.EqualError(t, err, `unsupported ssl_mode "bogus" for mysql`)
}

func TestCheckMySQLServer(t *testing.T) {
	ok := []struct {
		version string
		mariaDB bool
	}{{"8.0.19", false}, {"8.0.44", false}, {"8.4.3", false}, {"9.1.0", false}, {"10.6.20-MariaDB", true}, {"11.4.2-MariaDB-ubu2404", true}}
	for _, c := range ok {
		mariaDB, err := checkMySQLServer(c.version, "utf8mb4")
		require.NoError(t, err, c.version)
		assert.Equal(t, c.mariaDB, mariaDB, c.version)
	}
	for version, msg := range map[string]string{
		"8.0.18":         "MySQL 8.0.19 or later is required (server is 8.0.18)",
		"8.0.13":         "MySQL 8.0.19 or later is required (server is 8.0.13)",
		"5.7.44-log":     "MySQL 8.0.19 or later is required (server is 5.7.44-log)",
		"10.5.9-MariaDB": "MariaDB 10.6 or later is required (server is 10.5.9-MariaDB)",
		"garbage":        `cannot parse server version "garbage"`,
	} {
		_, err := checkMySQLServer(version, "utf8mb4")
		assert.EqualError(t, err, msg, version)
	}
	_, err := checkMySQLServer("8.4.3", "latin1")
	assert.EqualError(t, err, `database character set is "latin1"; create it with CHARACTER SET utf8mb4`)
}

func TestOpen_ClosesMySQLPoolWhenGormOpenFails(t *testing.T) {
	pool, err := sql.Open("mysql", "u:p@tcp(127.0.0.1:1)/x?timeout=2s")
	require.NoError(t, err)
	d := portableDialector{Dialector: mysql.New(mysql.Config{Conn: pool}), types: mysqlTypes, pool: pool}
	_, err = open(d, &config.DatabaseConfig{Driver: config.DriverMySQL}, false)
	require.Error(t, err)
	assert.EqualError(t, pool.Ping(), "sql: database is closed")
}

func TestMySQLColumnTypes(t *testing.T) {
	six := 6
	for _, c := range []struct {
		types                 map[string]string
		uuid, jsonb, text, ts string
	}{
		{mysqlTypes, "char(36)", "json", "longtext", "datetime(6) NULL"},
		{mariaDBTypes, "char(36)", "longtext", "longtext", "datetime(6) NULL"},
	} {
		d := portableDialector{Dialector: mysql.New(mysql.Config{DefaultDatetimePrecision: &six}), types: c.types}
		assert.Equal(t, c.uuid, d.DataTypeOf(&schema.Field{DataType: "uuid"}))
		assert.Equal(t, c.jsonb, d.DataTypeOf(&schema.Field{DataType: "jsonb"}))
		assert.Equal(t, c.text, d.DataTypeOf(&schema.Field{DataType: "text"}))
		assert.Equal(t, c.ts, d.DataTypeOf(&schema.Field{DataType: schema.Time, TagSettings: map[string]string{}}))
	}
}

func TestMySQLColumnSuffixes(t *testing.T) {
	s, err := schema.Parse(&models.UserPasskey{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	d := portableDialector{Dialector: mysql.New(mysql.Config{}), types: mysqlTypes, columnSuffixes: mysqlColumnSuffixes}
	assert.Equal(t, "varchar(1400) CHARACTER SET ascii COLLATE ascii_bin", d.DataTypeOf(s.LookUpField("CredentialID")))
	assert.Equal(t, "varchar(255)", d.DataTypeOf(s.LookUpField("RPID")))

	s, err = schema.Parse(&models.KVEntry{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	assert.Equal(t, "varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", d.DataTypeOf(s.LookUpField("Key")))

	s, err = schema.Parse(&models.ScheduledItem{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	assert.Equal(t, "varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", d.DataTypeOf(s.LookUpField("SetName")))
	assert.Equal(t, "varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", d.DataTypeOf(s.LookUpField("Item")))
}

func TestMySQLStringLiteral(t *testing.T) {
	for in, want := range map[string]string{"hello": `'hello'`, "it's": `'it''s'`, `a\b`: `'a\\b'`, "": `''`} {
		assert.Equal(t, want, mysqlStringLiteral(in), in)
	}
}

func TestNormalizeMySQLDefault(t *testing.T) {
	for in, want := range map[string]string{`_utf8mb4\'[]\'`: "[]", `'[]'`: "[]", `('{}')`: "{}", `_utf8mb4\'{}\'`: "{}"} {
		assert.Equal(t, want, normalizeMySQLDefault(in), in)
	}

	// Defaults of these kinds come back in the server's own spelling.
	for _, c := range []struct {
		value    any
		reported string
		same     bool
	}{
		{true, "1", true}, {false, "0", true}, {true, "0", false}, {false, "1", false},
		{0.7, "0.70", true}, {0.7, "0.7", true}, {0.7, "0.75", false}, {0.7, "x", false},
	} {
		f := &schema.Field{DefaultValueInterface: c.value}
		assert.Equal(t, c.same, sameTypedDefault(f, c.reported), "%v vs %s", c.value, c.reported)
	}
	assert.False(t, sameTypedDefault(&schema.Field{DefaultValueInterface: "x"}, "x"), "strings compare as GORM does")
}
