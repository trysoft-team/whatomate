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
