package setup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
)

func loadRendered(t *testing.T, v Values) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, Render(v, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	return cfg
}

func TestRender_SQLite(t *testing.T) {
	cfg := loadRendered(t, Values{
		Environment: "development", Port: 4747, AllowedOrigins: "http://localhost:4747",
		Database:  config.DatabaseConfig{Driver: config.DriverSQLite, Path: "whatomate.db"},
		JWTSecret: "jwt-secret", EncryptionKey: "enc-key",
	})
	assert.Equal(t, "development", cfg.App.Environment)
	assert.Equal(t, "enc-key", cfg.App.EncryptionKey)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 4747, cfg.Server.Port)
	assert.Equal(t, "http://localhost:4747", cfg.Server.AllowedOrigins)
	assert.Equal(t, config.DriverSQLite, cfg.Database.Driver)
	assert.Equal(t, "whatomate.db", cfg.Database.Path)
	assert.Equal(t, "jwt-secret", cfg.JWT.Secret)
	assert.Empty(t, cfg.Redis.Host)
}

func TestRender_RoundTripsAwkwardValues(t *testing.T) {
	awkward := "p\"a\\ss\nword\t ü 🔑 \x01 '#"
	cfg := loadRendered(t, Values{
		Environment: "production", Port: 9000, AllowedOrigins: "https://chat.example.com",
		Database: config.DatabaseConfig{
			Driver: config.DriverPostgres, Host: "db.local", Port: 5433,
			User: "u\"ser", Password: awkward, Name: "whatomate", SSLMode: "require",
		},
		JWTSecret: awkward, EncryptionKey: awkward,
	})
	assert.Equal(t, awkward, cfg.Database.Password)
	assert.Equal(t, "u\"ser", cfg.Database.User)
	assert.Equal(t, awkward, cfg.JWT.Secret)
	assert.Equal(t, awkward, cfg.App.EncryptionKey)
	assert.Equal(t, 5433, cfg.Database.Port)
	assert.Equal(t, "require", cfg.Database.SSLMode)
	assert.Equal(t, "production", cfg.App.Environment)
}

func TestRender_HasNoAdminSection(t *testing.T) {
	out := string(Render(Values{Database: config.DatabaseConfig{Driver: config.DriverSQLite, Path: "x.db"}}, time.Now()))
	assert.NotContains(t, out, "default_admin")
	assert.NotContains(t, out, "[redis]")
}

func TestNewSecret(t *testing.T) {
	a, err := newSecret(48)
	require.NoError(t, err)
	b, err := newSecret(48)
	require.NoError(t, err)
	assert.Len(t, a, 64)
	assert.NotEqual(t, a, b)
}

func TestStoredDBPath(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "data", "whatomate")
	assert.Equal(t, "whatomate.db", storedDBPath(filepath.Join(base, "whatomate.db"), base))
	assert.Equal(t, filepath.Join("db", "w.db"), storedDBPath(filepath.Join(base, "db", "w.db"), base))
	other := filepath.Join(string(filepath.Separator), "elsewhere", "w.db")
	assert.Equal(t, other, storedDBPath(other, base))
	assert.Equal(t, other, storedDBPath(other, ""), "no base keeps the absolute path")
}
