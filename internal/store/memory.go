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
