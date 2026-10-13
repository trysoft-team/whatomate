package netutil

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	assert.False(t, PortFree(port))
	require.NoError(t, ln.Close())
	assert.True(t, PortFree(port))
}
