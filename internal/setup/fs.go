package setup

import (
	"os"

	"github.com/trysoft/whatomate/internal/diskspace"
)

// Writable reports whether dir, or its nearest existing parent when dir does
// not exist yet, accepts new files. It leaves nothing behind.
func Writable(dir string) bool {
	d := diskspace.NearestExisting(dir)
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
