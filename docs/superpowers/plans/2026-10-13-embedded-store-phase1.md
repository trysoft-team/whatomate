# Built-in Store (Phase 1: no Redis server) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** whatomate runs with no Redis server: short-lived data in memory, durable data in the main database, Redis optional via `[redis] host`.

**Architecture:** A new `internal/store` package defines `KV`, `Counter`, `Scheduler` and `Broker` interfaces with memory, SQL (GORM) and Redis implementations; `store.New` picks embedded or Redis mode from `[redis] host`. A database-backed campaign queue implements the existing `queue.Queue`/`queue.Consumer` interfaces. Every `*redis.Client` consumer (handlers, calling, assignment, middleware, worker) switches to the interfaces.

**Tech Stack:** Go 1.25, GORM (postgres / mysql / glebarez sqlite), go-redis v9 (Redis mode only), testify.

**Spec:** `docs/superpowers/specs/2026-10-13-embedded-store-design.md` (Phase 1 sections).

## Global Constraints

- Pure Go, `CGO_ENABLED=0`, Go 1.25. No new external services. Do not bump gorm v1.25.7.
- Database drivers and the multi-database layer are not changed.
- Existing configs that set `[redis] host` keep working exactly as today (same Redis key names).
- `[redis] host` empty → embedded mode; set → Redis mode.
- Column names must avoid reserved words on MySQL/SQL Server (`key`, `value`, `member`): use `kv_key`, `kv_value`, `item`.
- `LIKE` escaping uses `ESCAPE '!'` (a backslash escape is parsed differently by MySQL and PostgreSQL).
- Tests: PostgreSQL `whatomate_test` on port 5433 (`TEST_DATABASE_URL`), SQLite via `TEST_DB_DRIVER=sqlite`, MySQL DB names only `whatomate_test` / `whatomate_test_[0-9a-f]{8}`. Redis tests only when `TEST_REDIS_URL` is set.
- Test packages share one PostgreSQL/MySQL test database (and Redis db 15), and the store/queue tests delete their tables' rows: always run multi-package database test commands with `-p 1` (e.g. `go test -p 1 ./...`). SQLite runs are per-process and unaffected.
- Commit locally only on `feat/embedded-store`. No push.

## Review Focus

