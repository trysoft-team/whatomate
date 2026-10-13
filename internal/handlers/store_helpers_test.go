package handlers

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/zerodha/logf"
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

// failingKV fails every write and delete.
type failingKV struct{ store.KV }

var errStoreDown = errors.New("store down")

func (failingKV) Set(context.Context, string, []byte, time.Duration) error { return errStoreDown }
func (failingKV) Del(context.Context, ...string) error                     { return errStoreDown }
func (failingKV) DelPrefix(context.Context, string) error                  { return errStoreDown }

func TestStoreFailuresAreLogged(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	a := &App{
		Cache: failingKV{},
		State: failingKV{},
		Log:   logf.New(logf.Opts{Writer: &buf, Level: logf.WarnLevel}),
	}

	a.cacheSetJSON(ctx, "k:set", 1, time.Hour)
	a.cacheDel(ctx, "k:del")
	a.cacheDelPrefix(ctx, "k:prefix:")
	a.deleteFlowEndpointState(ctx, "flow_endpoint:x")

	out := buf.String()
	assert.Contains(t, out, "Cache write failed")
	assert.Contains(t, out, "k:del")
	assert.Contains(t, out, "k:prefix:")
	assert.Equal(t, 2, strings.Count(out, "Cache delete failed"))
	assert.Contains(t, out, "flow endpoint: failed to delete state")
}
