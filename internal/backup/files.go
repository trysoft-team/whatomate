package backup

import (
	"archive/zip"
	"context"
	"errors"
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
	defer func() { _ = os.RemoveAll(stage) }()

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
	keepOld := false
	defer func() {
		if !keepOld {
			_ = os.RemoveAll(old)
		}
	}()
	var moved []string
	undo := func(err error) error {
		if perr := putBack(root, old, moved); perr != nil {
			keepOld = true
			return fmt.Errorf("%w; the original files are left in %s: %v", err, old, perr)
		}
		return err
	}
	current, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range current {
		if kept(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(root, e.Name()), filepath.Join(old, e.Name())); err != nil {
			return undo(err)
		}
		moved = append(moved, e.Name())
	}
	staged, err := os.ReadDir(stage)
	if err != nil {
		return undo(err)
	}
	var placed []string
	for _, e := range staged {
		if kept(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(stage, e.Name()), filepath.Join(root, e.Name())); err != nil {
			_ = putBack(stage, root, placed)
			return undo(fmt.Errorf("move %s into place: %w", e.Name(), err))
		}
		placed = append(placed, e.Name())
	}
	return nil
}

// putBack moves names from src back into dst.
func putBack(dst, src string, names []string) error {
	var errs []error
	for _, name := range names {
		if err := os.Rename(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// LeftoverWorkDirs returns the staging and old-files folders an interrupted
// restore left directly inside the areas.
func LeftoverWorkDirs(areas []FileArea) []string {
	var out []string
	for _, a := range areas {
		entries, err := os.ReadDir(a.Dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && isWorkDir(e.Name()) {
				out = append(out, filepath.Join(a.Dir, e.Name()))
			}
		}
	}
	return out
}

// checkSkips rejects a skip path nested below an area's direct children,
// because restoring the area would move it away with its parent.
func checkSkips(areas []FileArea, skip []string) error {
	for _, a := range areas {
		root, err := realPath(a.Dir)
		if err != nil {
			return err
		}
		for _, s := range skip {
			p, err := realPath(s)
			if err != nil {
				return err
			}
			if p == root || (within(p, root) && filepath.Dir(p) != root) {
				return fmt.Errorf("%s must be directly inside the %s folder (%s) or outside it, because a restore leaves it alone", s, a.Name, a.Dir)
			}
		}
	}
	return nil
}

func extract(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
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
