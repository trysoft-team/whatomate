package database

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
)

func TestPostgresDSN_RoundTripsValues(t *testing.T) {
	for _, password := range []string{
		"", "plain", "pass word", "it's", `say "hi"`, `a\b`, `a\\b`, `trailing\`, `\'`, `'leading`,
		"k=v host=evil", "line\nbreak", "pässwörd-密码",
	} {
		cfg := &config.DatabaseConfig{
			Host: "db.example.com", Port: 6543, User: "o'brien app", Password: password,
			Name: "ignored", SSLMode: "disable",
		}
		pc, err := pgconn.ParseConfig(postgresDSN(cfg, "whatomate"))
		require.NoError(t, err, "%q", password)
		assert.Equal(t, password, pc.Password, "%q", password)
		assert.Equal(t, "whatomate", pc.Database, "%q", password)
		assert.Equal(t, "db.example.com", pc.Host, "%q", password)
		assert.Equal(t, uint16(6543), pc.Port, "%q", password)
		assert.Equal(t, "o'brien app", pc.User, "%q", password)
		assert.Nil(t, pc.TLSConfig, "sslmode=disable for %q", password)
	}
}

func TestPostgresDSN_EmptySSLModeMeansPrefer(t *testing.T) {
	pc, err := pgconn.ParseConfig(postgresDSN(&config.DatabaseConfig{Host: "db", Port: 5432, User: "u"}, "app"))
	require.NoError(t, err)
	require.NotNil(t, pc.TLSConfig, "prefer tries TLS first")
	require.Len(t, pc.Fallbacks, 1, "then falls back to plaintext")
	assert.Nil(t, pc.Fallbacks[0].TLSConfig)
}
