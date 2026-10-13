package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
	"github.com/trysoft/whatomate/test/testutil"
)

// suiteKeys are the keys (as SCAN MATCH patterns) the storetest suites and
// these tests write.
var suiteKeys = []string{
	"missing", "a", "forever", "short", "d1", "d2", "never-set", "once", "Abc", "abc",
	"chatbot:settings:*", "p:*", "g:*", "rl:*", "set", "other", "S", "s",
}

// redisClient returns the test Redis client with the suite keys removed
// before and after the test, so keys left by an interrupted run do not leak
// into the next one.
func redisClient(t *testing.T) *redis.Client {
	c := testutil.SetupTestRedis(t)
	if c == nil {
		t.Skip("test Redis unavailable")
	}
	if db := c.Options().DB; db != 15 {
		t.Skipf("test Redis must use db 15, not %d: the suite deletes keys", db)
	}
	deleteSuiteKeys(t, c)
	t.Cleanup(func() { deleteSuiteKeys(t, c) })
	return c
}

func deleteSuiteKeys(t *testing.T, c *redis.Client) {
	ctx := context.Background()
	for _, pattern := range suiteKeys {
		iter := c.Scan(ctx, 0, pattern, 100).Iterator()
		for iter.Next(ctx) {
			if err := c.Del(ctx, iter.Val()).Err(); err != nil {
				t.Fatalf("delete %q: %v", iter.Val(), err)
			}
		}
		if err := iter.Err(); err != nil {
			t.Fatalf("scan %q: %v", pattern, err)
		}
	}
}

func newRedisStore(t *testing.T) *store.Redis {
	return store.NewRedis(redisClient(t))
}

func TestRedisKV(t *testing.T) {
	storetest.RunKV(t, func(t *testing.T) store.KV { return newRedisStore(t) })
}

// noGetDelHook rejects GETDEL the way Redis servers before 6.2 do.
type noGetDelHook struct{}

func (noGetDelHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (noGetDelHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "getdel" {
			return errors.New("ERR unknown command 'getdel'")
		}
		return next(ctx, cmd)
	}
}

func (noGetDelHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if cmd.Name() == "getdel" {
				return errors.New("ERR unknown command 'getdel'")
			}
		}
		return next(ctx, cmds)
	}
}

func TestRedisKVWithoutGetDel(t *testing.T) {
	storetest.RunKV(t, func(t *testing.T) store.KV {
		opts := *redisClient(t).Options()
		c := redis.NewClient(&opts)
		t.Cleanup(func() { _ = c.Close() })
		c.AddHook(noGetDelHook{})
		return store.NewRedis(c)
	})
}

func TestRedisCounter(t *testing.T) {
	storetest.RunCounter(t, func(t *testing.T) store.Counter { return newRedisStore(t) })
}

func TestRedisCounterRepairsMissingTTL(t *testing.T) {
	ctx := context.Background()
	c := redisClient(t)
	require.NoError(t, c.Set(ctx, "rl:no-ttl", "5", 0).Err())

	n, reset, err := store.NewRedis(c).Incr(ctx, "rl:no-ttl", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(6), n)
	assert.Greater(t, reset, 50*time.Second)
	assert.LessOrEqual(t, reset, time.Minute)
	ttl, err := c.TTL(ctx, "rl:no-ttl").Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
}

func TestRedisScheduler(t *testing.T) {
	storetest.RunScheduler(t, func(t *testing.T) store.Scheduler { return newRedisStore(t) })
}

func TestRedisBroker(t *testing.T) {
	storetest.RunBroker(t, func(t *testing.T) store.Broker { return newRedisStore(t) })
}
