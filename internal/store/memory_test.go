package store_test

import (
	"testing"

	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/internal/store/storetest"
)

func newMemory(t *testing.T) *store.Memory {
	m := store.NewMemory()
	t.Cleanup(m.Close)
	return m
}

func TestMemoryKV(t *testing.T) {
	storetest.RunKV(t, func(t *testing.T) store.KV { return newMemory(t) })
}

func TestMemoryCounter(t *testing.T) {
	storetest.RunCounter(t, func(t *testing.T) store.Counter { return newMemory(t) })
}
