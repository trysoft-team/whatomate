package websocket

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/zerodha/logf"
)

func TestHub_CloseAll(t *testing.T) {
	h := NewHub(logf.New(logf.Opts{}))
	c := &Client{hub: h, send: make(chan []byte, 1), userID: uuid.New(), organizationID: uuid.New()}
	h.registerClient(c)
	assert.Equal(t, 1, h.GetClientCount())
	h.CloseAll()
	assert.Equal(t, 0, h.GetClientCount())
	_, open := <-c.send
	assert.False(t, open)
	h.unregisterClient(c) // a late unregister must not panic
}
