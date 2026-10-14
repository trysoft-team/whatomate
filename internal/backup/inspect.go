package backup

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	// ErrCorrupt means the file is not a readable, intact backup.
	ErrCorrupt = errors.New("backup file is damaged")
	// ErrNewer means the backup holds data this version does not know.
	ErrNewer = errors.New("backup was made by a newer whatomate")
)

// Summary is what a restore would do, shown before it starts.
type Summary struct {
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	Manifest     Manifest `json:"manifest"`
	TargetDriver string   `json:"target_driver"`
	// KeyMatches is false when the backup's secrets were encrypted with a
	// different encryption key than this server's.
	KeyMatches bool `json:"key_matches"`
}

// Inspect reads the manifest and checks the backup fits this version's
// tables. It does not read the data; Verify does.
func Inspect(p string, db *gorm.DB, encryptionKey string) (Summary, error) {
	st, err := os.Stat(p)
	if err != nil {
		return Summary{}, err
	}
	m, err := ReadManifest(p)
	if err != nil {
		return Summary{}, err
	}
	if m.Format > FormatVersion {
		return Summary{}, fmt.Errorf("%w (%s, format %d); upgrade this server first", ErrNewer, m.AppVersion, m.Format)
	}
	tbls, err := backupTables(db)
	if err != nil {
		return Summary{}, err
	}
	known := map[string]table{}
	for _, t := range tbls {
		known[t.Name] = t
	}
	for _, te := range m.Tables {
		t, ok := known[te.Name]
		if !ok {
			return Summary{}, fmt.Errorf("%w (%s): unknown table %s; upgrade this server first", ErrNewer, m.AppVersion, te.Name)
		}
		cols := columnsByName(t)
		for _, c := range te.Columns {
			if _, ok := cols[c]; !ok {
				return Summary{}, fmt.Errorf("%w (%s): unknown column %s.%s; upgrade this server first", ErrNewer, m.AppVersion, te.Name, c)
			}
		}
	}
	return Summary{
		Name: filepath.Base(p), Size: st.Size(), Manifest: *m, TargetDriver: db.Name(),
		KeyMatches: m.KeyFingerprint == "" || m.KeyFingerprint == KeyFingerprint(encryptionKey),
	}, nil
}

// InspectFile is Inspect without a live database, for a restore target of
// targetDriver that may not exist yet. Table and column names do not depend
// on the driver.
func InspectFile(p, targetDriver, encryptionKey string) (Summary, error) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return Summary{}, err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	sum, err := Inspect(p, db, encryptionKey)
	if err != nil {
		return Summary{}, err
	}
	sum.TargetDriver = targetDriver
	return sum, nil
}

// Verify reads the whole backup: every table's checksum and row count, every
// file's zip checksum, and that no entry points outside its folder.
func Verify(p string, progress ProgressFunc) error {
	progress = orNop(progress)
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer func() { _ = zr.Close() }()
	m, err := readManifest(&zr.Reader)
	if err != nil {
		return err
	}
	for i, te := range m.Tables {
		f := findFile(&zr.Reader, "db/"+te.Name+".jsonl")
		if f == nil {
			return fmt.Errorf("%w: table %s is missing", ErrCorrupt, te.Name)
		}
		sum, rows, err := hashLines(f)
		if err != nil {
			return fmt.Errorf("%w: table %s: %v", ErrCorrupt, te.Name, err)
		}
		if sum != te.SHA256 || rows != te.Rows {
			return fmt.Errorf("%w: table %s does not match its checksum", ErrCorrupt, te.Name)
		}
		progress(Progress{Phase: PhaseVerify, Table: te.Name, Done: int64(i + 1), Total: int64(len(m.Tables))})
	}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "files/") {
			continue
		}
		if _, _, ok := splitFileEntry(f.Name); !ok {
			return fmt.Errorf("%w: unsafe file name %q", ErrCorrupt, f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, f.Name, err)
		}
		_, err = io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, f.Name, err)
		}
	}
	return nil
}

func hashLines(f *zip.File) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	br := bufio.NewReader(io.TeeReader(rc, h))
	var rows int64
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			rows++
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), rows, nil
}

// splitFileEntry splits "files/<area>/<rel>" and rejects names that are
// absolute or climb out of the area. Directory entries ("files/media/sub/")
// are allowed and come back with an empty rel.
func splitFileEntry(name string) (area, rel string, ok bool) {
	rest, found := strings.CutPrefix(name, "files/")
	if !found {
		return "", "", false
	}
	area, rel, found = strings.Cut(rest, "/")
	if !found || area == "" || area == "." || area == ".." || strings.Contains(area, `\`) {
		return "", "", false
	}
	if rel == "" || strings.HasSuffix(rel, "/") {
		return area, "", true
	}
	if strings.Contains(rel, `\`) || path.IsAbs(rel) {
		return "", "", false
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", false
	}
	return area, clean, true
}
