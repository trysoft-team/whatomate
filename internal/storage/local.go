package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalFS stores objects under Root using slash-separated keys
// (e.g. recordings/{org}/{id}.ogg).
type LocalFS struct {
	Root string
}

// NewLocalFS returns a filesystem store. Empty root defaults to ./uploads.
func NewLocalFS(root string) *LocalFS {
	if root == "" {
		root = "./uploads"
	}
	return &LocalFS{Root: root}
}

// Upload writes body to Root/key, creating parent directories as needed.
func (l *LocalFS) Upload(_ context.Context, key string, body io.Reader, _ string) error {
	dest, err := l.Resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("create recording directory: %w", err)
	}

	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create recording file: %w", err)
	}
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write recording file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close recording file: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("finalize recording file: %w", err)
	}
	return nil
}

// Resolve maps a storage key to an absolute path under Root.
// Rejects empty, absolute, and traversing keys.
func (l *LocalFS) Resolve(key string) (string, error) {
	if key == "" || filepath.IsAbs(key) || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid storage key")
	}
	root, err := filepath.Abs(l.Root)
	if err != nil {
		return "", fmt.Errorf("resolve storage root: %w", err)
	}
	dest, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(key)))
	if err != nil {
		return "", fmt.Errorf("resolve storage key: %w", err)
	}
	prefix := root + string(os.PathSeparator)
	if dest != root && !strings.HasPrefix(dest, prefix) {
		return "", fmt.Errorf("invalid storage key")
	}
	return dest, nil
}
