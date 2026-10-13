//go:build darwin

package setup

import "golang.org/x/sys/unix"

type hostSystem struct{}

func (hostSystem) TotalMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }

func (hostSystem) FreeDisk(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(nearestExisting(path), &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
