package backup

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// List returns the backups in dir, newest first. Files that are not readable
// backups are listed with Error set.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		info := Info{Name: e.Name()}
		if fi, err := e.Info(); err == nil {
			info.Size = fi.Size()
		}
		if m, err := ReadManifest(filepath.Join(dir, e.Name())); err != nil {
			info.Error = err.Error()
		} else {
			info.Manifest = m
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Manifest, out[j].Manifest
		switch {
		case a != nil && b != nil:
			return a.CreatedAt.After(b.CreatedAt)
		case a != nil:
			return true
		case b != nil:
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ReadManifest reads the manifest of the backup at path.
func ReadManifest(path string) (*Manifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer func() { _ = zr.Close() }()
	return readManifest(&zr.Reader)
}

func readManifest(zr *zip.Reader) (*Manifest, error) {
	f := findFile(zr, manifestName)
	if f == nil {
		return nil, fmt.Errorf("%w: no %s", ErrCorrupt, manifestName)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer func() { _ = rc.Close() }()
	var m Manifest
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
	}
	return &m, nil
}

func findFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}
