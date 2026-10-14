// Package store holds the key/value, counter, scheduler and pub/sub backends
// whatomate uses for caches and short-lived state. Embedded mode keeps them in
// memory and the main database; Redis mode uses a Redis server.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
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
	// <= 0 returns nothing. Once any item is removed PopDue returns it without
	// an error; on error, items not yet removed remain for a later call.
	PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error)
}

// Broker delivers messages published on a channel to its subscribers.
type Broker interface {
	Publish(ctx context.Context, channel string, payload []byte) error
	// Subscribe calls fn for every message on channel until stop is called or
	// ctx is done. fn may run concurrently with itself and may still run after
	// stop returns; it must not block.
	Subscribe(ctx context.Context, channel string, fn func([]byte)) (stop func(), err error)
}

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

// MissingTables returns the names of the embedded-mode tables (key/value
// state, scheduler, campaign queue) that do not exist in db.
func MissingTables(db *gorm.DB) []string {
	var missing []string
	for _, m := range []schema.Tabler{models.KVEntry{}, models.ScheduledItem{}, models.CampaignJob{}} {
		if !db.Migrator().HasTable(m) {
			missing = append(missing, m.TableName())
		}
	}
	return missing
}

// LeftoverRows returns the row counts of the embedded-mode scheduler and
// campaign queue tables that exist and hold rows. Redis mode does not process
// them. Tables that cannot be counted are left out.
func LeftoverRows(db *gorm.DB) map[string]int64 {
	out := map[string]int64{}
	for _, m := range []schema.Tabler{models.ScheduledItem{}, models.CampaignJob{}} {
		if !db.Migrator().HasTable(m) {
			continue
		}
		var n int64
		if err := db.Model(m).Count(&n).Error; err == nil && n > 0 {
			out[m.TableName()] = n
		}
	}
	return out
}

// Embedded reports whether no Redis server is used.
func (s *Stores) Embedded() bool { return s.Redis == nil }

// Close stops background janitors and closes the Redis client.
func (s *Stores) Close() {
	for _, c := range s.closers {
		c()
	}
}
