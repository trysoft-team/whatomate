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
	// PopDue removes and returns up to limit items due at or before now,
	// earliest due first. Each item is returned to exactly one caller. A limit
	// <= 0 returns nothing. On error no items were removed; once any item is
	// removed PopDue returns it without an error.
	PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error)
}

// Broker delivers messages published on a channel to its subscribers.
type Broker interface {
	Publish(ctx context.Context, channel string, payload []byte) error
	// Subscribe calls fn for every message on channel until stop is called or
	// ctx is done. fn may run concurrently with itself and may still run once
	// after stop returns; it must not block.
	Subscribe(ctx context.Context, channel string, fn func([]byte)) (stop func(), err error)
}
