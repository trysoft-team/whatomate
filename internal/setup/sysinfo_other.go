//go:build !linux && !darwin && !windows

package setup

import "errors"

type hostSystem struct{}

var errUnsupported = errors.New("not supported on this system")

func (hostSystem) TotalMemory() (uint64, error)    { return 0, errUnsupported }
func (hostSystem) FreeDisk(string) (uint64, error) { return 0, errUnsupported }
