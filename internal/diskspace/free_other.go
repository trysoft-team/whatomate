//go:build !linux && !darwin && !windows

package diskspace

func free(string) (uint64, error) { return 0, ErrUnsupported }
