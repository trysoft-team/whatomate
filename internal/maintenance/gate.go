// Package maintenance holds the switch that pauses the app during a restore.
package maintenance

import (
	"context"
	"sync/atomic"
	"time"
)

// Gate is on while a restore replaces the data. A nil Gate is always off.
type Gate struct{ on atomic.Bool }

// Active reports whether maintenance mode is on.
func (g *Gate) Active() bool { return g != nil && g.on.Load() }

// Set turns maintenance mode on or off.
func (g *Gate) Set(on bool) {
	if g != nil {
		g.on.Store(on)
	}
}

// Wait blocks while maintenance mode is on, or until ctx is done.
func (g *Gate) Wait(ctx context.Context) error {
	for g.Active() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil
}
