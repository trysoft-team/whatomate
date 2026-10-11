package testutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDriver(t *testing.T) {
	for env, want := range map[string]string{"": "postgres", "postgres": "postgres", " SQLite ": "sqlite"} {
		t.Setenv("TEST_DB_DRIVER", env)
		assert.Equal(t, want, Driver(), "TEST_DB_DRIVER=%q", env)
	}

	for _, env := range []string{"mysql", "sqlserver", "sqlite3"} {
		t.Setenv("TEST_DB_DRIVER", env)
		assert.PanicsWithValue(t, `TEST_DB_DRIVER="`+env+`" is not supported; use "postgres" or "sqlite"`, func() { Driver() }, env)
	}
}
