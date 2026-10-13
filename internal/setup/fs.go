package setup

import (
	"os"
	"path/filepath"
)

// nearestExisting returns path or its closest existing parent folder.
func nearestExisting(path string) string {
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

// Writable reports whether dir, or its nearest existing parent when dir does
// not exist yet, accepts new files. It leaves nothing behind.
func Writable(dir string) bool {
	d := nearestExisting(dir)
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		return false
	}
	f, err := os.CreateTemp(d, ".whatomate-write-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// FreeDisk returns the bytes available to this user on the disk holding path,
// or on its nearest existing parent.
func FreeDisk(path string) (uint64, error) { return hostSystem{}.FreeDisk(path) }
