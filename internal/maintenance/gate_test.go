package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGate(t *testing.T) {
	var nilGate *Gate
	assert.False(t, nilGate.Active())
	nilGate.Set(true)
	assert.NoError(t, nilGate.Wait(context.Background()))

	g := &Gate{}
	g.Set(true)
	assert.True(t, g.Active())
	go func() { time.Sleep(50 * time.Millisecond); g.Set(false) }()
	assert.NoError(t, g.Wait(context.Background()))

	g.Set(true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, g.Wait(ctx), context.DeadlineExceeded)
}
