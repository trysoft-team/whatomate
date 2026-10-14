// Package diskspace reads free disk space.
package diskspace

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrUnsupported means this platform cannot report free space.
var ErrUnsupported = errors.New("not supported on this system")

// Free returns the bytes available to this user on the disk holding path,
// or on its nearest existing parent.
func Free(path string) (uint64, error) { return free(NearestExisting(path)) }

// NearestExisting returns path or its closest existing parent folder.
func NearestExisting(path string) string {
	p := path
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}
