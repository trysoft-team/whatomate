//go:build !linux && !darwin && !windows

package setup

import "github.com/trysoft/whatomate/internal/diskspace"

type hostSystem struct{}

func (hostSystem) TotalMemory() (uint64, error)         { return 0, diskspace.ErrUnsupported }
func (hostSystem) FreeDisk(path string) (uint64, error) { return diskspace.Free(path) }
