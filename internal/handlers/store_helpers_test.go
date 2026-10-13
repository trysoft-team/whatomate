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
