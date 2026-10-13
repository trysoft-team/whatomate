package store_test

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
	"github.com/trysoft/whatomate/test/testutil"
)

// newRedisStore returns a store on the test Redis database and deletes the
// keys the test created when it ends; keys that already existed are kept.
func newRedisStore(t *testing.T) *store.Redis {
	c := testutil.SetupTestRedis(t)
	if c == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	before := redisKeys(t, c)
	t.Cleanup(func() {
		for k := range redisKeys(t, c) {
			if before[k] {
				continue
			}
			if err := c.Del(context.Background(), k).Err(); err != nil {
				t.Errorf("delete %q: %v", k, err)
			}
		}
	})
	return store.NewRedis(c)
}

func redisKeys(t *testing.T, c *redis.Client) map[string]bool {
	ctx := context.Background()
	keys := map[string]bool{}
	iter := c.Scan(ctx, 0, "*", 100).Iterator()
	for iter.Next(ctx) {
		keys[iter.Val()] = true
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return keys
}

func TestRedisKV(t *testing.T) {
	storetest.RunKV(t, func(t *testing.T) store.KV { return newRedisStore(t) })
}

func TestRedisCounter(t *testing.T) {
	storetest.RunCounter(t, func(t *testing.T) store.Counter { return newRedisStore(t) })
}

func TestRedisScheduler(t *testing.T) {
	storetest.RunScheduler(t, func(t *testing.T) store.Scheduler { return newRedisStore(t) })
}

func TestRedisBroker(t *testing.T) {
	storetest.RunBroker(t, func(t *testing.T) store.Broker { return newRedisStore(t) })
}
