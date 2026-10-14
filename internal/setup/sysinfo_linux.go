//go:build linux

package setup

import (
	"github.com/trysoft/whatomate/internal/diskspace"
	"golang.org/x/sys/unix"
)

type hostSystem struct{}

func (hostSystem) TotalMemory() (uint64, error) {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0, err
	}
	return uint64(si.Totalram) * uint64(si.Unit), nil
}

func (hostSystem) FreeDisk(path string) (uint64, error) { return diskspace.Free(path) }
