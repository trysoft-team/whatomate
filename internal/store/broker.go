package store

import (
	"context"
	"sync"
)

// MemoryBroker delivers messages to subscribers in the same process.
type MemoryBroker struct {
	mu   sync.RWMutex
	next int
	subs map[string]map[int]func([]byte)
}

func NewMemoryBroker() *MemoryBroker {
	return &MemoryBroker{subs: map[string]map[int]func([]byte){}}
}

func (b *MemoryBroker) Publish(_ context.Context, channel string, payload []byte) error {
	b.mu.RLock()
	fns := make([]func([]byte), 0, len(b.subs[channel]))
	for _, fn := range b.subs[channel] {
		fns = append(fns, fn)
	}
	b.mu.RUnlock()
	for _, fn := range fns {
		fn(append([]byte(nil), payload...))
	}
	return nil
}

func (b *MemoryBroker) Subscribe(ctx context.Context, channel string, fn func([]byte)) (func(), error) {
	b.mu.Lock()
	id := b.next
	b.next++
	if b.subs[channel] == nil {
		b.subs[channel] = map[int]func([]byte){}
	}
	b.subs[channel][id] = fn
	b.mu.Unlock()

	var once sync.Once
	done := make(chan struct{})
	stop := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs[channel], id)
			b.mu.Unlock()
			close(done)
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	return stop, nil
}
