# Built-in store: run without a Redis server

## Goal

A user downloads one binary, points it at their database (PostgreSQL, MySQL,
SQLite or SQL Server — unchanged) and it runs. No Redis to install.

Redis stays supported as an option for deployments that run several API
servers or separate worker machines.

## Constraints

- Pure Go, `CGO_ENABLED=0`, Go 1.25. No new external services.
- Database drivers and the multi-database layer are not changed; new tables
  follow the existing GORM model + `AutoMigrate` pattern and use the
  `internal/database/dialect` helpers where SQL differs.
- Existing configs that set `[redis] host` keep working exactly as today.

## What Redis does today

| Use | Where | Needs to survive restart? |
|---|---|---|
| Caches (chatbot settings, flows, keywords, AI contexts, SLA settings, WhatsApp accounts, webhooks, tags, permissions, team assignment, IVR flows, calling settings, Meta analytics, WABA currency) | `handlers/cache.go`, `calling/cache.go`, `assignment/cache.go`, `handlers/meta_analytics.go` | No |
| Rate limiting (`INCR` + `EXPIRE`) | `middleware/ratelimit.go` | No |
| Refresh tokens | `handlers/auth.go` | Yes (else everyone is logged out) |
| SSO / Google OAuth / passkey one-time state | `sso.go`, `google_oauth.go`, `passkeys.go` | Short-lived, but login-related |
| WhatsApp Flow endpoint state | `flow_endpoint.go` | Yes |
| Sticky caller → agent routing | `call_webhook.go` | Yes |
| Chatbot delays and agent-session timeouts (sorted sets) | `chat_delay_processor.go` | Yes |
| Campaign job queue (Streams + consumer group) | `queue/redis.go`, `worker/worker.go` | Yes |
| Campaign progress (pub/sub) | `queue/pubsub.go`, `handlers/app.go` | No |
| Readiness check (`PING`) | `handlers/app.go` | — |

## Design

### Mode selection

- `[redis] host` empty (the new default) → **embedded mode**.
- `[redis] host` set → **Redis mode**, identical behaviour to today.

Startup logs which mode is active. `config.example.toml` ships with the
`[redis]` block commented out.

### New package `internal/store`

Four small interfaces, each with an embedded implementation and a Redis
implementation. Call sites depend only on the interfaces.

```go
// KV is a key/value store with per-key expiry.
type KV interface {
    Get(ctx context.Context, key string) ([]byte, error)    // ErrNotFound on miss/expired
    Set(ctx context.Context, key string, val []byte, ttl time.Duration) error // ttl 0 = no expiry
    Del(ctx context.Context, keys ...string) error
    GetDel(ctx context.Context, key string) ([]byte, error) // single-use read
    DelPrefix(ctx context.Context, prefix string) error
    Ping(ctx context.Context) error
}

// Counter is a fixed-window counter for rate limiting.
type Counter interface {
    Incr(ctx context.Context, key string, window time.Duration) (count int64, resetIn time.Duration, err error)
}

// Scheduler holds members due at a time (replaces the delay sorted sets).
type Scheduler interface {
    Schedule(ctx context.Context, set, member string, at time.Time) error // replaces existing due time
    Cancel(ctx context.Context, set string, members ...string) error
    PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error) // each member returned to exactly one caller
}

// Broker carries campaign progress from workers to the API.
type Broker interface {
    Publish(ctx context.Context, channel string, payload []byte) error
    Subscribe(ctx context.Context, channel string, fn func([]byte)) (stop func(), err error)
}
```

`store.Stores` bundles them and is built once in `main.go`:

| Field | Embedded mode | Redis mode |
|---|---|---|
| `Cache KV` | memory | Redis |
| `State KV` | database table `kv_entries` | Redis |
| `Counter` | memory | Redis |
| `Scheduler` | database table `scheduled_items` | Redis sorted sets |
| `Broker` | in-process channels | Redis pub/sub |
| `Queue` / `Consumer` | database table `campaign_jobs` | Redis Streams (existing code) |

Which KV a call site uses:

- `Cache`: everything in the caches row above.
- `State`: refresh tokens, SSO / Google OAuth / passkey state, WhatsApp Flow
  endpoint state, sticky call routing.

Key names stay the same as today, so Redis mode is a drop-in.

### Embedded implementations

**Memory KV and Counter** — a `map` guarded by a `sync.RWMutex`, entries carry
an expiry; reads ignore expired entries and a janitor goroutine removes them
every minute. `DelPrefix` walks the map. No size limit: keys are per
organisation/account plus rate-limit keys that expire within their window.

**Database KV (`kv_entries`)**

| Column | Type |
|---|---|
| `key` | string(255), primary key |
| `value` | text / blob |
| `expires_at` | nullable time, indexed |

- `Set` is an upsert (`clause.OnConflict{UpdateAll: true}`; works on all four
  dialects).
