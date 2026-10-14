//go:build darwin

package setup

import (
	"github.com/trysoft/whatomate/internal/diskspace"
	"golang.org/x/sys/unix"
)

type hostSystem struct{}

func (hostSystem) TotalMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }

func (hostSystem) FreeDisk(path string) (uint64, error) { return diskspace.Free(path) }
