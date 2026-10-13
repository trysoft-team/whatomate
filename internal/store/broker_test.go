package store_test

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
)

func TestMemoryBroker(t *testing.T) {
	storetest.RunBroker(t, func(t *testing.T) store.Broker { return store.NewMemoryBroker() })
}

func TestMemoryBroker_ContextCancelEndsDelivery(t *testing.T) {
	b := store.NewMemoryBroker()
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	_, err := b.Subscribe(ctx, "ch", func([]byte) { n.Add(1) })
	require.NoError(t, err)
	require.NoError(t, b.Publish(context.Background(), "ch", []byte("x")))
	require.Equal(t, int32(1), n.Load())
	cancel()
	assert.Eventually(t, func() bool {
		before := n.Load()
		assert.NoError(t, b.Publish(context.Background(), "ch", []byte("x")))
		return n.Load() == before
	}, time.Second, 10*time.Millisecond)
}

func TestMemoryBroker_StopReleasesGoroutine(t *testing.T) {
	b := store.NewMemoryBroker()
	before := runtime.NumGoroutine()
	for range 100 {
		stop, err := b.Subscribe(context.Background(), "ch", func([]byte) {})
		require.NoError(t, err)
		stop()
	}
	// Polled by hand: assert.Eventually runs its condition in extra goroutines.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before)
}
