// Package defaultaudio carries the hold music and ringback tone shipped with
// whatomate and writes them into the calling audio folder.
package defaultaudio

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// HoldMusicFile and RingbackFile are the bundled files' names.
const (
	HoldMusicFile = "hold_music.opus"
	RingbackFile  = "ringback.opus"
)

//go:embed hold_music.opus ringback.opus
var files embed.FS

// Install writes the bundled files into dir, creating it if needed. Files
// already there are left alone, so a replaced hold track survives upgrades.
// It returns the names it wrote.
func Install(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	var written []string
	for _, name := range []string{HoldMusicFile, RingbackFile} {
		data, err := files.ReadFile(name)
		if err != nil {
			return written, err
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return written, fmt.Errorf("write %s: %w", name, err)
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(f.Name())
			return written, fmt.Errorf("write %s: %w", name, errors.Join(werr, cerr))
		}
		written = append(written, name)
	}
	return written, nil
}
