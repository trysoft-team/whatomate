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

// GetDel runs GET and DEL in one MULTI/EXEC rather than GETDEL, which needs
// Redis 6.2+. Only the caller whose DEL removed the key gets the value.
func (r *Redis) GetDel(ctx context.Context, key string) ([]byte, error) {
	var get *redis.StringCmd
	var del *redis.IntCmd
	_, err := r.c.TxPipelined(ctx, func(p redis.Pipeliner) error {
		get = p.Get(ctx, key)
		del = p.Del(ctx, key)
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	if del.Val() != 1 {
		return nil, ErrNotFound
	}
	return get.Bytes()
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

// incrScript starts the window on the first hit and also on a key left
// without a TTL, which would otherwise count forever.
var incrScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if n == 1 or ttl == -1 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
	ttl = tonumber(ARGV[1])
end
return {n, ttl}
`)

func (r *Redis) Incr(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	res, err := incrScript.Run(ctx, r.c, []string{key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	return res[0], time.Duration(res[1]) * time.Millisecond, nil
}

// Schedule stores at as whole Unix seconds, the score format existing Redis
// deployments already hold, so due times have one-second granularity.
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

// PopDue compares due times with now at one-second granularity. Each item is
// claimed by the caller whose ZREM removes it.
func (r *Redis) PopDue(ctx context.Context, set string, now time.Time, limit int) ([]string, error) {
	// go-redis omits LIMIT when Count is 0, which would return every due item.
	if limit <= 0 {
		return nil, nil
	}
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
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
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
