package backup

import (
	"fmt"

	"github.com/trysoft/whatomate/internal/diskspace"
)

// EnsureSpace returns an error when the disk holding dir has less than need
// bytes free (plus a 64 MB margin). An unreadable disk is not an error.
func EnsureSpace(dir string, need int64) error {
	free, err := diskspace.Free(dir)
	if err != nil {
		return nil
	}
	const margin = 64 << 20
	if int64(free) < need+margin {
		return fmt.Errorf("not enough disk space in %s: %d MB free, about %d MB needed", dir, free>>20, (need+margin)>>20)
	}
	return nil
}
