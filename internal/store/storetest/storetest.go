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

		require.NoError(t, kv.Set(ctx, "g:[ab]?:1", []byte("x"), time.Hour))
		require.NoError(t, kv.Set(ctx, "g:a1:1", []byte("x"), time.Hour))
		require.NoError(t, kv.DelPrefix(ctx, "g:[ab]?"))
		_, err = kv.Get(ctx, "g:a1:1")
		assert.NoError(t, err)
		_, err = kv.Get(ctx, "g:[ab]?:1")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("KeysCaseSensitive", func(t *testing.T) {
		kv := newKV(t)
		require.NoError(t, kv.Set(ctx, "Abc", []byte("1"), time.Hour))
		require.NoError(t, kv.Set(ctx, "abc", []byte("2"), time.Hour))
		v, err := kv.Get(ctx, "Abc")
		require.NoError(t, err)
		assert.Equal(t, "1", string(v))
		v, err = kv.Get(ctx, "abc")
		require.NoError(t, err)
		assert.Equal(t, "2", string(v))

		require.NoError(t, kv.DelPrefix(ctx, "Ab"))
		_, err = kv.Get(ctx, "Abc")
		assert.ErrorIs(t, err, store.ErrNotFound)
		v, err = kv.Get(ctx, "abc")
		require.NoError(t, err)
		assert.Equal(t, "2", string(v))
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
		_, _, err := c.Incr(ctx, "rl:c", time.Minute)
		require.NoError(t, err)
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

	t.Run("ItemsCaseSensitive", func(t *testing.T) {
		s := newScheduler(t)
		require.NoError(t, s.Schedule(ctx, "set", "X", now.Add(-time.Minute)))
		require.NoError(t, s.Schedule(ctx, "set", "x", now.Add(-time.Minute)))
		got, err := s.PopDue(ctx, "set", now, 50)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"X", "x"}, got)
	})

	t.Run("SetNamesCaseSensitive", func(t *testing.T) {
		s := newScheduler(t)
		require.NoError(t, s.Schedule(ctx, "S", "upper", now.Add(-time.Minute)))
		require.NoError(t, s.Schedule(ctx, "s", "lower", now.Add(-time.Minute)))
		got, err := s.PopDue(ctx, "S", now, 50)
		require.NoError(t, err)
		assert.Equal(t, []string{"upper"}, got)
		got, err = s.PopDue(ctx, "s", now, 50)
		require.NoError(t, err)
		assert.Equal(t, []string{"lower"}, got)
	})

	t.Run("NonPositiveLimit", func(t *testing.T) {
		s := newScheduler(t)
		require.NoError(t, s.Schedule(ctx, "set", "a", now.Add(-time.Minute)))
		for _, limit := range []int{0, -1} {
			got, err := s.PopDue(ctx, "set", now, limit)
			require.NoError(t, err)
			assert.Empty(t, got, "limit %d", limit)
		}
		got, err := s.PopDue(ctx, "set", now, 50)
		require.NoError(t, err)
		assert.Equal(t, []string{"a"}, got)
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
					if !assert.NoError(t, err) {
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
		var extra atomic.Int32
		gotA, gotB := make(chan string, 1), make(chan string, 1)
		stopA, err := b.Subscribe(ctx, "ch", sendTo(gotA, &extra))
		require.NoError(t, err)
		defer stopA()
		stopB, err := b.Subscribe(ctx, "ch", sendTo(gotB, &extra))
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
		assert.Zero(t, extra.Load(), "duplicate deliveries")
	})

	t.Run("StopEndsDelivery", func(t *testing.T) {
		b := newBroker(t)
		var extra atomic.Int32
		got := make(chan string, 2)
		stop, err := b.Subscribe(ctx, "ch2", sendTo(got, &extra))
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
		var extra atomic.Int32
		got := make(chan string, 1)
		stop, err := b.Subscribe(ctx, "ch3", sendTo(got, &extra))
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

// sendTo returns a subscriber callback that never blocks: a delivery that
// finds ch full is counted in extra instead of stalling the subscriber.
func sendTo(ch chan string, extra *atomic.Int32) func([]byte) {
	return func(p []byte) {
		select {
		case ch <- string(p):
		default:
			extra.Add(1)
		}
	}
}
