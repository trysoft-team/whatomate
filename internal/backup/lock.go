package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const lockName = "server.lock"

// LockOwner is the process recorded in a server lock. Host is empty in locks
// written by versions that only recorded the PID.
type LockOwner struct {
	PID  int
	Host string
}

// FromOtherHost reports whether the lock was written on another host, such as
// a container that has since been recreated.
func (o LockOwner) FromOtherHost() bool {
	return o.Host != "" && o.Host != hostname()
}

// written holds the backup folders whose lock this process wrote. In Docker
// every container's main process is PID 1, so a PID alone does not tell
// another container's lock from our own.
var written sync.Map

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func lockKey(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func me() LockOwner { return LockOwner{PID: os.Getpid(), Host: hostname()} }

// WriteServerLock records that a server using this backup folder is running.
func WriteServerLock(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	m := me()
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte(fmt.Sprintf("%d\n%s\n", m.PID, m.Host)), 0o640); err != nil {
		return err
	}
	written.Store(lockKey(dir), true)
	return nil
}

// ownLock reports whether this process wrote the lock now in dir.
func ownLock(dir string, o LockOwner) bool {
	_, ok := written.Load(lockKey(dir))
	return ok && o == me()
}

// RemoveServerLock removes the lock if this process wrote it.
func RemoveServerLock(dir string) {
	if o, ok := readLock(dir); ok && ownLock(dir, o) {
		_ = os.Remove(filepath.Join(dir, lockName))
		written.Delete(lockKey(dir))
	}
}

// ClaimServerLock is used at server start. It writes the lock unless another
// live process on this host holds it, in which case it returns that process
// and leaves the lock alone. A lock from another host is taken over and its
// owner returned, since container hostnames change when they are recreated.
func ClaimServerLock(dir string) (claimed bool, other LockOwner, err error) {
	o, ok := readLock(dir)
	if ok && !ownLock(dir, o) {
		other = o
		if !o.FromOtherHost() && o.PID != os.Getpid() && processAlive(o.PID) {
			return false, o, nil
		}
	}
	if err := WriteServerLock(dir); err != nil {
		return false, other, err
	}
	return true, other, nil
}

// ClaimCLILock is used by the backup and restore commands. It writes the lock
// unless a server may be running, in which case it returns that lock's owner.
func ClaimCLILock(dir string) (claimed bool, other LockOwner, err error) {
	if o, running := ServerRunning(dir); running {
		return false, o, nil
	}
	if err := WriteServerLock(dir); err != nil {
		return false, LockOwner{}, err
	}
	return true, LockOwner{}, nil
}

// ServerRunning reports whether a lock this process did not write may belong
// to a live process: one from another host, or with our own PID, cannot be
// checked and counts as live.
func ServerRunning(dir string) (LockOwner, bool) {
	o, ok := readLock(dir)
	if !ok || ownLock(dir, o) {
		return o, false
	}
	if o.FromOtherHost() || o.PID == os.Getpid() {
		return o, true
	}
	return o, processAlive(o.PID)
}

func readLock(dir string) (LockOwner, bool) {
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		return LockOwner{}, false
	}
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return LockOwner{}, false
	}
	o := LockOwner{PID: pid}
	if len(lines) == 2 {
		o.Host = strings.TrimSpace(lines[1])
	}
	return o, true
}