1. A refresh token presented twice at the same moment must log in only once → `TestKV_GetDelExactlyOnce` (Task 1 harness, run in Tasks 1, 3, 5).
2. A user stays logged in across a server restart in embedded mode → `TestSQLKV_SurvivesNewInstance` (Task 3).
3. Cache invalidation for an account named with `%`, `_`, `*` or `!` must not delete other keys → `TestKV_DelPrefixLiteral` (Task 1 harness).
4. A campaign job whose handler keeps failing is retried, then dropped after `MaxJobAttempts`, never looping forever → `TestDBConsumer_GivesUpAfterMaxAttempts` (Task 6).
5. An existing install with `[redis] host` set still uses Redis; one without it starts with no Redis → `TestNew_EmbeddedWhenHostEmpty`, `TestNew_RedisWhenHostSet` (Task 7).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/store/store.go` | Interfaces, `ErrNotFound`, `Stores`, `New` |
| `internal/store/memory.go` | Memory `KV` + `Counter` with janitor |
| `internal/store/sqlkv.go` | Database `KV` on `kv_entries` |
| `internal/store/sqlscheduler.go` | Database `Scheduler` on `scheduled_items` |
| `internal/store/broker.go` | In-process `Broker` |
| `internal/store/redis.go` | Redis `KV`, `Counter`, `Scheduler`, `Broker` |
| `internal/store/storetest/storetest.go` | Shared conformance tests (`RunKV`, `RunCounter`, `RunScheduler`, `RunBroker`) |
| `internal/store/*_test.go` | Run the conformance suites per implementation |
| `internal/models/store.go` | `KVEntry`, `ScheduledItem`, `CampaignJob` |
| `internal/queue/dbqueue.go` | Database `Queue` + `Consumer` |
| `internal/queue/pubsub.go` | Publisher/Subscriber on `store.Broker` |
| `internal/handlers/store_helpers.go` | Nil-safe cache helpers on `App` |

---

### Task 1: Store interfaces, memory store and conformance harness

**Files:**
- Create: `internal/store/store.go`, `internal/store/memory.go`, `internal/store/storetest/storetest.go`, `internal/store/memory_test.go`

**Interfaces:**
- Produces:
  - `store.ErrNotFound`
  - `type KV interface { Get(ctx, key string) ([]byte, error); Set(ctx, key string, val []byte, ttl time.Duration) error; Del(ctx, keys ...string) error; GetDel(ctx, key string) ([]byte, error); DelPrefix(ctx, prefix string) error; Ping(ctx) error }`
  - `type Counter interface { Incr(ctx, key string, window time.Duration) (int64, time.Duration, error) }`
  - `type Scheduler interface { Schedule(ctx, set, item string, at time.Time) error; Cancel(ctx, set string, items ...string) error; PopDue(ctx, set string, now time.Time, limit int) ([]string, error) }`
  - `type Broker interface { Publish(ctx, channel string, payload []byte) error; Subscribe(ctx, channel string, fn func([]byte)) (func(), error) }`
  - `store.NewMemory() *Memory` (implements `KV`, `Counter`; `Close()`)
  - `storetest.RunKV(t, func(t) store.KV)`, `storetest.RunCounter(t, func(t) store.Counter)`, `storetest.RunScheduler(t, func(t) store.Scheduler)`, `storetest.RunBroker(t, func(t) store.Broker)`

- [ ] **Step 1: Write `internal/store/store.go` (interfaces only for now)**

```go
// Package store holds the key/value, counter, scheduler and pub/sub backends
// whatomate uses for caches and short-lived state. Embedded mode keeps them in
// memory and the main database; Redis mode uses a Redis server.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by KV reads for a missing or expired key.
var ErrNotFound = errors.New("store: not found")

// KV is a key/value store with per-key expiry.
type KV interface {
	Get(ctx context.Context, key string) ([]byte, error)
	// Set stores val; ttl 0 means no expiry.
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	// GetDel returns and removes the value; concurrent callers for the same
	// key get it at most once.
	GetDel(ctx context.Context, key string) ([]byte, error)
	DelPrefix(ctx context.Context, prefix string) error
	Ping(ctx context.Context) error
}

// Counter is a fixed-window counter used for rate limiting.
type Counter interface {
	// Incr increments key, starting a new window of length window when the key
	// is new or its window ended. It returns the count in the current window
	// and the time until the window resets.
	Incr(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error)
}

// Scheduler holds items that become due at a time.
type Scheduler interface {
	// Schedule adds item to set, replacing any existing due time.
	Schedule(ctx context.Context, set, item string, at time.Time) error
	Cancel(ctx context.Context, set string, items ...string) error
	// PopDue removes and returns up to limit items due at or before now. Each
	// item is returned to exactly one caller.
	PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error)
}

// Broker delivers messages published on a channel to its subscribers.
type Broker interface {
	Publish(ctx context.Context, channel string, payload []byte) error
	// Subscribe calls fn for every message on channel until stop is called or
	// ctx is done.
	Subscribe(ctx context.Context, channel string, fn func([]byte)) (stop func(), err error)
}
```

- [ ] **Step 2: Write the conformance harness `internal/store/storetest/storetest.go`**

```go
// Package storetest holds conformance tests every store implementation runs.
package storetest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/store"
)

// RunKV runs the KV conformance suite. newKV must return an empty store.
func RunKV(t *testing.T, newKV func(t *testing.T) store.KV) {
	ctx := context.Background()

	t.Run("GetMissing", func(t *testing.T) {
		kv := newKV(t)
		_, err := kv.Get(ctx, "missing")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("SetGetOverwrite", func(t *testing.T) {
		kv := newKV(t)
		require.NoError(t, kv.Set(ctx, "a", []byte("1"), time.Hour))
		require.NoError(t, kv.Set(ctx, "a", []byte("2"), time.Hour))
		v, err := kv.Get(ctx, "a")
		require.NoError(t, err)
		assert.Equal(t, "2", string(v))
	})

	t.Run("NoExpiry", func(t *testing.T) {
		kv := newKV(t)
		require.NoError(t, kv.Set(ctx, "forever", []byte("x"), 0))
		v, err := kv.Get(ctx, "forever")
		require.NoError(t, err)
		assert.Equal(t, "x", string(v))
	})

	t.Run("Expiry", func(t *testing.T) {
		kv := newKV(t)
		require.NoError(t, kv.Set(ctx, "short", []byte("x"), 1100*time.Millisecond))
		time.Sleep(1500 * time.Millisecond)
		_, err := kv.Get(ctx, "short")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = kv.GetDel(ctx, "short")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("DelMany", func(t *testing.T) {
		kv := newKV(t)
		require.NoError(t, kv.Set(ctx, "d1", []byte("x"), time.Hour))
		require.NoError(t, kv.Set(ctx, "d2", []byte("x"), time.Hour))
		require.NoError(t, kv.Del(ctx, "d1", "d2", "never-set"))
		_, err := kv.Get(ctx, "d1")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = kv.Get(ctx, "d2")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("GetDelExactlyOnce", func(t *testing.T) {
		kv := newKV(t)
		require.NoError(t, kv.Set(ctx, "once", []byte("token"), time.Hour))
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if v, err := kv.GetDel(ctx, "once"); err == nil && string(v) == "token" {
					wins.Add(1)
				}
			}()
		}
		wg.Wait()
		assert.Equal(t, int32(1), wins.Load())
		_, err := kv.Get(ctx, "once")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("DelPrefixLiteral", func(t *testing.T) {
		kv := newKV(t)
		keys := map[string]bool{ // key -> expected to be deleted
			"chatbot:settings:org1:a%b_c*d!e": true,
			"chatbot:settings:org1:plain":     true,
			"chatbot:settings:org1X:other":    false,
			"chatbot:settings:org2:a":         false,
			"chatbot:settings:org1":           false,
		}
		for k := range keys {
			require.NoError(t, kv.Set(ctx, k, []byte("x"), time.Hour))
		}
		require.NoError(t, kv.DelPrefix(ctx, "chatbot:settings:org1:"))
		for k, deleted := range keys {
			_, err := kv.Get(ctx, k)
			if deleted {
				assert.ErrorIs(t, err, store.ErrNotFound, k)
			} else {
				assert.NoError(t, err, k)
			}
		}

		// A prefix containing LIKE / glob wildcards matches only literally.
		require.NoError(t, kv.Set(ctx, "p:a%:1", []byte("x"), time.Hour))
		require.NoError(t, kv.Set(ctx, "p:ab:1", []byte("x"), time.Hour))
		require.NoError(t, kv.DelPrefix(ctx, "p:a%"))
		_, err := kv.Get(ctx, "p:ab:1")
		assert.NoError(t, err)
		_, err = kv.Get(ctx, "p:a%:1")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("Ping", func(t *testing.T) {
		assert.NoError(t, newKV(t).Ping(ctx))
	})
}

// RunCounter runs the Counter conformance suite.
func RunCounter(t *testing.T, newCounter func(t *testing.T) store.Counter) {
	ctx := context.Background()

	t.Run("CountsWithinWindow", func(t *testing.T) {
		c := newCounter(t)
		for i := int64(1); i <= 3; i++ {
			n, reset, err := c.Incr(ctx, "rl:a", time.Minute)
			require.NoError(t, err)
			assert.Equal(t, i, n)
			assert.Greater(t, reset, 50*time.Second)
			assert.LessOrEqual(t, reset, time.Minute)
		}
	})

	t.Run("NewWindowAfterExpiry", func(t *testing.T) {
		c := newCounter(t)
		_, _, err := c.Incr(ctx, "rl:b", time.Second)
		require.NoError(t, err)
		time.Sleep(1500 * time.Millisecond)
		n, _, err := c.Incr(ctx, "rl:b", time.Second)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})

	t.Run("KeysIndependent", func(t *testing.T) {
		c := newCounter(t)
		_, _, _ = c.Incr(ctx, "rl:c", time.Minute)
		n, _, err := c.Incr(ctx, "rl:d", time.Minute)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})
}

// RunScheduler runs the Scheduler conformance suite.
func RunScheduler(t *testing.T, newScheduler func(t *testing.T) store.Scheduler) {
	ctx := context.Background()
	now := time.Now()

	t.Run("PopDueOnlyDue", func(t *testing.T) {
		s := newScheduler(t)
		require.NoError(t, s.Schedule(ctx, "set", "past", now.Add(-time.Minute)))
		require.NoError(t, s.Schedule(ctx, "set", "future", now.Add(time.Hour)))
		require.NoError(t, s.Schedule(ctx, "other", "past-other", now.Add(-time.Minute)))
		got, err := s.PopDue(ctx, "set", now, 50)
		require.NoError(t, err)
		assert.Equal(t, []string{"past"}, got)
		got, err = s.PopDue(ctx, "set", now, 50)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("ScheduleReplacesDueTime", func(t *testing.T) {
		s := newScheduler(t)
		require.NoError(t, s.Schedule(ctx, "set", "x", now.Add(-time.Minute)))
		require.NoError(t, s.Schedule(ctx, "set", "x", now.Add(time.Hour)))
		got, err := s.PopDue(ctx, "set", now, 50)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("Cancel", func(t *testing.T) {
		s := newScheduler(t)
		require.NoError(t, s.Schedule(ctx, "set", "a", now.Add(-time.Minute)))
		require.NoError(t, s.Schedule(ctx, "set", "b", now.Add(-time.Minute)))
		require.NoError(t, s.Cancel(ctx, "set", "a", "never"))
		got, err := s.PopDue(ctx, "set", now, 50)
		require.NoError(t, err)
		assert.Equal(t, []string{"b"}, got)
	})

	t.Run("LimitAndOrder", func(t *testing.T) {
		s := newScheduler(t)
		for i := range 5 {
			require.NoError(t, s.Schedule(ctx, "set", fmt.Sprintf("i%d", i), now.Add(-time.Duration(10-i)*time.Second)))
		}
		got, err := s.PopDue(ctx, "set", now, 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"i0", "i1"}, got)
	})

	t.Run("ExactlyOnceUnderConcurrency", func(t *testing.T) {
		s := newScheduler(t)
		for i := range 20 {
			require.NoError(t, s.Schedule(ctx, "set", fmt.Sprintf("c%d", i), now.Add(-time.Second)))
		}
		var mu sync.Mutex
		seen := map[string]int{}
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 10 {
					got, err := s.PopDue(ctx, "set", now, 5)
					if err != nil {
						return
					}
					mu.Lock()
					for _, g := range got {
						seen[g]++
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		assert.Len(t, seen, 20)
		for item, n := range seen {
			assert.Equal(t, 1, n, item)
		}
	})
}

// RunBroker runs the Broker conformance suite.
func RunBroker(t *testing.T, newBroker func(t *testing.T) store.Broker) {
	ctx := context.Background()

	t.Run("DeliversToAllSubscribers", func(t *testing.T) {
		b := newBroker(t)
		gotA, gotB := make(chan string, 1), make(chan string, 1)
		stopA, err := b.Subscribe(ctx, "ch", func(p []byte) { gotA <- string(p) })
		require.NoError(t, err)
		defer stopA()
		stopB, err := b.Subscribe(ctx, "ch", func(p []byte) { gotB <- string(p) })
		require.NoError(t, err)
		defer stopB()
		require.NoError(t, b.Publish(ctx, "ch", []byte("hello")))
		for _, ch := range []chan string{gotA, gotB} {
			select {
			case v := <-ch:
				assert.Equal(t, "hello", v)
			case <-time.After(3 * time.Second):
				t.Fatal("message not delivered")
			}
		}
	})

	t.Run("StopEndsDelivery", func(t *testing.T) {
		b := newBroker(t)
		got := make(chan string, 2)
		stop, err := b.Subscribe(ctx, "ch2", func(p []byte) { got <- string(p) })
		require.NoError(t, err)
		stop()
		require.NoError(t, b.Publish(ctx, "ch2", []byte("late")))
		select {
		case v := <-got:
			t.Fatalf("unexpected delivery %q", v)
		case <-time.After(300 * time.Millisecond):
		}
	})

	t.Run("OtherChannelIgnored", func(t *testing.T) {
		b := newBroker(t)
		got := make(chan string, 1)
		stop, err := b.Subscribe(ctx, "ch3", func(p []byte) { got <- string(p) })
		require.NoError(t, err)
		defer stop()
		require.NoError(t, b.Publish(ctx, "elsewhere", []byte("x")))
		select {
		case v := <-got:
			t.Fatalf("unexpected delivery %q", v)
		case <-time.After(300 * time.Millisecond):
		}
	})
}
```

- [ ] **Step 3: Write `internal/store/memory_test.go`**

```go
package store_test

import (
	"testing"

	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
)

func newMemory(t *testing.T) *store.Memory {
	m := store.NewMemory()
	t.Cleanup(m.Close)
	return m
}

func TestMemoryKV(t *testing.T) {
	storetest.RunKV(t, func(t *testing.T) store.KV { return newMemory(t) })
}

func TestMemoryCounter(t *testing.T) {
	storetest.RunCounter(t, func(t *testing.T) store.Counter { return newMemory(t) })
}
```

- [ ] **Step 4: Run to verify it fails**

Run: `go test ./internal/store/...`
Expected: FAIL — `undefined: store.NewMemory`.

- [ ] **Step 5: Implement `internal/store/memory.go`**

```go
package store

import (
	"context"
	"strings"
	"sync"
	"time"
)

type memEntry struct {
	val []byte
	exp time.Time // zero = no expiry
}

type memCounter struct {
	n   int64
	exp time.Time
}

// Memory is an in-process KV and Counter. Expired entries are ignored on read
// and removed by a janitor every minute.
type Memory struct {
	mu       sync.Mutex
	entries  map[string]memEntry
	counters map[string]memCounter
	stop     chan struct{}
	once     sync.Once
}

// NewMemory returns an empty store and starts its janitor; call Close to stop it.
func NewMemory() *Memory {
	m := &Memory{
		entries:  map[string]memEntry{},
		counters: map[string]memCounter{},
		stop:     make(chan struct{}),
	}
	go m.janitor(time.Minute)
	return m
}

// Close stops the janitor.
func (m *Memory) Close() { m.once.Do(func() { close(m.stop) }) }

func (m *Memory) janitor(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			m.mu.Lock()
			for k, e := range m.entries {
				if expired(e.exp, now) {
					delete(m.entries, k)
				}
			}
			for k, c := range m.counters {
				if expired(c.exp, now) {
					delete(m.counters, k)
				}
			}
			m.mu.Unlock()
		}
	}
}

func expired(exp, now time.Time) bool { return !exp.IsZero() && !now.Before(exp) }

func expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || expired(e.exp, time.Now()) {
		return nil, ErrNotFound
	}
	return append([]byte(nil), e.val...), nil
}

func (m *Memory) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	m.mu.Lock()
	m.entries[key] = memEntry{val: append([]byte(nil), val...), exp: expiry(ttl)}
	m.mu.Unlock()
	return nil
}

func (m *Memory) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	for _, k := range keys {
		delete(m.entries, k)
	}
	m.mu.Unlock()
	return nil
}

func (m *Memory) GetDel(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	delete(m.entries, key)
	if !ok || expired(e.exp, time.Now()) {
		return nil, ErrNotFound
	}
	return e.val, nil
}

func (m *Memory) DelPrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	for k := range m.entries {
		if strings.HasPrefix(k, prefix) {
			delete(m.entries, k)
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *Memory) Ping(context.Context) error { return nil }

func (m *Memory) Incr(_ context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.counters[key]
	if !ok || expired(c.exp, now) {
		c = memCounter{exp: now.Add(window)}
	}
	c.n++
	m.counters[key] = c
	return c.n, c.exp.Sub(now), nil
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/store/... -run 'TestMemory' -v`
Expected: PASS (all subtests).

- [ ] **Step 7: Commit**

```bash
git add internal/store
git commit -m "feat(store): add store interfaces, memory store and conformance tests"
```

---

### Task 2: Database models for the store and queue

**Files:**
- Create: `internal/models/store.go`
- Modify: `internal/database/database.go` (`GetMigrationModels`), `test/testutil/db.go` (`migrateModels`, `cleanupTables`, `TruncateTables`)
- Test: `internal/models/store_test.go`

**Interfaces:**
- Produces: `models.KVEntry{Key string; Value []byte; ExpiresAt *time.Time}` (table `kv_entries`), `models.ScheduledItem{SetName, Item string; DueAt time.Time}` (table `scheduled_items`), `models.CampaignJob{ID int64; JobType, Payload, LockedBy string; LockedUntil *time.Time; Attempts int; CreatedAt time.Time}` (table `campaign_jobs`).

- [ ] **Step 1: Write the failing test `internal/models/store_test.go`**

```go
package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestStoreModels_RoundTrip(t *testing.T) {
	db := testutil.SetupTestDB(t)
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	kv := models.KVEntry{Key: "rt:" + testutil.RandomUUID().String(), Value: []byte{0, 1, 2, 255}, ExpiresAt: &exp}
	require.NoError(t, db.Create(&kv).Error)
	var gotKV models.KVEntry
	require.NoError(t, db.Where("kv_key = ?", kv.Key).Take(&gotKV).Error)
	assert.Equal(t, kv.Value, gotKV.Value)
	assert.True(t, gotKV.ExpiresAt.Equal(exp))

	item := models.ScheduledItem{SetName: "rt", Item: testutil.RandomUUID().String(), DueAt: exp}
	require.NoError(t, db.Create(&item).Error)

	job := models.CampaignJob{JobType: "recipient", Payload: `{"a":1}`}
	require.NoError(t, db.Create(&job).Error)
	assert.NotZero(t, job.ID)

	db.Where("kv_key = ?", kv.Key).Delete(&models.KVEntry{})
	db.Where("set_name = ? AND item = ?", item.SetName, item.Item).Delete(&models.ScheduledItem{})
	db.Delete(&job)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/models/ -run TestStoreModels -v`
Expected: FAIL — `undefined: models.KVEntry`.

- [ ] **Step 3: Implement `internal/models/store.go`**

```go
package models

import "time"

// KVEntry is a durable key/value row used by the embedded store.
type KVEntry struct {
	Key       string     `gorm:"column:kv_key;primaryKey;size:255"`
	Value     []byte     `gorm:"column:kv_value"`
	ExpiresAt *time.Time `gorm:"index"`
}

func (KVEntry) TableName() string { return "kv_entries" }

// ScheduledItem is an item due at a time (chatbot delays, agent session timeouts).
type ScheduledItem struct {
	SetName string    `gorm:"primaryKey;size:64"`
	Item    string    `gorm:"primaryKey;size:255"`
	DueAt   time.Time `gorm:"index;not null"`
}

func (ScheduledItem) TableName() string { return "scheduled_items" }

// CampaignJob is a queued campaign job in the database queue.
type CampaignJob struct {
	ID          int64      `gorm:"primaryKey;autoIncrement"`
	JobType     string     `gorm:"size:32;not null"`
	Payload     string     `gorm:"type:text;not null"`
	LockedBy    string     `gorm:"size:255"`
	LockedUntil *time.Time `gorm:"index"`
	Attempts    int        `gorm:"not null;default:0"`
	CreatedAt   time.Time
}

func (CampaignJob) TableName() string { return "campaign_jobs" }
```

- [ ] **Step 4: Register the models**

In `internal/database/database.go` `GetMigrationModels`, append at the end of the returned slice:

```go
		// Embedded store (used when no Redis server is configured)
		{"KVEntry", &models.KVEntry{}},
		{"ScheduledItem", &models.ScheduledItem{}},
		{"CampaignJob", &models.CampaignJob{}},
```

In `test/testutil/db.go` `migrateModels`, append after `&models.SharedMetaAppCredentials{},`:

```go
		// Embedded store
		&models.KVEntry{},
		&models.ScheduledItem{},
		&models.CampaignJob{},
```

In both `cleanupTables` and `TruncateTables`, add these names at the top of the `tables` slice:

```go
		"kv_entries",
		"scheduled_items",
		"campaign_jobs",
```

- [ ] **Step 5: Run tests on SQLite and PostgreSQL**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/models/ -run TestStoreModels -v`
Run: `TEST_DATABASE_URL=postgres://...@localhost:5433/whatomate_test?sslmode=disable go test ./internal/models/ -run TestStoreModels -v` (use the DSN from the existing test setup)
Expected: PASS on both.

- [ ] **Step 6: Commit**

```bash
git add internal/models/store.go internal/models/store_test.go internal/database/database.go test/testutil/db.go
git commit -m "feat(models): add kv_entries, scheduled_items and campaign_jobs tables"
```

---

### Task 3: Database KV

**Files:**
- Create: `internal/store/sqlkv.go`, `internal/store/sqlkv_test.go`

**Interfaces:**
- Consumes: `models.KVEntry`, `store.KV`, `storetest.RunKV`
- Produces: `store.NewSQLKV(db *gorm.DB) *SQLKV` (implements `KV`; `Close()` stops its janitor); `store.escapeLike(s string) string` (package-internal, reused by Task 4)

- [ ] **Step 1: Write the failing test `internal/store/sqlkv_test.go`**

```go
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

func cleanDB(t *testing.T) *gorm.DB {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.Exec("DELETE FROM kv_entries").Error)
	require.NoError(t, db.Exec("DELETE FROM scheduled_items").Error)
	require.NoError(t, db.Exec("DELETE FROM campaign_jobs").Error)
	return db
}

func newSQLKV(t *testing.T) *store.SQLKV {
	kv := store.NewSQLKV(cleanDB(t))
	t.Cleanup(kv.Close)
	return kv
}

func TestSQLKV(t *testing.T) {
	storetest.RunKV(t, func(t *testing.T) store.KV { return newSQLKV(t) })
}

func TestSQLKV_SurvivesNewInstance(t *testing.T) {
	db := cleanDB(t)
	ctx := context.Background()
	first := store.NewSQLKV(db)
	require.NoError(t, first.Set(ctx, "refresh:jti-1", []byte("user-1"), time.Hour))
	first.Close()

	second := store.NewSQLKV(db) // simulates a restart
	defer second.Close()
	v, err := second.GetDel(ctx, "refresh:jti-1")
	require.NoError(t, err)
	assert.Equal(t, "user-1", string(v))
}

func TestSQLKV_PurgeExpired(t *testing.T) {
	db := cleanDB(t)
	ctx := context.Background()
	kv := store.NewSQLKV(db)
	defer kv.Close()
	require.NoError(t, kv.Set(ctx, "gone", []byte("x"), time.Millisecond))
	require.NoError(t, kv.Set(ctx, "kept", []byte("x"), 0))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, kv.PurgeExpired(ctx))
	var n int64
	db.Model(&models.KVEntry{}).Count(&n)
	assert.Equal(t, int64(1), n)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/store/ -run TestSQLKV -v`
Expected: FAIL — `undefined: store.NewSQLKV`.

- [ ] **Step 3: Implement `internal/store/sqlkv.go`**

```go
package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SQLKV is a KV stored in the kv_entries table of the main database.
type SQLKV struct {
	db   *gorm.DB
	stop chan struct{}
	once sync.Once
}

// NewSQLKV returns a database-backed KV and starts a janitor that deletes
// expired rows every 10 minutes; call Close to stop it.
func NewSQLKV(db *gorm.DB) *SQLKV {
	s := &SQLKV{db: db, stop: make(chan struct{})}
	go s.janitor(10 * time.Minute)
	return s
}

// Close stops the janitor.
func (s *SQLKV) Close() { s.once.Do(func() { close(s.stop) }) }

func (s *SQLKV) janitor(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			_ = s.PurgeExpired(ctx)
			cancel()
		}
	}
}

// PurgeExpired deletes expired rows.
func (s *SQLKV) PurgeExpired(ctx context.Context) error {
	return s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now()).
		Delete(&models.KVEntry{}).Error
}

func (s *SQLKV) live(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Where("expires_at IS NULL OR expires_at > ?", time.Now())
}

func (s *SQLKV) Get(ctx context.Context, key string) ([]byte, error) {
	var row models.KVEntry
	err := s.live(ctx).Where("kv_key = ?", key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row.Value, nil
}

func (s *SQLKV) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	row := models.KVEntry{Key: key, Value: val}
	if ttl > 0 {
		exp := time.Now().Add(ttl)
		row.ExpiresAt = &exp
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kv_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"kv_value", "expires_at"}),
	}).Create(&row).Error
}

func (s *SQLKV) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("kv_key IN ?", keys).Delete(&models.KVEntry{}).Error
}

func (s *SQLKV) GetDel(ctx context.Context, key string) ([]byte, error) {
	val, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	res := s.db.WithContext(ctx).Where("kv_key = ?", key).Delete(&models.KVEntry{})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrNotFound // another caller took it first
	}
	return val, nil
}

func (s *SQLKV) DelPrefix(ctx context.Context, prefix string) error {
	return s.db.WithContext(ctx).
		Where("kv_key LIKE ? ESCAPE '!'", escapeLike(prefix)+"%").
		Delete(&models.KVEntry{}).Error
}

func (s *SQLKV) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// escapeLike escapes LIKE wildcards using '!' as the escape character.
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}
```

- [ ] **Step 4: Run tests on every available database**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/store/ -run TestSQLKV -v`
Run: `TEST_DATABASE_URL=<postgres whatomate_test DSN> go test ./internal/store/ -run TestSQLKV -v`
Run (if MySQL test DSN available): `TEST_DB_DRIVER=mysql TEST_MYSQL_DSN=<.../whatomate_test> go test ./internal/store/ -run TestSQLKV -v`
Expected: PASS on each.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlkv.go internal/store/sqlkv_test.go
git commit -m "feat(store): add database-backed KV"
```

---

### Task 4: Database scheduler and in-process broker

**Files:**
- Create: `internal/store/sqlscheduler.go`, `internal/store/broker.go`, `internal/store/sqlscheduler_test.go`, `internal/store/broker_test.go`

**Interfaces:**
- Consumes: `models.ScheduledItem`, `storetest.RunScheduler`, `storetest.RunBroker`, `cleanDB` (Task 3 test helper)
- Produces: `store.NewSQLScheduler(db *gorm.DB) *SQLScheduler`, `store.NewMemoryBroker() *MemoryBroker`

- [ ] **Step 1: Write the failing tests**

`internal/store/sqlscheduler_test.go`:

```go
package store_test

import (
	"testing"

	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
)

func TestSQLScheduler(t *testing.T) {
	storetest.RunScheduler(t, func(t *testing.T) store.Scheduler { return store.NewSQLScheduler(cleanDB(t)) })
}
```

`internal/store/broker_test.go`:

```go
package store_test

import (
	"testing"

	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
)

func TestMemoryBroker(t *testing.T) {
	storetest.RunBroker(t, func(t *testing.T) store.Broker { return store.NewMemoryBroker() })
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/store/ -run 'TestSQLScheduler|TestMemoryBroker' -v`
Expected: FAIL — undefined constructors.

- [ ] **Step 3: Implement `internal/store/sqlscheduler.go`**

```go
package store

import (
	"context"
	"time"

	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SQLScheduler is a Scheduler stored in the scheduled_items table.
type SQLScheduler struct{ db *gorm.DB }

func NewSQLScheduler(db *gorm.DB) *SQLScheduler { return &SQLScheduler{db: db} }

func (s *SQLScheduler) Schedule(ctx context.Context, set, item string, at time.Time) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "set_name"}, {Name: "item"}},
		DoUpdates: clause.AssignmentColumns([]string{"due_at"}),
	}).Create(&models.ScheduledItem{SetName: set, Item: item, DueAt: at}).Error
}

func (s *SQLScheduler) Cancel(ctx context.Context, set string, items ...string) error {
	if len(items) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("set_name = ? AND item IN ?", set, items).
		Delete(&models.ScheduledItem{}).Error
}

func (s *SQLScheduler) PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error) {
	var due []models.ScheduledItem
	if err := s.db.WithContext(ctx).
		Where("set_name = ? AND due_at <= ?", set, now).
		Order("due_at, item").Limit(limit).
		Find(&due).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(due))
	for _, d := range due {
		// Delete first so concurrent pollers don't both take the item; the
		// due_at condition skips items rescheduled into the future meanwhile.
		res := s.db.WithContext(ctx).
			Where("set_name = ? AND item = ? AND due_at <= ?", set, d.Item, now).
			Delete(&models.ScheduledItem{})
		if res.Error != nil {
			return out, res.Error
		}
		if res.RowsAffected == 1 {
			out = append(out, d.Item)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Implement `internal/store/broker.go`**

```go
package store

import (
	"context"
	"sync"
)

// MemoryBroker delivers messages to subscribers in the same process.
type MemoryBroker struct {
	mu   sync.RWMutex
	next int
	subs map[string]map[int]func([]byte)
}

func NewMemoryBroker() *MemoryBroker {
	return &MemoryBroker{subs: map[string]map[int]func([]byte){}}
}

func (b *MemoryBroker) Publish(_ context.Context, channel string, payload []byte) error {
	b.mu.RLock()
	fns := make([]func([]byte), 0, len(b.subs[channel]))
	for _, fn := range b.subs[channel] {
		fns = append(fns, fn)
	}
	b.mu.RUnlock()
	for _, fn := range fns {
		fn(append([]byte(nil), payload...))
	}
	return nil
}

func (b *MemoryBroker) Subscribe(ctx context.Context, channel string, fn func([]byte)) (func(), error) {
	b.mu.Lock()
	id := b.next
	b.next++
	if b.subs[channel] == nil {
		b.subs[channel] = map[int]func([]byte){}
	}
	b.subs[channel][id] = fn
	b.mu.Unlock()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs[channel], id)
			b.mu.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		stop()
	}()
	return stop, nil
}
```

- [ ] **Step 5: Run tests**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/store/ -v`
Run: `TEST_DATABASE_URL=<postgres whatomate_test DSN> go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/sqlscheduler.go internal/store/broker.go internal/store/sqlscheduler_test.go internal/store/broker_test.go
git commit -m "feat(store): add database scheduler and in-process broker"
```

---

### Task 5: Redis implementations

**Files:**
- Create: `internal/store/redis.go`, `internal/store/redis_test.go`

**Interfaces:**
- Consumes: go-redis v9 `*redis.Client`, the four interfaces, `storetest.Run*`, `testutil.SetupTestRedis`
- Produces: `store.NewRedis(c *redis.Client) *Redis` implementing `KV`, `Counter`, `Scheduler`, `Broker`

- [ ] **Step 1: Write the failing test `internal/store/redis_test.go`**

```go
package store_test

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
	"github.com/trysoft/whatomate/test/testutil"
)

func newRedisStore(t *testing.T) *store.Redis {
	c := testutil.SetupTestRedis(t)
	if c == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	flush(t, c)
	return store.NewRedis(c)
}

func flush(t *testing.T, c *redis.Client) {
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush: %v", err)
	}
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `TEST_REDIS_URL=redis://localhost:6379/15 go test ./internal/store/ -run TestRedis -v`
Expected: FAIL — `undefined: store.NewRedis` (or SKIP everywhere without Redis; then rely on compile failure).

- [ ] **Step 3: Implement `internal/store/redis.go`**

```go
package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis implements every store interface on a Redis server, using the same
// key names whatomate used before the store package existed.
type Redis struct{ c *redis.Client }

func NewRedis(c *redis.Client) *Redis { return &Redis{c: c} }

func notFound(err error) error {
	if errors.Is(err, redis.Nil) {
		return ErrNotFound
	}
	return err
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := r.c.Get(ctx, key).Bytes()
	return v, notFound(err)
}

func (r *Redis) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return r.c.Set(ctx, key, val, ttl).Err()
}

func (r *Redis) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return r.c.Del(ctx, keys...).Err()
}

func (r *Redis) GetDel(ctx context.Context, key string) ([]byte, error) {
	v, err := r.c.GetDel(ctx, key).Bytes()
	return v, notFound(err)
}

func (r *Redis) DelPrefix(ctx context.Context, prefix string) error {
	iter := r.c.Scan(ctx, 0, globEscape(prefix)+"*", 100).Iterator()
	for iter.Next(ctx) {
		if err := r.c.Del(ctx, iter.Val()).Err(); err != nil {
			return err
		}
	}
	return iter.Err()
}

func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

// globEscape escapes Redis MATCH pattern metacharacters.
func globEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`).Replace(s)
}

func (r *Redis) Incr(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	n, err := r.c.Incr(ctx, key).Result()
	if err != nil {
		return 0, 0, err
	}
	if n == 1 {
		if err := r.c.Expire(ctx, key, window).Err(); err != nil {
			return n, window, err
		}
		return n, window, nil
	}
	ttl, err := r.c.TTL(ctx, key).Result()
	if err != nil || ttl < 0 {
		ttl = window
	}
	return n, ttl, nil
}

func (r *Redis) Schedule(ctx context.Context, set, item string, at time.Time) error {
	return r.c.ZAdd(ctx, set, redis.Z{Score: float64(at.Unix()), Member: item}).Err()
}

func (r *Redis) Cancel(ctx context.Context, set string, items ...string) error {
	if len(items) == 0 {
		return nil
	}
	members := make([]any, len(items))
	for i, it := range items {
		members[i] = it
	}
	return r.c.ZRem(ctx, set, members...).Err()
}

func (r *Redis) PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error) {
	ids, err := r.c.ZRangeByScore(ctx, set, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now.Unix(), 10), Count: int64(limit),
	}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		removed, err := r.c.ZRem(ctx, set, id).Result()
		if err != nil {
			return out, err
		}
		if removed == 1 {
			out = append(out, id)
		}
	}
	return out, nil
}

func (r *Redis) Publish(ctx context.Context, channel string, payload []byte) error {
	return r.c.Publish(ctx, channel, payload).Err()
}

func (r *Redis) Subscribe(ctx context.Context, channel string, fn func([]byte)) (func(), error) {
	ps := r.c.Subscribe(ctx, channel)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, err
	}
	ch := ps.Channel()
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done); _ = ps.Close() }) }
	go func() {
		for {
			select {
			case <-ctx.Done():
				stop()
				return
			case <-done:
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				fn([]byte(msg.Payload))
			}
		}
	}()
	return stop, nil
}
```

Note: Redis sorted-set scores are whole seconds (as before), so `RunScheduler`'s `LimitAndOrder` uses due times 1 s apart.

- [ ] **Step 4: Run tests**

Run: `TEST_REDIS_URL=redis://localhost:6379/15 go test ./internal/store/ -run TestRedis -v`
Expected: PASS (requires a local Redis; if none, run `go vet ./internal/store/` and note the skip in the handoff).

- [ ] **Step 5: Commit**

```bash
git add internal/store/redis.go internal/store/redis_test.go
git commit -m "feat(store): add Redis implementations of the store interfaces"
```

---

### Task 6: Database campaign queue and Broker-based pub/sub

**Files:**
- Create: `internal/queue/dbqueue.go`, `internal/queue/dbqueue_test.go`
- Modify: `internal/queue/pubsub.go`, `internal/queue/pubsub_test.go`, `internal/queue/queue_test.go` (publisher/subscriber constructors only)

**Interfaces:**
- Consumes: `models.CampaignJob`, `store.Broker`, existing `queue.Queue`, `queue.Consumer`, `queue.JobHandler`, `queue.RecipientJob`, `JobTypeRecipient`, `ClaimMinIdleTime`
- Produces:
  - `queue.NewDBQueue(db *gorm.DB, log logf.Logger) *DBQueue` (implements `Queue`)
  - `(*DBQueue).NewConsumer() *DBConsumer` (implements `Consumer`)
  - `queue.MaxJobAttempts = 5`, `queue.PollInterval = time.Second`
  - `queue.NewPublisher(b store.Broker, log logf.Logger) *Publisher`, `queue.NewSubscriber(b store.Broker, log logf.Logger) *Subscriber` (same methods as today)

- [ ] **Step 1: Write the failing test `internal/queue/dbqueue_test.go`**

```go
package queue_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/queue"
	"github.com/trysoft/whatomate/test/testutil"
	"gorm.io/gorm"
)

type funcHandler func(ctx context.Context, job *queue.RecipientJob) error

func (f funcHandler) HandleRecipientJob(ctx context.Context, job *queue.RecipientJob) error {
	return f(ctx, job)
}

func dbQueue(t *testing.T) (*queue.DBQueue, *gorm.DB) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.Exec("DELETE FROM campaign_jobs").Error)
	return queue.NewDBQueue(db, testutil.NopLogger()), db
}

func jobs(n int) []*queue.RecipientJob {
	out := make([]*queue.RecipientJob, n)
	for i := range out {
		out[i] = &queue.RecipientJob{CampaignID: uuid.New(), RecipientID: uuid.New(), PhoneNumber: "1"}
	}
	return out
}

func TestDBQueue_EachJobHandledOnceAcrossConsumers(t *testing.T) {
	q, db := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	h := funcHandler(func(_ context.Context, j *queue.RecipientJob) error {
		mu.Lock()
		seen[j.RecipientID]++
		mu.Unlock()
		return nil
	})
	for range 3 {
		go func() { _ = q.NewConsumer().Consume(ctx, h) }()
	}

	in := jobs(30)
	require.NoError(t, q.EnqueueRecipients(ctx, in))
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 30
	}, 10*time.Second, "all jobs handled")

	for _, j := range in {
		assert.Equal(t, 1, seen[j.RecipientID])
	}
	testutil.AssertEventually(t, func() bool {
		var n int64
		db.Model(&models.CampaignJob{}).Count(&n)
		return n == 0
	}, 5*time.Second, "acked jobs deleted")
}

func TestDBQueue_EnqueueWakesConsumerImmediately(t *testing.T) {
	q, _ := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan struct{}, 1)
	go func() {
		_ = q.NewConsumer().Consume(ctx, funcHandler(func(context.Context, *queue.RecipientJob) error {
			got <- struct{}{}
			return nil
		}))
	}()
	time.Sleep(200 * time.Millisecond) // consumer is now idle
	start := time.Now()
	require.NoError(t, q.EnqueueRecipient(ctx, jobs(1)[0]))
	select {
	case <-got:
		assert.Less(t, time.Since(start), queue.PollInterval)
	case <-time.After(5 * time.Second):
		t.Fatal("job not handled")
	}
}

func TestDBConsumer_FailedJobRetriedAfterLockExpiry(t *testing.T) {
	q, db := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, q.EnqueueRecipient(ctx, jobs(1)[0]))

	var calls atomic.Int32
	h := funcHandler(func(context.Context, *queue.RecipientJob) error {
		if calls.Add(1) == 1 {
			return errors.New("transient")
		}
		return nil
	})
	go func() { _ = q.NewConsumer().Consume(ctx, h) }()
	testutil.AssertEventually(t, func() bool { return calls.Load() == 1 }, 5*time.Second, "first attempt")

	// Simulate the lock running out instead of waiting ClaimMinIdleTime.
	require.NoError(t, db.Model(&models.CampaignJob{}).Where("1 = 1").
		Update("locked_until", time.Now().Add(-time.Second)).Error)
	testutil.AssertEventually(t, func() bool { return calls.Load() == 2 }, 5*time.Second, "retried")
}

func TestDBConsumer_GivesUpAfterMaxAttempts(t *testing.T) {
	q, db := dbQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, q.EnqueueRecipient(ctx, jobs(1)[0]))
	require.NoError(t, db.Model(&models.CampaignJob{}).Where("1 = 1").
		Update("attempts", queue.MaxJobAttempts).Error)

	var calls atomic.Int32
	go func() {
		_ = q.NewConsumer().Consume(ctx, funcHandler(func(context.Context, *queue.RecipientJob) error {
			calls.Add(1)
			return errors.New("always fails")
		}))
	}()
	testutil.AssertEventually(t, func() bool {
		var n int64
		db.Model(&models.CampaignJob{}).Count(&n)
		return n == 0
	}, 5*time.Second, "job dropped")
	assert.Equal(t, int32(0), calls.Load())
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/queue/ -run 'TestDB' -v`
Expected: FAIL — `undefined: queue.NewDBQueue`.

- [ ] **Step 3: Implement `internal/queue/dbqueue.go`**

```go
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

const (
	// PollInterval is how often an idle consumer checks the database for jobs
	// enqueued by another process.
	PollInterval = time.Second
	// MaxJobAttempts is how many times a job is tried before it is dropped.
	MaxJobAttempts = 5
)

// wakeup lets Enqueue wake every idle consumer in this process.
type wakeup struct {
	mu sync.Mutex
	ch chan struct{}
}

func (w *wakeup) wait() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

func (w *wakeup) notify() {
	w.mu.Lock()
	close(w.ch)
	w.ch = make(chan struct{})
	w.mu.Unlock()
}

// DBQueue is a campaign job queue stored in the campaign_jobs table.
type DBQueue struct {
	db   *gorm.DB
	log  logf.Logger
	wake *wakeup
}

func NewDBQueue(db *gorm.DB, log logf.Logger) *DBQueue {
	return &DBQueue{db: db, log: log, wake: &wakeup{ch: make(chan struct{})}}
}

func (q *DBQueue) EnqueueRecipient(ctx context.Context, job *RecipientJob) error {
	return q.EnqueueRecipients(ctx, []*RecipientJob{job})
}

func (q *DBQueue) EnqueueRecipients(ctx context.Context, jobs []*RecipientJob) error {
	if len(jobs) == 0 {
		return nil
	}
	now := time.Now()
	rows := make([]models.CampaignJob, len(jobs))
	for i, job := range jobs {
		if job.EnqueuedAt.IsZero() {
			job.EnqueuedAt = now
		}
		payload, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("failed to marshal recipient job: %w", err)
		}
		rows[i] = models.CampaignJob{JobType: string(JobTypeRecipient), Payload: string(payload)}
	}
	if err := q.db.WithContext(ctx).CreateInBatches(rows, 500).Error; err != nil {
		return fmt.Errorf("failed to enqueue recipient jobs: %w", err)
	}
	q.wake.notify()
	q.log.Info("Recipient jobs enqueued", "count", len(jobs), "campaign_id", jobs[0].CampaignID)
	return nil
}

func (q *DBQueue) Close() error { return nil }

// NewConsumer returns a consumer that is woken by this queue's Enqueue calls.
func (q *DBQueue) NewConsumer() *DBConsumer {
	host, _ := os.Hostname()
	return &DBConsumer{q: q, id: fmt.Sprintf("worker-%s-%d-%s", host, os.Getpid(), uuid.NewString()[:8])}
}

// DBConsumer claims jobs from campaign_jobs. A job whose handler fails stays
// locked for ClaimMinIdleTime and is then retried, up to MaxJobAttempts.
type DBConsumer struct {
	q  *DBQueue
	id string
}

func (c *DBConsumer) Consume(ctx context.Context, handler JobHandler) error {
	c.q.log.Info("Starting to consume jobs", "consumer_id", c.id)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wake := c.q.wake.wait()
		job, err := c.claim(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.q.log.Error("Failed to claim job", "error", err)
			sleep(ctx, time.Second)
			continue
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
			case <-time.After(PollInterval):
			}
			continue
		}
		c.process(ctx, job, handler)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// claim locks the oldest available job for this consumer, or returns nil.
func (c *DBConsumer) claim(ctx context.Context) (*models.CampaignJob, error) {
	now := time.Now()
	var cands []models.CampaignJob
	if err := c.q.db.WithContext(ctx).
		Where("locked_until IS NULL OR locked_until < ?", now).
		Order("id").Limit(10).Find(&cands).Error; err != nil {
		return nil, err
	}
	for i := range cands {
		res := c.q.db.WithContext(ctx).Model(&models.CampaignJob{}).
			Where("id = ? AND (locked_until IS NULL OR locked_until < ?)", cands[i].ID, now).
			Updates(map[string]any{
				"locked_by":    c.id,
				"locked_until": now.Add(ClaimMinIdleTime),
				"attempts":     gorm.Expr("attempts + 1"),
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 1 {
			cands[i].Attempts++
			return &cands[i], nil
		}
	}
	return nil, nil
}

func (c *DBConsumer) process(ctx context.Context, job *models.CampaignJob, handler JobHandler) {
	if job.Attempts > MaxJobAttempts {
		c.q.log.Error("Dropping campaign job after too many attempts", "job_id", job.ID, "attempts", job.Attempts-1)
		c.ack(ctx, job.ID)
		return
	}
	if JobType(job.JobType) != JobTypeRecipient {
		c.q.log.Error("Dropping job with unknown type", "job_id", job.ID, "type", job.JobType)
		c.ack(ctx, job.ID)
		return
	}
	var rj RecipientJob
	if err := json.Unmarshal([]byte(job.Payload), &rj); err != nil {
		c.q.log.Error("Dropping job with invalid payload", "job_id", job.ID, "error", err)
		c.ack(ctx, job.ID)
		return
	}
	if err := handler.HandleRecipientJob(ctx, &rj); err != nil {
		c.q.log.Error("Failed to process job; will retry", "job_id", job.ID, "attempt", job.Attempts, "error", err)
		return
	}
	c.ack(ctx, job.ID)
}

func (c *DBConsumer) ack(ctx context.Context, id int64) {
	if err := c.q.db.WithContext(ctx).Delete(&models.CampaignJob{}, id).Error; err != nil {
		c.q.log.Error("Failed to delete finished job", "job_id", id, "error", err)
	}
}

func (c *DBConsumer) Close() error { return nil }
```

- [ ] **Step 4: Move pub/sub onto `store.Broker`**

Replace `internal/queue/pubsub.go` from `// Publisher publishes messages to Redis pub/sub channels` to the end of the file with:

```go
// Publisher publishes campaign stats updates.
type Publisher struct {
	broker store.Broker
	log    logf.Logger
}

func NewPublisher(b store.Broker, log logf.Logger) *Publisher {
	return &Publisher{broker: b, log: log}
}

// PublishCampaignStats publishes a campaign stats update
func (p *Publisher) PublishCampaignStats(ctx context.Context, update *CampaignStatsUpdate) error {
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	if err := p.broker.Publish(ctx, CampaignStatsChannel, payload); err != nil {
		p.log.Error("Failed to publish campaign stats", "error", err, "campaign_id", update.CampaignID)
		return err
	}
	p.log.Debug("Published campaign stats update", "campaign_id", update.CampaignID, "status", update.Status)
	return nil
}

// Subscriber receives campaign stats updates.
type Subscriber struct {
	broker store.Broker
	log    logf.Logger
	stop   func()
}

func NewSubscriber(b store.Broker, log logf.Logger) *Subscriber {
	return &Subscriber{broker: b, log: log}
}

// SubscribeCampaignStats calls handler for each update until ctx is done or Close is called.
func (s *Subscriber) SubscribeCampaignStats(ctx context.Context, handler func(update *CampaignStatsUpdate)) error {
	stop, err := s.broker.Subscribe(ctx, CampaignStatsChannel, func(payload []byte) {
		var update CampaignStatsUpdate
		if err := json.Unmarshal(payload, &update); err != nil {
			s.log.Error("Failed to unmarshal campaign stats update", "error", err)
			return
		}
		handler(&update)
	})
	if err != nil {
		return err
	}
	s.stop = stop
	s.log.Info("Subscribed to campaign stats channel")
	return nil
}

// Close stops the subscription.
func (s *Subscriber) Close() error {
	if s.stop != nil {
		s.stop()
	}
	return nil
}
```

Update the imports of `pubsub.go`: remove `github.com/redis/go-redis/v9`, add `github.com/trysoft/whatomate/internal/store`. Update the `CampaignStatsChannel` comment to "pub/sub channel for campaign stats updates".

In `internal/queue/pubsub_test.go` and `internal/queue/queue_test.go`, every `queue.NewPublisher(X, log)` / `queue.NewSubscriber(X, log)` where `X` is a `*redis.Client` becomes `queue.NewPublisher(store.NewRedis(X), log)` / `queue.NewSubscriber(store.NewRedis(X), log)` (add the `store` import). Then add a Redis-free test to `pubsub_test.go`:

```go
func TestPubSub_MemoryBroker(t *testing.T) {
	b := store.NewMemoryBroker()
	pub := queue.NewPublisher(b, testutil.NopLogger())
	sub := queue.NewSubscriber(b, testutil.NopLogger())
	got := make(chan *queue.CampaignStatsUpdate, 1)
	require.NoError(t, sub.SubscribeCampaignStats(context.Background(), func(u *queue.CampaignStatsUpdate) { got <- u }))
	defer sub.Close()
	require.NoError(t, pub.PublishCampaignStats(context.Background(), &queue.CampaignStatsUpdate{CampaignID: "c1", SentCount: 3}))
	select {
	case u := <-got:
		assert.Equal(t, "c1", u.CampaignID)
		assert.Equal(t, 3, u.SentCount)
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
	}
}
```

In `queue_test.go`, the test at line ~527 that builds a publisher on a bad client (`queue.NewPublisher(badClient, log)`) becomes `queue.NewPublisher(store.NewRedis(badClient), log)` and keeps asserting an error.

- [ ] **Step 5: Run tests**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/queue/ -v`
Run: `TEST_DATABASE_URL=<postgres whatomate_test DSN> go test ./internal/queue/ -run 'TestDB|TestPubSub_MemoryBroker' -v`
Expected: PASS (Redis-only tests SKIP without `TEST_REDIS_URL`).

`go build ./...` will fail in `internal/worker` and `internal/handlers` (they still pass `*redis.Client`); Task 8 and Task 9 fix those. Run `go vet ./internal/queue/ ./internal/store/` here instead.

- [ ] **Step 6: Commit**

```bash
git add internal/queue
git commit -m "feat(queue): add database campaign queue; pub/sub on store.Broker"
```

---

### Task 7: `store.New` and mode selection

**Files:**
- Modify: `internal/store/store.go`
- Create: `internal/store/new_test.go`

**Interfaces:**
- Consumes: `config.RedisConfig`, `database.NewRedis`, `NewMemory`, `NewSQLKV`, `NewSQLScheduler`, `NewMemoryBroker`, `NewRedis`
- Produces:
  - `type Stores struct { Cache, State KV; Counter Counter; Scheduler Scheduler; Broker Broker; Redis *redis.Client }`
  - `store.New(cfg *config.RedisConfig, db *gorm.DB) (*Stores, error)`
  - `(*Stores).Embedded() bool`, `(*Stores).Close()`

- [ ] **Step 1: Write the failing test `internal/store/new_test.go`**

```go
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
	assert.IsType(t, &store.Redis{}, s.Cache)
}

func TestNew_RedisUnreachableFails(t *testing.T) {
	_, err := store.New(&config.RedisConfig{Host: "127.0.0.1", Port: 1}, cleanDB(t))
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/store/ -run TestNew -v`
Expected: FAIL — `undefined: store.New`.

- [ ] **Step 3: Append to `internal/store/store.go`**

```go
// Stores bundles the backends the app uses.
type Stores struct {
	Cache     KV // short-lived caches; memory in embedded mode
	State     KV // logins, OAuth/passkey state, flow state, sticky calls; database in embedded mode
	Counter   Counter
	Scheduler Scheduler
	Broker    Broker
	// Redis is the client in Redis mode and nil in embedded mode.
	Redis *redis.Client

	closers []func()
}

// New returns embedded stores when cfg.Host is empty, otherwise Redis stores.
func New(cfg *config.RedisConfig, db *gorm.DB) (*Stores, error) {
	if cfg.Host == "" {
		mem := NewMemory()
		state := NewSQLKV(db)
		return &Stores{
			Cache:     mem,
			State:     state,
			Counter:   mem,
			Scheduler: NewSQLScheduler(db),
			Broker:    NewMemoryBroker(),
			closers:   []func(){mem.Close, state.Close},
		}, nil
	}
	client, err := database.NewRedis(cfg)
	if err != nil {
		return nil, err
	}
	r := NewRedis(client)
	return &Stores{
		Cache: r, State: r, Counter: r, Scheduler: r, Broker: r,
		Redis:   client,
		closers: []func(){func() { _ = client.Close() }},
	}, nil
}

// Embedded reports whether no Redis server is used.
func (s *Stores) Embedded() bool { return s.Redis == nil }

// Close stops background janitors and closes the Redis client.
func (s *Stores) Close() {
	for _, c := range s.closers {
		c()
	}
}
```

Add imports to `store.go`: `github.com/redis/go-redis/v9`, `github.com/trysoft/whatomate/internal/config`, `github.com/trysoft/whatomate/internal/database`, `gorm.io/gorm`.

- [ ] **Step 4: Run tests**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/store/ -v`
Expected: PASS (`TestNew_RedisWhenHostSet` SKIPs without Redis).

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/new_test.go
git commit -m "feat(store): choose embedded or Redis stores from [redis] host"
```

---

### Task 8: Calling, assignment, rate limiting and worker on the store interfaces

**Files:**
- Modify: `internal/calling/session.go:179,193,217`, `internal/calling/cache.go`, `internal/assignment/assigner.go:23,28,170-175`, `internal/assignment/cache.go`, `internal/assignment/assigner_test.go:29`, `internal/middleware/ratelimit.go`, `internal/middleware/ratelimit_test.go`, `internal/worker/worker.go:20-52`, `internal/worker/worker_test.go:33-37`

**Interfaces:**
- Consumes: `store.KV`, `store.Counter`, `store.Broker`, `store.NewMemory`, `store.NewMemoryBroker`, `queue.Consumer`, `queue.NewPublisher(store.Broker, log)`
- Produces:
  - `calling.NewManager(cfg *config.CallingConfig, recordings storage.Uploader, db *gorm.DB, cache store.KV, waClient *whatsapp.Client, wsHub *websocket.Hub, assigner *assignment.Assigner, log logf.Logger) *Manager`
  - `assignment.New(db *gorm.DB, cache store.KV, log logf.Logger) *Assigner`
  - `middleware.RateLimitOpts{Counter store.Counter; Log; Max; Window; KeyPrefix; TrustProxy}`
  - `worker.New(cfg *config.Config, db *gorm.DB, consumer queue.Consumer, broker store.Broker, log logf.Logger) *Worker`; `Worker.Consumer` is `queue.Consumer`; `Worker.Redis` field removed

- [ ] **Step 1: Update the rate-limit test first**

In `internal/middleware/ratelimit_test.go`, replace every Redis setup (`rdb := testutil.SetupTestRedis(t)` + nil-skip + any `FlushDB`) with:

```go
	mem := store.NewMemory()
	t.Cleanup(mem.Close)
```

and every `Redis: rdb,` inside `middleware.RateLimitOpts{...}` with `Counter: mem,`. Remove the `redis` import, add `github.com/trysoft/whatomate/internal/store`. Add this test:

```go
func TestRateLimit_FailsOpenOnCounterError(t *testing.T) {
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Counter: failingCounter{}, Log: testutil.NopLogger(), Max: 1, Window: time.Minute, KeyPrefix: "x",
	})
	req := testutil.NewGETRequest(t)
	assert.NotNil(t, rl(req))
	assert.NotNil(t, rl(req))
}

type failingCounter struct{}

func (failingCounter) Incr(context.Context, string, time.Duration) (int64, time.Duration, error) {
	return 0, 0, errors.New("down")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/middleware/ -v`
Expected: FAIL — `unknown field Counter in struct literal`.

- [ ] **Step 3: Update `internal/middleware/ratelimit.go`**

Field: replace `Redis      *redis.Client` with `Counter    store.Counter`. Doc comments: "using Redis INCR + EXPIRE" → "using a fixed-window counter"; "if Redis is unavailable" → "if the counter store is unavailable"; `KeyPrefix` comment → `// Key prefix (e.g., "login", "register").`. Replace the body of `enforce` from `count, err := opts.Redis.Incr(ctx, key).Result()` through the `if count > int64(opts.Max) {` block's `retryAfter` line with:

```go
	count, resetIn, err := opts.Counter.Incr(ctx, key, opts.Window)
	if err != nil {
		// Fail open — log and allow request.
		opts.Log.Error("Rate limit counter failed", "error", err, "key", key)
		return r
	}

	if count > int64(opts.Max) {
		retryAfter := max(int(math.Ceil(resetIn.Seconds())), 1)
```

Imports: drop `github.com/redis/go-redis/v9`, add `math` and `github.com/trysoft/whatomate/internal/store`.

- [ ] **Step 4: Run middleware tests**

Run: `go test ./internal/middleware/ -v`
Expected: PASS.

- [ ] **Step 5: Calling manager on `store.KV`**

`internal/calling/session.go`: field `redis         *redis.Client` → `cache         store.KV`; `NewManager(..., rd *redis.Client, ...)` → `NewManager(..., cache store.KV, ...)`; `redis:      rd,` → `cache:      cache,`. Swap the `go-redis` import for `internal/store` if nothing else in the file uses go-redis (`rg -n 'redis\.' internal/calling/session.go`).

`internal/calling/cache.go`: add these helpers after the constants and use them for every Redis call in the file:

```go
func (m *Manager) cacheGet(ctx context.Context, key string) (string, bool) {
	if m.cache == nil {
		return "", false
	}
	v, err := m.cache.Get(ctx, key)
	if err != nil {
		return "", false
	}
	return string(v), true
}

func (m *Manager) cacheSet(ctx context.Context, key, val string) {
	if m.cache != nil {
		_ = m.cache.Set(ctx, key, []byte(val), callingCacheTTL)
	}
}

func (m *Manager) cacheDel(ctx context.Context, keys ...string) {
	if m.cache != nil {
		_ = m.cache.Del(ctx, keys...)
	}
}
```

Replacements in `cache.go`:
- `if cached, err := m.redis.Get(ctx, key).Result(); err == nil && cached != "" {` → `if cached, ok := m.cacheGet(ctx, key); ok && cached != "" {` (two places: `getIVRFlowCached`, `getOrgCallingSettingsCached`)
- `if cached, err := m.redis.Get(ctx, key).Result(); err == nil {` → `if cached, ok := m.cacheGet(ctx, key); ok {` (`getIVRFlowByConfigCached`)
- `m.redis.Set(ctx, key, X, callingCacheTTL)` → `m.cacheSet(ctx, key, X)` (five places; `X` is `string(data)`, `"null"` or `"{}"`)
- `InvalidateIVRFlowCache` body → 

```go
	ctx := context.Background()
	m.cacheDel(ctx,
		ivrFlowCachePrefix+flowID.String(),
		fmt.Sprintf("%s%s:%s:call_start", ivrFlowCfgCachePrefix, orgID.String(), accountName),
		fmt.Sprintf("%s%s:%s:outgoing_end", ivrFlowCfgCachePrefix, orgID.String(), accountName),
	)
```
- `InvalidateOrgCallingSettingsCache` body → `m.cacheDel(context.Background(), orgSettingsCachePrefix+orgID.String())`
- Comments "reading from Redis first" / "with Redis caching" → "reading from the cache first" / "with caching".

- [ ] **Step 6: Assigner on `store.KV`**

`internal/assignment/assigner.go`: field `redis *redis.Client` → `cache store.KV`; `func New(db *gorm.DB, redis *redis.Client, log logf.Logger) *Assigner` → `func New(db *gorm.DB, cache store.KV, log logf.Logger) *Assigner` with `cache: cache` in the literal; `InvalidateTeamCache`'s `a.redis.Del(ctx, key)` → 

```go
	if a.cache != nil {
		_ = a.cache.Del(ctx, key)
	}
```

`internal/assignment/cache.go`: replace

```go
	cached, err := a.redis.Get(ctx, key).Result()
	if err == nil && cached != "" {
		var cfg TeamConfig
		if err := json.Unmarshal([]byte(cached), &cfg); err == nil {
```

with

```go
	if a.cache != nil {
		if cached, err := a.cache.Get(ctx, key); err == nil {
			var cfg TeamConfig
			if err := json.Unmarshal(cached, &cfg); err == nil {
				return &cfg
			}
		}
	}
```

(removing the old block's `return &cfg` / closing braces), and

```go
	data, err := json.Marshal(cfg)
	if err == nil {
		a.redis.Set(ctx, key, string(data), teamCacheTTL)
	}
```

with

```go
	if data, err := json.Marshal(cfg); err == nil && a.cache != nil {
		_ = a.cache.Set(ctx, key, data, teamCacheTTL)
	}
```

`internal/assignment/assigner_test.go:29`: build with a memory store instead of Redis:

```go
	mem := store.NewMemory()
	t.Cleanup(mem.Close)
	return assignment.New(db, mem, testutil.NopLogger()), db
```

and delete the `SetupTestRedis` lines/skip above it.

- [ ] **Step 7: Worker on `queue.Consumer` + `store.Broker`**

`internal/worker/worker.go`: struct fields become

```go
type Worker struct {
	Config    *config.Config
	DB        *gorm.DB
	Log       logf.Logger
	WhatsApp  *whatsapp.Client
	Consumer  queue.Consumer
	Publisher *queue.Publisher
}
```

and `New` becomes

```go
// New creates a new Worker instance
func New(cfg *config.Config, db *gorm.DB, consumer queue.Consumer, broker store.Broker, log logf.Logger) *Worker {
	return &Worker{
		Config:    cfg,
		DB:        db,
		Log:       log,
		WhatsApp:  whatsapp.New(log),
		Consumer:  consumer,
		Publisher: queue.NewPublisher(broker, log),
	}
}
```

Drop the `fmt` import if unused and the go-redis import; add `internal/store`. `worker_test.go` lines 33-37 become:

```go
	w.Publisher = queue.NewPublisher(store.NewMemoryBroker(), log)
```

- [ ] **Step 8: Run tests**

Run: `TEST_DB_DRIVER=sqlite go test ./internal/middleware/ ./internal/assignment/ ./internal/calling/... ./internal/worker/ ./internal/queue/ ./internal/store/...`
Expected: PASS. (`go build ./...` still fails in `cmd/whatomate` and `internal/handlers` until Task 9.)

- [ ] **Step 9: Commit**

```bash
git add internal/calling internal/assignment internal/middleware internal/worker
git commit -m "refactor: calling, assignment, rate limit and worker use store interfaces"
```

---

### Task 9: Handlers and server wiring

**Files:**
- Create: `internal/handlers/store_helpers.go`, `internal/handlers/store_helpers_test.go`
- Modify: `internal/handlers/app.go`, `cache.go`, `meta_analytics.go`, `auth.go`, `sso.go`, `google_oauth.go`, `passkeys.go`, `flow_endpoint.go`, `call_webhook.go`, `chat_delay_processor.go`, `cmd/whatomate/main.go`
- Modify tests: `internal/handlers/testhelpers_test.go`, `webhook_test.go`, `cache_internal_test.go`, `goroutines_test.go`, `sso_test.go`, `auth_gaps_test.go`, `meta_analytics_test.go`, `chatbot_processor_test.go`, `sla_processor_internal_test.go`, `google_oauth_test.go`

**Interfaces:**
- Consumes: `store.Stores`, `store.New`, `store.KV`, `store.Scheduler`, `store.Broker`, `store.ErrNotFound`, `queue.NewDBQueue`, `(*DBQueue).NewConsumer`, `queue.NewRedisQueue`, `queue.NewRedisConsumer`, new `worker.New`, `calling.NewManager`, `assignment.New`, `middleware.RateLimitOpts{Counter}`
- Produces: `handlers.App` fields `Cache store.KV`, `State store.KV`, `Scheduler store.Scheduler`, `Broker store.Broker` (field `Redis` removed); helper methods `cacheGetJSON`, `cacheSetJSON`, `cacheDel`, `cacheDelPrefix`

- [ ] **Step 1: Write the failing helper test `internal/handlers/store_helpers_test.go`**

```go
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
)

func TestCacheHelpers(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	defer mem.Close()
	a := &App{Cache: mem, Log: testutil.NopLogger()}

	a.cacheSetJSON(ctx, "k:1", map[string]int{"n": 1}, time.Hour)
	var got map[string]int
	assert.True(t, a.cacheGetJSON(ctx, "k:1", &got))
	assert.Equal(t, 1, got["n"])

	a.cacheSetJSON(ctx, "k:2", 2, time.Hour)
	a.cacheDelPrefix(ctx, "k:")
	assert.False(t, a.cacheGetJSON(ctx, "k:1", &got))
	assert.False(t, a.cacheGetJSON(ctx, "k:2", &got))

	nilApp := &App{Log: testutil.NopLogger()}
	nilApp.cacheSetJSON(ctx, "k", 1, time.Hour) // no panic without a cache
	assert.False(t, nilApp.cacheGetJSON(ctx, "k", &got))
	nilApp.cacheDel(ctx, "k")
	nilApp.cacheDelPrefix(ctx, "k")
}
```

- [ ] **Step 2: Implement `internal/handlers/store_helpers.go`**

```go
package handlers

import (
	"context"
	"encoding/json"
	"time"
)

// cacheGetJSON decodes the cached value at key into dst; false on miss.
func (a *App) cacheGetJSON(ctx context.Context, key string, dst any) bool {
	if a.Cache == nil {
		return false
	}
	b, err := a.Cache.Get(ctx, key)
	if err != nil || len(b) == 0 {
		return false
	}
	return json.Unmarshal(b, dst) == nil
}

// cacheSetJSON stores v as JSON at key; failures are logged and ignored.
func (a *App) cacheSetJSON(ctx context.Context, key string, v any, ttl time.Duration) {
	if a.Cache == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := a.Cache.Set(ctx, key, b, ttl); err != nil {
		a.Log.Warn("Cache write failed", "key", key, "error", err)
	}
}

func (a *App) cacheDel(ctx context.Context, keys ...string) {
	if a.Cache != nil {
		_ = a.Cache.Del(ctx, keys...)
	}
}

func (a *App) cacheDelPrefix(ctx context.Context, prefix string) {
	if a.Cache != nil {
		_ = a.Cache.DelPrefix(ctx, prefix)
	}
}
```

- [ ] **Step 3: `App` fields, readiness and campaign subscriber (`internal/handlers/app.go`)**

Replace `Redis             *redis.Client` with:

```go
	// Cache holds short-lived caches (memory, or Redis in Redis mode).
	Cache store.KV
	// State holds logins, OAuth/passkey state, flow state and sticky calls
	// (database, or Redis in Redis mode).
	State     store.KV
	Scheduler store.Scheduler
	Broker    store.Broker
```

Readiness check (lines ~126-130) becomes:

```go
	// Check the state store (database or Redis)
	if a.State != nil {
		if err := a.State.Ping(r.RequestCtx); err != nil {
			a.Log.Error("State store error", "error", err)
			return r.SendErrorEnvelope(500, "State store error", nil, "")
		}
	}
```

In `StartCampaignStatsSubscriber`: `queue.NewSubscriber(a.Redis, a.Log)` → `queue.NewSubscriber(a.Broker, a.Log)`; log text "from Redis" → "" (i.e. `"Received campaign stats update"`); doc comment "from Redis pub/sub" → "from the broker". Also add, at the start of the function after the WSHub check:

```go
	if a.Broker == nil {
		return nil
	}
```

Imports: drop go-redis, add `internal/store`.

- [ ] **Step 4: `internal/handlers/cache.go` — apply these exact replacements**

Pattern A (read):

```go
	cached, err := a.Redis.Get(ctx, cacheKey).Result()
	if err == nil && cached != "" {
		var T X
		if err := json.Unmarshal([]byte(cached), &T); err == nil {
			BODY
		}
	}
```
→
```go
	var T X
	if a.cacheGetJSON(ctx, cacheKey, &T) {
		BODY
	}
```

Pattern B (write): `if data, err := json.Marshal(V); err == nil { a.Redis.Set(ctx, K, data, TTL) }` → `a.cacheSetJSON(ctx, K, V, TTL)`.

Apply per function:
- `getChatbotSettingsCached`: the `if a.Redis != nil { cached ... }` block → Pattern A with `T=cacheData`, `X=chatbotSettingsCache`; the `if a.Redis != nil { cacheData := ...; if data ... }` block →
  ```go
	a.cacheSetJSON(ctx, cacheKey, chatbotSettingsCache{
		ChatbotSettings:  settings,
		AIAPIKey:         settings.AI.APIKey,
		TranscribeAPIKey: settings.AI.TranscribeAPIKey,
	}, settingsCacheTTL)
  ```
- `getChatbotFlowsCached`: delete the `if a.Redis == nil { ... return flows, nil }` early-return block; Pattern A (`flows`, `[]models.ChatbotFlow`); Pattern B (`flows`, `flowsCacheTTL`).
- `getKeywordRulesCached`: Pattern A (`rules`, `[]models.KeywordRule`) — rename the later `var rules []models.KeywordRule` declaration to an assignment `rules = append(accountRules, globalRules...)` and drop its separate `var`; Pattern B (`rules`, `keywordRulesCacheTTL`).
- `InvalidateChatbotSettingsCache`: `a.deleteKeysByPattern(ctx, pattern)` with `pattern := fmt.Sprintf("%s%s:*", ...)` → `a.cacheDelPrefix(ctx, fmt.Sprintf("%s%s:", settingsCachePrefix, orgID.String()))`.
- `InvalidateChatbotFlowsCache`: `a.Redis.Del(ctx, cacheKey)` → `a.cacheDel(ctx, cacheKey)`.
- `InvalidateKeywordRulesCache`: → `a.cacheDelPrefix(ctx, fmt.Sprintf("%s%s:", keywordRulesCachePrefix, orgID.String()))`.
- Delete `deleteKeysByPattern`.
- `getWhatsAppAccountCached`: Pattern A (`cacheData`, `whatsAppAccountCache`); Pattern B with the `whatsAppAccountCache{...}` literal as `V`, `whatsappAccountCacheTTL`.
- `InvalidateWhatsAppAccountCache`, `InvalidateWebhooksCache`, `InvalidateSLASettingsCache`, `InvalidateTagsCache`: `a.Redis.Del(ctx, K)` → `a.cacheDel(ctx, K)`.
- `getWebhooksCached` (`webhooks`, `[]models.Webhook`, `webhooksCacheTTL`), `getSLAEnabledSettingsCached` (`settings`, `[]models.ChatbotSettings`, key `slaSettingsCacheKey`, `slaSettingsCacheTTL`), `getAIContextsCached` (`contexts`, `[]models.AIContext`, `aiContextsCacheTTL`; same `var`→assignment fix as keyword rules), `getTagsCached` (`tags`, `[]models.Tag`, `tagsCacheTTL`), `GetRolePermissionsCached` (`perms`, `[]string`, `rolePermissionsCacheTTL`): Patterns A and B.
- `InvalidateAIContextsCache`: → `a.cacheDelPrefix(ctx, fmt.Sprintf("%s%s:", aiContextsCachePrefix, orgID.String()))`.
- `getUserPermissionsCached`: the `if a.Redis != nil { ... }` read → Pattern A (`perms`, `UserPermissions`, BODY `return &perms, nil`); both `if a.Redis != nil { if data ... }` writes → `a.cacheSetJSON(ctx, cacheKey, perms, userPermissionsCacheTTL)`. Rename the later `perms := UserPermissions{...}` to `perms = UserPermissions{...}`, and in the super-admin branch `perms := UserPermissions{IsSuperAdmin: true, ...}` to `perms = UserPermissions{IsSuperAdmin: true, Permissions: []string{}}`.
- `InvalidateUserPermissionsCache`: →
  ```go
	ctx := context.Background()
	base := fmt.Sprintf("%s%s", userPermissionsCachePrefix, userID.String())
	a.cacheDel(ctx, base)
	a.cacheDelPrefix(ctx, base+":")
  ```
- `InvalidateRolePermissionsCache`: `a.Redis.Del(ctx, roleCacheKey)` → `a.cacheDel(ctx, roleCacheKey)`.
- Comment on `decryptChatbotSettingsSecrets`: "Redis keeps the encrypted form." → "The cache keeps the encrypted form."

- [ ] **Step 5: Remaining handler files**

`meta_analytics.go`:
- Analytics read → `var cachedResponse []MetaAnalyticsResponse` + `if a.cacheGetJSON(ctx, cacheKey, &cachedResponse) { ...existing body... }`.
- Analytics write → `a.cacheSetJSON(ctx, cacheKey, results, cacheTTL)`.
- `getWABACurrency`:
  ```go
	if a.Cache != nil {
		if cached, err := a.Cache.Get(ctx, cacheKey); err == nil && len(cached) > 0 {
			return string(cached)
		}
	}
  ```
  and the write → `if a.Cache != nil { _ = a.Cache.Set(ctx, cacheKey, []byte(info.Currency), metaWABACurrencyCacheTTL) }`.

`auth.go`:
- Refresh consumption (line ~305):
  ```go
	// Validate JTI (single-use: removed on consumption)
	if claims.ID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := a.State.GetDel(ctx, refreshTokenKey(claims.ID)); err != nil {
			// Token was already used or revoked
			return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Refresh token has been revoked", nil, "")
		}
	}
  ```
- Store (line ~408): `a.Redis.Set(ctx, refreshTokenKey(jti), user.ID.String(), expiry).Err()` → `a.State.Set(ctx, refreshTokenKey(jti), []byte(user.ID.String()), expiry)`; log "in Redis" → "".
- Logout (line ~536): `a.Redis.Del(ctx, refreshTokenKey(claims.ID))` → `_ = a.State.Del(ctx, refreshTokenKey(claims.ID))`.
- Comment `// refreshTokenKey returns the Redis key for a refresh token JTI.` → `// refreshTokenKey returns the state key for a refresh token JTI.`

`sso.go`:
- Store: `a.Redis.Set(redisCtx, stateKey, stateJSON, 5*time.Minute).Err()` → `a.State.Set(redisCtx, stateKey, stateJSON, 5*time.Minute)`; rename `redisCtx` → `stateCtx` in both functions; comments "in Redis"/"from Redis" → "".
- Callback: replace the `Get` + separate `Del` with a single-use read:
  ```go
	stateJSON, err := a.State.GetDel(stateCtx, stateKey)
	if err != nil {
		a.redirectWithError(r, "Invalid or expired state")
		return nil
	}
  ```
  and delete the following `a.Redis.Del(redisCtx, stateKey)` line and its comment.

`google_oauth.go`: `a.Redis.Set(redisCtx, googleOAuthStatePrefix+nonce, stateJSON, googleOAuthStateTTL).Err()` → `a.State.Set(stateCtx, googleOAuthStatePrefix+nonce, stateJSON, googleOAuthStateTTL)` (rename `redisCtx` → `stateCtx`); `a.Redis.GetDel(ctx, googleOAuthStatePrefix+nonce).Bytes()` → `a.State.GetDel(ctx, googleOAuthStatePrefix+nonce)`; comment "stored in Redis" → "stored in the state store".

`passkeys.go`: `return a.Redis.Set(ctx, key, data, passkeySessionTTL).Err()` → `return a.State.Set(ctx, key, data, passkeySessionTTL)`; `data, err := a.Redis.GetDel(ctx, key).Bytes()` → `data, err := a.State.GetDel(ctx, key)`.

`flow_endpoint.go`: delete `flowEndpointMemState` and its comment; the three functions become:

```go
func (a *App) loadFlowEndpointState(ctx context.Context, key string) *flowEndpointState {
	st := &flowEndpointState{Data: map[string]any{}}
	if a.State != nil {
		if raw, err := a.State.Get(ctx, key); err == nil {
			_ = json.Unmarshal(raw, st)
		}
	}
	if st.Data == nil {
		st.Data = map[string]any{}
	}
	return st
}

func (a *App) saveFlowEndpointState(ctx context.Context, key string, st *flowEndpointState) {
	if a.State == nil {
		return
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := a.State.Set(ctx, key, raw, flowEndpointStateTTL); err != nil {
		a.Log.Warn("flow endpoint: failed to save state", "error", err)
	}
}

func (a *App) deleteFlowEndpointState(ctx context.Context, key string) {
	if a.State != nil {
		_ = a.State.Del(ctx, key)
	}
}
```

Remove the `sync` import if now unused. Tests that relied on `flowEndpointMemState` get a `State` via `newTestApp` (Step 7).

`call_webhook.go`: `a.Redis == nil` → `a.State == nil` (two places); `a.Redis.Set(ctx, stickyCallKey(orgID, phone), agentID.String(), time.Duration(ttlMinutes)*time.Minute).Err()` → `a.State.Set(ctx, stickyCallKey(orgID, phone), []byte(agentID.String()), time.Duration(ttlMinutes)*time.Minute)`; `val, err := a.Redis.Get(ctx, stickyCallKey(orgID, phone)).Result()` → `raw, err := a.State.Get(ctx, stickyCallKey(orgID, phone))` then `val := string(raw)` after the error check; rename `findStickyAgentInRedis` → `findStickyAgent` (update its caller: `rg -n findStickyAgentInRedis internal`); comments "Redis key"/"in Redis"/"a Redis failure"/"Redis is unhealthy" → "state key"/""/"a store failure"/"the store is unavailable"; log "in Redis" → "".

`chat_delay_processor.go` — replace the Redis calls:

```go
func (p *ChatDelayProcessor) processDueDelays(ctx context.Context) {
	if p.app.Scheduler == nil {
		return
	}
	ids, err := p.app.Scheduler.PopDue(ctx, chatDelayZSetKey, time.Now(), 50)
	if err != nil {
		p.app.Log.Warn("chat delay poll failed", "error", err)
	}
	for _, idStr := range ids {
		sessionID, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if err := p.app.resumeChatSessionAfterDelay(sessionID); err != nil {
			p.app.Log.Warn("chat delay resume failed", "session", sessionID, "error", err)
		}
	}
}
```

`scheduleChatDelay`: guard `a.Scheduler == nil`; body → `a.Scheduler.Schedule(ctx, chatDelayZSetKey, sessionID.String(), time.Now().Add(time.Duration(seconds)*time.Second))` with the same error log. `cancelChatDelaysForContact`: guard `a.Scheduler == nil`; build `items := make([]string, len(sessionIDs))` of `id.String()` and call `a.Scheduler.Cancel(ctx, chatDelayZSetKey, items...)`. `scheduleAgentSessionTimeout`: guard `a.Scheduler == nil`; `a.Scheduler.Schedule(ctx, agentSessionTimeoutZSetKey, contactID.String(), until)`. `cancelAgentSessionTimeout`: guard; `a.Scheduler.Cancel(ctx, agentSessionTimeoutZSetKey, contactID.String())`. `processDueAgentSessionTimeouts`: same shape as `processDueDelays` with `agentSessionTimeoutZSetKey` and `handleAgentSessionTimeoutExpiry(contactID)`. Remove `strconv` and go-redis imports. Key constant names stay (`chatDelayZSetKey` etc.) so Redis mode uses the same keys.

- [ ] **Step 6: Server wiring (`cmd/whatomate/main.go`)**

Server (lines ~174-183): replace the Redis connect + `queue.NewRedisQueue` with:

```go
	// Caches, state, scheduler and broker: embedded unless [redis] host is set
	stores, err := store.New(&cfg.Redis, db)
	if err != nil {
		lo.Fatal("Failed to connect to Redis", "error", err)
	}
	defer stores.Close()
	if stores.Embedded() {
		lo.Info("Using built-in store (no Redis server)")
	} else {
		lo.Info("Connected to Redis")
	}

	var jobQueue queue.Queue
	var dbQueue *queue.DBQueue
	if stores.Embedded() {
		dbQueue = queue.NewDBQueue(db, lo)
		jobQueue = dbQueue
	} else {
		jobQueue = queue.NewRedisQueue(stores.Redis, lo)
	}
	lo.Info("Job queue initialized")
```

`App{...}` literal: replace `Redis:      rdb,` with

```go
		Cache:     stores.Cache,
		State:     stores.State,
		Scheduler: stores.Scheduler,
		Broker:    stores.Broker,
```

`assignment.New(db, rdb, lo)` → `assignment.New(db, stores.Cache, lo)`; `calling.NewManager(..., rdb, ...)` → `calling.NewManager(..., stores.Cache, ...)`; `setupRoutes(g, app, lo, cfg.Server.BasePath, rdb, cfg)` → `setupRoutes(g, app, lo, cfg.Server.BasePath, stores.Counter, cfg)`; `setupRoutes` signature `rdb *redis.Client` → `counter store.Counter`; every `Redis: rdb,` / `Redis:      rdb,` in `RateLimitOpts` → `Counter: counter,`.

Add a helper near `runWorker`:

```go
// newConsumer returns a campaign job consumer for the active store mode.
func newConsumer(stores *store.Stores, dbQueue *queue.DBQueue) (queue.Consumer, error) {
	if stores.Embedded() {
		return dbQueue.NewConsumer(), nil
	}
	return queue.NewRedisConsumer(stores.Redis, lo)
}
```

(pass `lo` as a parameter if it is not package-level: `func newConsumer(stores *store.Stores, dbQueue *queue.DBQueue, lo logf.Logger)`.) Embedded workers loop (line ~333):

```go
			consumer, err := newConsumer(stores, dbQueue, lo)
			if err != nil {
				lo.Fatal("Failed to create worker", "error", err, "worker_num", i+1)
			}
			w := worker.New(cfg, db, consumer, stores.Broker, lo)
```

`runWorker` (lines ~439-460): replace the Redis connect with the same `store.New` block (fatal message unchanged, `defer stores.Close()`), create `dbQueue := queue.NewDBQueue(db, lo)` when `stores.Embedded()`, log `"No Redis configured: live campaign progress is not pushed to the UI from separate worker processes"` when embedded, and build workers with `newConsumer` + `worker.New` as above.

Imports: add `internal/store`; remove `github.com/redis/go-redis/v9` and the `database.NewRedis` usage (keep the `database` import for `database.Open`/migrations).

- [ ] **Step 7: Handler tests**

`testhelpers_test.go` `newTestApp`: delete the `SetupTestRedis` lines and the skip; after `db := testutil.SetupTestDB(t)` add

```go
	mem := store.NewMemory()
	t.Cleanup(mem.Close)
	state := store.NewSQLKV(db)
	t.Cleanup(state.Close)
```

and in the `App` literal replace `Redis:  redisClient,` with

```go
		Cache:     mem,
		State:     state,
		Scheduler: store.NewSQLScheduler(db),
		Broker:    store.NewMemoryBroker(),
```

Update the doc comment ("with a test database, Redis, and default config" → "with a test database, built-in stores and default config"; drop "Skips the test if TEST_REDIS_URL is not set.").

`webhook_test.go:150-168`: same replacement as `newTestApp`.

For the remaining test files, translate direct Redis calls (`ctx` is whatever the test already uses):

| Old | New |
|---|---|
| `app.Redis.Get(ctx, k).Result()` (string) | `v, err := app.Cache.Get(ctx, k)` then `string(v)` |
| `app.Redis.Set(ctx, k, v, ttl).Err()` | `app.Cache.Set(ctx, k, []byte(v), ttl)` (use `data` directly when already `[]byte`) |
| `app.Redis.Del(ctx, k)` | `app.Cache.Del(ctx, k)` |
| `app.Redis.Exists(ctx, k).Result()` → `exists == 0/1` | `_, err := app.Cache.Get(ctx, k)` → `assert.ErrorIs(t, err, store.ErrNotFound)` / `assert.NoError(t, err)` |
| `app.Redis.FlushDB(...)` | delete the line |
| `testutil.SetupTestRedis(t)` + skip | delete; use `newTestApp` |

Use `app.State` instead of `app.Cache` for keys that start with `refresh:`, `sso:state:`, `google_oauth:state:`, `passkey:`, `flow_endpoint:`, `vc_sticky:`. In `goroutines_test.go`, chatbot-delay assertions on the `whatomate:chatbot:delays` sorted set (`ZScore`/`ZCard`/`ZRange`) become `app.Scheduler.PopDue(ctx, "whatomate:chatbot:delays", time.Now().Add(24*time.Hour), 100)` and an assertion on the returned IDs. Test files that build `&handlers.App{...}` / `&App{...}` with `Redis: rdb` (e.g. `chatbot_processor_test.go`, `sla_processor_internal_test.go`, `meta_analytics_test.go`, `google_oauth_test.go`) switch to `Cache: store.NewMemory()` (+ `State: store.NewSQLKV(db)` where the test uses state keys) and drop the Redis skip. `cache_internal_test.go`'s assertion "Redis must keep the encrypted key" → "The cache must keep the encrypted key".

Find every remaining reference with: `rg -n 'Redis|redis' internal/handlers --glob '*_test.go'` — the result must be empty when done.

- [ ] **Step 8: Build and run**

Run: `gofmt -l internal cmd` (expect no output), `go build ./... && go vet ./...`
Run: `TEST_DB_DRIVER=sqlite go test ./...`
Run: `TEST_DATABASE_URL=<postgres whatomate_test DSN> go test ./...`
Expected: PASS. Handler tests that used to SKIP without `TEST_REDIS_URL` now run. Any failure unrelated to this change (a handler test that was silently skipped before) is reported with its name and error; fix it only if it is caused by the store change, otherwise file a bd issue.

- [ ] **Step 9: Commit**

```bash
git add internal/handlers cmd/whatomate
git commit -m "feat: handlers and server use the built-in store; Redis optional"
```

---

### Task 10: Configuration, Docker and docs

**Files:**
- Modify: `config.example.toml:43-49`, `docker-compose.yml`, `docs/src/content/docs/getting-started/configuration.mdx` (`[redis]` section ~line 169 and the example at ~line 42), `internal/config/config_test.go:40` (only if the default changes; it does not)

**Interfaces:** none (docs/config only).

- [ ] **Step 1: `config.example.toml`** — replace the `[redis]` block with:

```toml
# Redis is optional. Leave this section commented out to use the built-in
# store (memory + the main database). Set host to use a Redis server, e.g.
# when running several API servers or separate worker machines.
# [redis]
# host = "localhost"
# port = 6379
# username = ""  # Redis ACL username (Redis 6+)
# password = ""
# db = 0
# tls = false    # true for TLS connections (e.g. Upstash, Redis Cloud)
```

- [ ] **Step 2: `docker-compose.yml`** — delete the `redis:` service block (lines ~52-66), the app's `redis:` entry under `depends_on` (lines ~17-18; delete `depends_on` entirely if it becomes empty), and `redis-data:` under `volumes`. If the compose file sets `WHATOMATE_REDIS__HOST`, delete that line too (`rg -n REDIS docker-compose.yml .env.example`).

- [ ] **Step 3: `configuration.mdx`** — in the example config, comment out the `[redis]` block the same way as Step 1. Replace the body of the `### [redis]` section with:

```md
Optional. When `host` is empty (the default), whatomate keeps caches and rate
limits in memory and stores logins, OAuth and passkey state, WhatsApp Flow
state, chatbot delays and the campaign queue in the main database. No Redis
server is needed.

Set `host` to use a Redis server instead. Do this when you run several API
servers behind a load balancer, or `whatomate worker` on separate machines and
want live campaign progress in the UI.

Switching an existing install from Redis to the built-in store signs users out
once. Campaign messages still queued in Redis are not moved: pause and start
the campaign again to re-queue its pending recipients.
```

followed by the existing key table (host, port, username, password, db, tls).

- [ ] **Step 4: Verify**

Run: `go test ./internal/config/...` → PASS. `rg -n 'redis' docker-compose.yml` → no output.

- [ ] **Step 5: Commit**

```bash
git add config.example.toml docker-compose.yml docs/src/content/docs/getting-started/configuration.mdx
git commit -m "docs: Redis is optional; drop Redis from docker-compose"
```

---

### Task 11: End-to-end verification without Redis

**Files:** none (verification; fixes go in the task that owns the code).

- [ ] **Step 1: Quality gates**

Run: `gofmt -l internal cmd` (no output), `go vet ./...`, `TEST_DB_DRIVER=sqlite go test ./...`, `TEST_DATABASE_URL=<postgres DSN> go test ./...`, `CGO_ENABLED=0 go build -o /tmp/whatomate-nocgo ./cmd/whatomate`.
Expected: all pass.

- [ ] **Step 2: Run the server with no Redis**

Comment out `[redis]` in `config.local.toml` (keep a note of the original lines to restore), stop the local Redis container if any is used only by whatomate — do NOT stop other projects' containers; instead verify by pointing nothing at Redis: `rg -n '^\[redis\]' config.local.toml` → no output. Build `go build -o whatomate ./cmd/whatomate` and start it on 8080 as a long-running background command (`./whatomate server -config config.local.toml > /tmp/whatomate.log 2>&1`). Check the log contains `Using built-in store (no Redis server)` and `curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/ready` → `200`.

- [ ] **Step 3: Manual checks (ask the user to drive the UI parts)**

1. Log in, restart the server, reload the UI → still logged in.
2. Send a small campaign to the user's own test number only → messages sent, progress updates live.
3. A chatbot flow with a Delay node: trigger it, restart during the delay → the flow resumes after the delay.
4. Place a WhatsApp call into an IVR with an AI Call node → works as before.

- [ ] **Step 4: Close out**

`bd close` the Phase 1 tasks; report changed files, test results, and the commands to restore `[redis]` in `config.local.toml` if the user wants Redis mode back.
