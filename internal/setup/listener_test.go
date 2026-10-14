package setup

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serveOnce(ln net.Listener, msg string) {
	c, err := ln.Accept()
	if err != nil {
		return
	}
	_, _ = c.Write([]byte(msg))
	_ = c.Close()
}

func dialRead(t *testing.T, addr net.Addr) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	b, err := io.ReadAll(c)
	require.NoError(t, err)
	return string(b)
}

func TestShared_ViewsTakeTurns(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := Share(raw)
	defer func() { _ = s.Close() }()

	first := s.View(false)
	go serveOnce(first, "first")
	assert.Equal(t, "first", dialRead(t, raw.Addr()))
	require.NoError(t, first.Close())

	second := s.View(true)
	go serveOnce(second, "second")
	assert.Equal(t, "second", dialRead(t, raw.Addr()))
	require.NoError(t, second.Close())

	_, err = net.DialTimeout("tcp", raw.Addr().String(), time.Second)
	assert.Error(t, err, "closing the owner view closes the socket")
}

func TestShared_ConnectionWaitsForNextView(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := Share(raw)
	defer func() { _ = s.Close() }()
	require.NoError(t, s.View(false).Close())

	go func() {
		time.Sleep(200 * time.Millisecond)
		serveOnce(s.View(true), "late")
	}()
	assert.Equal(t, "late", dialRead(t, raw.Addr()), "a connection made between views is served by the next one")
}

func TestShared_ClosedViewStopsAccepting(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := Share(raw)
	defer func() { _ = s.Close() }()
	v := s.View(false)
	done := make(chan error, 1)
	go func() {
		_, err := v.Accept()
		done <- err
	}()
	require.NoError(t, v.Close())
	select {
	case err := <-done:
		assert.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return after Close")
	}
	assert.Equal(t, raw.Addr(), v.Addr())
}

type failingListener struct{ err error }

func (l failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (l failingListener) Close() error              { return nil }
func (l failingListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func TestShared_AcceptErrorReachesViews(t *testing.T) {
	boom := errors.New("boom")
	s := Share(failingListener{err: boom})
	defer func() { _ = s.Close() }()
	v := s.View(true)
	_, err := v.Accept()
	assert.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, net.ErrClosed)
}

func TestShared_CloseWakesBlockedAccept(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := Share(raw)
	v := s.View(false)
	done := make(chan error, 1)
	go func() {
		_, err := v.Accept()
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, s.Close())
	select {
	case err := <-done:
		assert.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return after Shared.Close")
	}
}