- `Get` treats an expired row as missing.
- `GetDel` deletes by key and only returns the value when `RowsAffected == 1`,
  so a token can be consumed once even with concurrent requests.
- `DelPrefix` uses `key LIKE prefix%` with the prefix escaped.
- A janitor deletes expired rows every 10 minutes.

**Database Scheduler (`scheduled_items`)**

| Column | Type |
|---|---|
| `set_name` + `member` | composite primary key |
| `due_at` | time, indexed |

`PopDue` selects up to `limit` due rows ordered by `due_at`, then deletes each
one and keeps it only when `RowsAffected == 1` — the same "remove first so
concurrent pollers don't double-resume" rule the Redis code uses today. The
1-second `ChatDelayProcessor` loop is unchanged apart from calling the
interface.

**Database queue (`campaign_jobs`)** implementing the existing `queue.Queue`
and `queue.Consumer` interfaces:

| Column | Type |
|---|---|
| `id` | auto-increment primary key |
| `job_type` | string |
| `payload` | text (the same JSON as today) |
| `locked_by` | string, nullable |
| `locked_until` | time, nullable, indexed |
| `created_at` | time |

- `EnqueueRecipients` inserts with `CreateInBatches` and wakes local consumers
  through a channel, so sending starts immediately instead of on the next poll.
- A consumer claims the oldest available job with a conditional
  `UPDATE … SET locked_by, locked_until = now + 5m WHERE id = ? AND
  (locked_until IS NULL OR locked_until < now)` and only proceeds on
  `RowsAffected == 1`. No `SKIP LOCKED` needed, so it works on SQLite and SQL
  Server too.
- Success deletes the row (ack). A handler error leaves the row locked, so it
  is retried after 5 minutes — the same as today's `XCLAIM` with
  `ClaimMinIdleTime`. A crashed worker's jobs are picked up the same way.
- When idle, consumers wait on the wake channel or a 1-second poll.

**In-process Broker** — a map of channel name → subscriber callbacks.

### Separate worker machines

`whatomate worker` without Redis still processes campaigns through the
database queue. Live campaign progress over WebSocket is in-process only, so
on that setup the UI is not pushed progress updates; reloading the campaign
page shows the current counts from the database. Deployments that need live progress from separate workers,
or several API servers sharing caches and rate limits, set `[redis] host`.

### Readiness check

`/ready` pings `State` (database in embedded mode, Redis in Redis mode)
instead of Redis directly.

### Switching modes

Moving an existing install from Redis to embedded mode loses only what was in
Redis at that moment: users sign in again, and campaign jobs still queued in
Redis are not moved. Those recipients stay `pending` in the database, so
pausing and starting the campaign again re-queues them (`StartCampaign`
enqueues every pending recipient). This is called out in the docs.

## Code changes

- `internal/store`: interfaces, memory, database and Redis implementations,
  `New(cfg, db, log) (*Stores, error)`.
- `internal/queue`: add the database queue/consumer; Redis queue unchanged;
  pub/sub moves behind `store.Broker`.
- `internal/models`: `KVEntry`, `ScheduledItem`, `CampaignJob`, registered in
  `GetMigrationModels`.
- `handlers.App`: `Redis *redis.Client` is replaced by `Cache`, `State`,
  `Scheduler`, `Broker`. Call sites switch to the interfaces; the
  `if a.Redis != nil` branches go away because a store always exists.
- `calling.Manager`, `assignment.Assigner`, `worker.Worker`,
  `middleware.RateLimitOpts`: take the interfaces instead of `*redis.Client`.
- `cmd/whatomate/main.go`: build `store.Stores` instead of `database.NewRedis`;
  no Redis connection unless `[redis] host` is set.
- `config.example.toml`, `docs/.../configuration.mdx`: `[redis]` documented as
  optional. `docker-compose.yml`: the Redis service and the app's
  `depends_on: redis` are removed (users who want Redis can add it back and set
  `[redis] host`).

## Testing

- One conformance test suite per interface, run against memory, database
  (SQLite in-memory always; PostgreSQL `whatomate_test` when available) and
  Redis (when `TEST_REDIS_URL` is set). Covers expiry, `GetDel` single use
  under concurrency, `DelPrefix`, `PopDue` exactly-once under concurrent
  pollers, queue claim exclusivity across two consumers, retry after lock
  expiry, and ack.
- Handler and middleware tests that need Redis today switch to memory/SQLite
  stores, so `go test ./...` no longer needs a Redis server. Redis-specific
  tests (`queue/queue_test.go`, `queue/redis_gaps_test.go`,
  `database/redis_test.go`) still run when `TEST_REDIS_URL` is set.
- Manual check: start with no `[redis]` section and no Redis running; log in,
  restart, confirm still logged in; send a small campaign; trigger a chatbot
  delay node across a restart; place an AI call.

## Out of scope

- Changes to the database drivers or the multi-database layer.
- Migrating live data from Redis into the new tables.
- Cross-process cache invalidation without Redis (only one API process is
  supported in embedded mode).
