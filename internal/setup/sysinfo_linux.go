//go:build linux

package setup

import "golang.org/x/sys/unix"

type hostSystem struct{}

func (hostSystem) TotalMemory() (uint64, error) {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0, err
	}
	return uint64(si.Totalram) * uint64(si.Unit), nil
}

func (hostSystem) FreeDisk(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(nearestExisting(path), &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
