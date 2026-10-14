package main

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListenApp_BusyPortNamesConfigFile(t *testing.T) {
	busy, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = busy.Close() }()
	addr := fmt.Sprintf("127.0.0.1:%d", busy.Addr().(*net.TCPAddr).Port)

	_, err = listenApp(addr, "/etc/whatomate/config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), addr)
	assert.Contains(t, err.Error(), "[server] port in /etc/whatomate/config.toml")
}

func TestListenApp_FreePort(t *testing.T) {
	ln, err := listenApp("127.0.0.1:0", "/etc/whatomate/config.toml")
	require.NoError(t, err)
	_ = ln.Close()
}
