package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	tempPrefix = ".tmp-"
	// UploadPrefix starts the names of partly uploaded backups.
	UploadPrefix = ".upload-"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}\.zip$`)

// ValidName reports whether name is a backup file name the API accepts: a
// plain file name ending in .zip, without path separators or "..".
func ValidName(name string) bool {
	return nameRE.MatchString(name) && !strings.Contains(name, "..")
}

func newName(dir, kind string, at time.Time) string {
	prefix := "whatomate-backup-"
	if kind == KindPreRestore {
		prefix = "whatomate-pre-restore-"
	}
	return freeName(dir, prefix+at.UTC().Format("20060102T150405Z"))
}

// UploadName turns an uploaded file's name into a free, valid name in dir.
func UploadName(dir, original string) string {
	base := filepath.Base(strings.ReplaceAll(original, `\`, "/"))
	if strings.EqualFold(filepath.Ext(base), ".zip") {
		base = base[:len(base)-4]
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	clean := strings.Trim(b.String(), "-_")
	for strings.Contains(clean, "--") {
		clean = strings.ReplaceAll(clean, "--", "-")
	}
	if clean == "" {
		clean = "uploaded-backup"
	}
	if len(clean) > 150 {
		clean = clean[:150]
	}
	return freeName(dir, clean)
}

func freeName(dir, base string) string {
	name := base + ".zip"
	for i := 2; exists(filepath.Join(dir, name)); i++ {
		name = fmt.Sprintf("%s-%d.zip", base, i)
	}
	return name
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return !errors.Is(err, fs.ErrNotExist)
}

// CleanTemp removes unfinished backups and uploads left by a crash.
func CleanTemp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) || strings.HasPrefix(e.Name(), UploadPrefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
