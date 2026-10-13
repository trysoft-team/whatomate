package backup

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// restoreFiles puts back each area's files. Everything happens inside the
// area folder (staging and old-files folders are its children), so it also
// works when the folder is a mount point.
func restoreFiles(ctx context.Context, zr *zip.Reader, areas []FileArea, skip []string, progress ProgressFunc) error {
	for _, a := range areas {
		if err := restoreArea(ctx, zr, a, skip, progress); err != nil {
			return fmt.Errorf("%s: %w", a.Dir, err)
		}
	}
	return nil
}

func restoreArea(ctx context.Context, zr *zip.Reader, a FileArea, skip []string, progress ProgressFunc) error {
	if err := os.MkdirAll(a.Dir, 0o750); err != nil {
		return err
	}
	root, err := realPath(a.Dir)
	if err != nil {
		return err
	}
	var skipReal []string
	for _, s := range skip {
		if p, err := realPath(s); err == nil {
			skipReal = append(skipReal, p)
		}
	}
	// Names left in place must not be replaced by staged entries either.
	kept := func(name string) bool {
		return isWorkDir(name) || skipped(filepath.Join(root, name), skipReal)
	}
	stage, err := os.MkdirTemp(root, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	var n int64
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "files/") {
			continue
		}
		area, rel, ok := splitFileEntry(f.Name)
		if !ok {
			return fmt.Errorf("unsafe file name %q", f.Name)
		}
		if area != a.Name || rel == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := extract(f, filepath.Join(stage, filepath.FromSlash(rel))); err != nil {
			return err
		}
		n++
		progress(Progress{Phase: PhaseSwap, Table: a.Name, Done: n})
	}

	old, err := os.MkdirTemp(root, ".old-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(old)
	current, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var moved []string
	for _, e := range current {
		if kept(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(root, e.Name()), filepath.Join(old, e.Name())); err != nil {
			putBack(root, old, moved)
			return err
		}
		moved = append(moved, e.Name())
	}
	staged, err := os.ReadDir(stage)
	if err != nil {
		putBack(root, old, moved)
		return err
	}
	var placed []string
	for _, e := range staged {
		if kept(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(stage, e.Name()), filepath.Join(root, e.Name())); err != nil {
			putBack(stage, root, placed)
			putBack(root, old, moved)
			return fmt.Errorf("move %s into place: %w", e.Name(), err)
		}
		placed = append(placed, e.Name())
	}
	return nil
}

// putBack undoes moving names from root into old.
func putBack(root, old string, names []string) {
	for _, name := range names {
		_ = os.Rename(filepath.Join(old, name), filepath.Join(root, name))
	}
}

func extract(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
