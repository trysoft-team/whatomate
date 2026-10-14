package netutil

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPortFree(t *testing.T) {
	t.Run("wildcard listener", func(t *testing.T) {
		ln, err := net.Listen("tcp", ":0")
		require.NoError(t, err)
		port := ln.Addr().(*net.TCPAddr).Port
		assert.False(t, PortFree(port))
		require.NoError(t, ln.Close())
		assert.True(t, PortFree(port))
	})

	t.Run("IPv4 loopback-only listener", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = ln.Close() }()
		assert.False(t, PortFree(ln.Addr().(*net.TCPAddr).Port))
	})

	t.Run("IPv4-only wildcard listener", func(t *testing.T) {
		ln, err := net.Listen("tcp4", "0.0.0.0:0")
		require.NoError(t, err)
		defer func() { _ = ln.Close() }()
		assert.False(t, PortFree(ln.Addr().(*net.TCPAddr).Port))
	})

	t.Run("IPv6-only wildcard listener", func(t *testing.T) {
		ln, err := net.Listen("tcp6", "[::]:0")
		if err != nil {
			t.Skip("IPv6 unavailable: " + err.Error())
		}
		defer func() { _ = ln.Close() }()
		assert.False(t, PortFree(ln.Addr().(*net.TCPAddr).Port))
	})

	t.Run("IPv6 loopback-only listener", func(t *testing.T) {
		ln, err := net.Listen("tcp", "[::1]:0")
		if err != nil {
			t.Skip("IPv6 loopback unavailable: " + err.Error())
		}
		defer func() { _ = ln.Close() }()
		assert.False(t, PortFree(ln.Addr().(*net.TCPAddr).Port))
	})
}
