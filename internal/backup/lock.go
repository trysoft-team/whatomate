package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const lockName = "server.lock"

// WriteServerLock records that a server using this backup folder is running.
func WriteServerLock(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, lockName), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o640)
}

// RemoveServerLock removes the lock if this process wrote it.
func RemoveServerLock(dir string) {
	if pid, ok := readLock(dir); ok && pid == os.Getpid() {
		_ = os.Remove(filepath.Join(dir, lockName))
	}
}

// ServerRunning reports whether another live process holds the lock.
func ServerRunning(dir string) (int, bool) {
	pid, ok := readLock(dir)
	if !ok || pid == os.Getpid() {
		return pid, false
	}
	return pid, processAlive(pid)
}

func readLock(dir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid, err == nil && pid > 0
}
