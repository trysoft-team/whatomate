package backup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerLock(t *testing.T) {
	dir := t.TempDir()
	_, running := ServerRunning(dir)
	assert.False(t, running)

	require.NoError(t, WriteServerLock(dir))
	_, running = ServerRunning(dir)
	assert.False(t, running, "our own PID does not count")

	if runtime.GOOS != "windows" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte("1\n"), 0o640))
		pid, running := ServerRunning(dir)
		assert.Equal(t, 1, pid)
		assert.True(t, running, "PID 1 is always alive")
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte("999999999\n"), 0o640))
	_, running = ServerRunning(dir)
	assert.False(t, running, "a dead PID is stale")

	require.NoError(t, WriteServerLock(dir))
	RemoveServerLock(dir)
	assert.NoFileExists(t, filepath.Join(dir, lockName))

	require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte("999999999\n"), 0o640))
	RemoveServerLock(dir)
	assert.FileExists(t, filepath.Join(dir, lockName), "a lock written by another PID stays")
}

func TestClaimServerLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	claimed, other, err := ClaimServerLock(dir)
	require.NoError(t, err)
	assert.True(t, claimed, "a free folder is claimed")
	assert.Zero(t, other)
	pid, ok := readLock(dir)
	assert.True(t, ok)
	assert.Equal(t, os.Getpid(), pid)

	require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte("999999999\n"), 0o640))
	claimed, _, err = ClaimServerLock(dir)
	require.NoError(t, err)
	assert.True(t, claimed, "a stale lock is taken over")

	if runtime.GOOS != "windows" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, lockName), []byte("1\n"), 0o640))
		claimed, other, err = ClaimServerLock(dir)
		require.NoError(t, err)
		assert.False(t, claimed, "a live process keeps its lock")
		assert.Equal(t, 1, other)
		pid, _ = readLock(dir)
		assert.Equal(t, 1, pid, "the other process's lock is unchanged")
	}
}
