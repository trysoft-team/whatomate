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
}
