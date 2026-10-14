package store_test

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/store"
)

func TestNew_EmbeddedWhenHostEmpty(t *testing.T) {
	s, err := store.New(&config.RedisConfig{Port: 6379}, cleanDB(t))
	require.NoError(t, err)
	defer s.Close()
	assert.True(t, s.Embedded())
	assert.Nil(t, s.Redis)
	assert.IsType(t, &store.Memory{}, s.Cache)
	assert.IsType(t, &store.SQLKV{}, s.State)
	assert.IsType(t, &store.Memory{}, s.Counter)
	assert.IsType(t, &store.SQLScheduler{}, s.Scheduler)
	assert.IsType(t, &store.MemoryBroker{}, s.Broker)
}

func TestNew_RedisWhenHostSet(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, _ := strconv.Atoi(u.Port())
	db, _ := strconv.Atoi(strings.TrimPrefix(u.Path, "/"))
	s, err := store.New(&config.RedisConfig{Host: u.Hostname(), Port: port, DB: db}, cleanDB(t))
	require.NoError(t, err)
	defer s.Close()
	assert.False(t, s.Embedded())
	assert.NotNil(t, s.Redis)
	for _, v := range []any{s.Cache, s.State, s.Counter, s.Scheduler, s.Broker} {
		assert.IsType(t, &store.Redis{}, v)
	}
}

func TestNew_RedisUnreachableFails(t *testing.T) {
	_, err := store.New(&config.RedisConfig{Host: "127.0.0.1", Port: 1}, cleanDB(t))
	assert.Error(t, err)
}
