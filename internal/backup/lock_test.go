package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeLockFile(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte(content), 0o640))
}

func myHost(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	require.NoError(t, err)
	return h
}

func TestServerLock(t *testing.T) {
	dir := t.TempDir()
	_, running := ServerRunning(dir)
	assert.False(t, running)

	require.NoError(t, WriteServerLock(dir))
	_, running = ServerRunning(dir)
	assert.False(t, running, "our own lock does not count")
	owner, ok := readLock(dir)
	require.True(t, ok)
	assert.Equal(t, LockOwner{PID: os.Getpid(), Host: myHost(t)}, owner, "the lock records PID and hostname")

	if runtime.GOOS != "windows" {
		writeLockFile(t, dir, "1\n")
		other, running := ServerRunning(dir)
		assert.Equal(t, 1, other.PID)
		assert.True(t, running, "PID 1 is always alive")
	}

	writeLockFile(t, dir, "999999999\n")
	_, running = ServerRunning(dir)
	assert.False(t, running, "a dead PID is stale")

	require.NoError(t, WriteServerLock(dir))
	RemoveServerLock(dir)
	assert.NoFileExists(t, filepath.Join(dir, lockName))

	writeLockFile(t, dir, "999999999\n")
	RemoveServerLock(dir)
	assert.FileExists(t, filepath.Join(dir, lockName), "a lock written by another PID stays")
}

func TestServerLock_OtherHost(t *testing.T) {
	dir := t.TempDir()
	writeLockFile(t, dir, "999999999\nsome-other-container\n")
	other, running := ServerRunning(dir)
	assert.True(t, running, "a lock from another host may belong to a live server")
	assert.Equal(t, LockOwner{PID: 999999999, Host: "some-other-container"}, other)

	writeLockFile(t, dir, fmt.Sprintf("%d\nsome-other-container\n", os.Getpid()))
	_, running = ServerRunning(dir)
	assert.True(t, running, "our PID on another host is someone else")
	RemoveServerLock(dir)
	assert.FileExists(t, filepath.Join(dir, lockName), "a lock from another host stays")

	claimed, other, err := ClaimServerLock(dir)
	require.NoError(t, err)
	assert.True(t, claimed, "a server takes over a lock from another host")
	assert.Equal(t, "some-other-container", other.Host)
	owner, _ := readLock(dir)
	assert.Equal(t, LockOwner{PID: os.Getpid(), Host: myHost(t)}, owner)
}

func TestServerLock_OwnPIDNotWrittenByUs(t *testing.T) {
	dir := t.TempDir()
	writeLockFile(t, dir, fmt.Sprintf("%d\n%s\n", os.Getpid(), myHost(t)))
	_, running := ServerRunning(dir)
	assert.True(t, running, "a container sharing our hostname and PID may hold it")
	RemoveServerLock(dir)
	assert.FileExists(t, filepath.Join(dir, lockName), "only a lock this process wrote is removed")

	claimed, _, err := ClaimCLILock(dir)
	require.NoError(t, err)
	assert.False(t, claimed, "a command does not take it over")

	claimed, _, err = ClaimServerLock(dir)
	require.NoError(t, err)
	assert.True(t, claimed, "a server on the same host with our PID is ourselves restarted")
}

func TestServerLock_LegacyPIDOnly(t *testing.T) {
	dir := t.TempDir()
	writeLockFile(t, dir, fmt.Sprintf("%d\n", os.Getpid()))
	owner, ok := readLock(dir)
	require.True(t, ok)
	assert.Equal(t, LockOwner{PID: os.Getpid()}, owner)
	_, running := ServerRunning(dir)
	assert.True(t, running, "an old lock with our PID was written by another process")

	writeLockFile(t, dir, "999999999\n")
	claimed, _, err := ClaimCLILock(dir)
	require.NoError(t, err)
	assert.True(t, claimed, "an old lock with a dead PID is stale")
	RemoveServerLock(dir)
	assert.NoFileExists(t, filepath.Join(dir, lockName))
}

func TestClaimServerLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	claimed, other, err := ClaimServerLock(dir)
	require.NoError(t, err)
	assert.True(t, claimed, "a free folder is claimed")
	assert.Zero(t, other)
	owner, ok := readLock(dir)
	assert.True(t, ok)
	assert.Equal(t, os.Getpid(), owner.PID)

	writeLockFile(t, dir, "999999999\n")
	claimed, _, err = ClaimServerLock(dir)
	require.NoError(t, err)
	assert.True(t, claimed, "a stale lock is taken over")

	if runtime.GOOS != "windows" {
		writeLockFile(t, dir, "1\n"+myHost(t)+"\n")
		claimed, other, err = ClaimServerLock(dir)
		require.NoError(t, err)
		assert.False(t, claimed, "a live process keeps its lock")
		assert.Equal(t, 1, other.PID)
		owner, _ = readLock(dir)
		assert.Equal(t, 1, owner.PID, "the other process's lock is unchanged")

		claimed, _, err = ClaimCLILock(dir)
		require.NoError(t, err)
		assert.False(t, claimed)
	}
}
