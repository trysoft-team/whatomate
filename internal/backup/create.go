package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"gorm.io/gorm"
)

// FileArea is a folder of local files a backup can hold.
type FileArea struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// Areas returns the file areas for the media and audio folders, dropping an
// empty one and one that lies inside the other.
func Areas(media, audio string) []FileArea {
	var out []FileArea
	add := func(name, dir string) {
		if dir == "" {
			return
		}
		abs, err := realPath(dir)
		if err != nil {
			return
		}
		for _, a := range out {
			if within(abs, a.Dir) {
				return
			}
		}
		out = append(out, FileArea{Name: name, Dir: abs})
	}
	add("media", media)
	add("audio", audio)
	return out
}

// realPath returns p made absolute with symlinks resolved, or just absolute
// when p does not exist yet.
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs)), nil
	}
	return abs, nil
}

// ProtectedPaths are the paths a backup never copies and a restore never
// replaces: the backup folder, the config file in use and, on SQLite, the
// database file with its journal files.
func ProtectedPaths(backupDir, configPath string, db config.DatabaseConfig) []string {
	out := []string{backupDir}
	if configPath != "" {
		out = append(out, configPath)
	}
	if db.Driver == config.DriverSQLite && db.Path != "" {
		out = append(out, db.Path, db.Path+"-wal", db.Path+"-shm", db.Path+"-journal")
	}
	return out
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Options control a backup.
type Options struct {
	Kind          string
	AppVersion    string
	EncryptionKey string
	IncludeFiles  bool
	Areas         []FileArea
	// Skip lists folders and files never copied, such as the backup folder
	// itself (see ProtectedPaths).
	Skip []string
	// Path, when set, is the file to write instead of a new name in dir.
	Path string
	// Warn, when set, is told about files left out of the backup.
	Warn func(msg string, kv ...any)
}

// Create writes a backup of db (and, if asked, the local files) into dir.
// The file only appears under its final name once it is complete.
func Create(ctx context.Context, db *gorm.DB, dir string, opts Options, progress ProgressFunc) (Info, error) {
	progress = orNop(progress)
	tmpDir := dir
	if opts.Path != "" {
		tmpDir = filepath.Dir(opts.Path)
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return Info{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, tempPrefix+"*.zip")
	if err != nil {
		return Info{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	kind := opts.Kind
	if kind == "" {
		kind = KindManual
	}
	m := Manifest{
		Format: FormatVersion, AppVersion: opts.AppVersion, Kind: kind, CreatedAt: time.Now().UTC(),
		SourceDriver: db.Name(), KeyFingerprint: KeyFingerprint(opts.EncryptionKey), IncludeFiles: opts.IncludeFiles,
	}
	zw := zip.NewWriter(tmp)
	tbls, err := backupTables(db)
	if err != nil {
		return Info{}, err
	}
	fks, err := foreignKeys(db)
	if err != nil {
		return Info{}, err
	}
	byName := make(map[string]table, len(tbls))
	names := make([]string, 0, len(tbls))
	for _, t := range tbls {
		byName[t.Name] = t
		names = append(names, t.Name)
	}
	order := planLoad(names, fks).Order
	// Children are read before their parents so a row inserted during the
	// backup cannot appear without the parent it points to.
	readTables := func(tx *gorm.DB) error {
		for i := len(order) - 1; i >= 0; i-- {
			t := byName[order[i]]
			entry, err := writeTable(ctx, tx, zw, t, progress)
			if err != nil {
				return fmt.Errorf("table %s: %w", t.Name, err)
			}
			m.Tables = append(m.Tables, entry)
		}
		return nil
	}
	switch db.Name() {
	case config.DriverPostgres, config.DriverMySQL:
		err = db.WithContext(ctx).Transaction(readTables, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	default:
		// SQLite opens transactions with _txlock=immediate, which would block
		// every writer for the whole backup.
		err = readTables(db)
	}
	if err != nil {
		return Info{}, err
	}
	if opts.IncludeFiles {
		m.Files = map[string]FileStats{}
		for _, a := range opts.Areas {
			st, err := writeArea(ctx, zw, a, opts.Skip, opts.Warn, progress)
			if err != nil {
				return Info{}, fmt.Errorf("files in %s: %w", a.Dir, err)
			}
			m.Files[a.Name] = st
		}
	}
	w, err := zw.Create(manifestName)
	if err != nil {
		return Info{}, err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return Info{}, err
	}
	if err := zw.Close(); err != nil {
		return Info{}, err
	}
	if err := tmp.Sync(); err != nil {
		return Info{}, err
	}
	if err := tmp.Close(); err != nil {
		return Info{}, err
	}
	final := opts.Path
	if final == "" {
		final = filepath.Join(dir, newName(dir, kind, m.CreatedAt))
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return Info{}, err
	}
	done = true
	st, err := os.Stat(final)
	if err != nil {
		return Info{}, err
	}
	return Info{Name: filepath.Base(final), Size: st.Size(), Manifest: &m}, nil
}

func writeTable(ctx context.Context, db *gorm.DB, zw *zip.Writer, t table, progress ProgressFunc) (TableEntry, error) {
	entry := TableEntry{Name: t.Name}
	for _, f := range t.Columns {
		entry.Columns = append(entry.Columns, f.DBName)
	}
	var total int64
	if err := db.WithContext(ctx).Table(t.Name).Count(&total).Error; err != nil {
		return entry, err
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "db/" + t.Name + ".jsonl", Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return entry, err
	}
	h := sha256.New()
	out := io.MultiWriter(w, h)
	model := reflect.New(t.Schema.ModelType).Interface()
	rows, err := db.WithContext(ctx).Unscoped().Model(model).Order(strings.Join(t.Schema.PrimaryFieldDBNames, ", ")).Rows()
	if err != nil {
		return entry, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		item := reflect.New(t.Schema.ModelType)
		if err := db.ScanRows(rows, item.Interface()); err != nil {
			return entry, err
		}
		line, err := encodeRow(ctx, t.Columns, item.Elem())
		if err != nil {
			return entry, err
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			return entry, err
		}
		entry.Rows++
		if entry.Rows%500 == 0 {
			progress(Progress{Phase: PhaseTables, Table: t.Name, Done: entry.Rows, Total: total})
		}
	}
	if err := rows.Err(); err != nil {
		return entry, err
	}
	progress(Progress{Phase: PhaseTables, Table: t.Name, Done: entry.Rows, Total: total})
	entry.SHA256 = hex.EncodeToString(h.Sum(nil))
	return entry, nil
}

func writeArea(ctx context.Context, zw *zip.Writer, a FileArea, skip []string, warn func(string, ...any), progress ProgressFunc) (FileStats, error) {
	var st FileStats
	err := walkArea(ctx, a, skip, func(p, rel string, info fs.FileInfo) error {
		if strings.Contains(rel, `\`) {
			if warn != nil {
				warn("Left a file out of the backup because its name contains a backslash", "path", p)
			}
			return nil
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = "files/" + a.Name + "/" + filepath.ToSlash(rel)
		hdr.Method = zip.Store
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		n, err := io.Copy(w, f)
		_ = f.Close()
		if err != nil {
			return err
		}
		st.Count++
		st.Bytes += n
		progress(Progress{Phase: PhaseFiles, Table: a.Name, Done: st.Count})
		return nil
	})
	return st, err
}

// walkArea calls fn for each regular file in the area, leaving out symlinks,
// restore work folders and the skip paths. A missing area folder is empty.
func walkArea(ctx context.Context, a FileArea, skip []string, fn func(p, rel string, info fs.FileInfo) error) error {
	root, err := filepath.Abs(a.Dir)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var skipReal []string
	for _, s := range skip {
		if p, err := realPath(s); err == nil {
			skipReal = append(skipReal, p)
		}
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (isWorkDir(d.Name()) || skipped(p, skipReal)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipped(p, skipReal) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return fn(p, rel, info)
	})
}

// EstimateSize roughly predicts a backup's size: the database's size plus,
// when files are included, the files in the areas. Sizes that cannot be read
// count as zero.
func EstimateSize(db *gorm.DB, areas []FileArea, includeFiles bool, skip []string) int64 {
	n := databaseSize(db)
	if includeFiles {
		for _, a := range areas {
			_ = walkArea(context.Background(), a, skip, func(_, _ string, info fs.FileInfo) error {
				n += info.Size()
				return nil
			})
		}
	}
	return n
}

func databaseSize(db *gorm.DB) int64 {
	var n int64
	switch db.Name() {
	case config.DriverPostgres:
		db.Raw("SELECT pg_database_size(current_database())").Scan(&n)
	case config.DriverMySQL:
		db.Raw("SELECT COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&n)
	default:
		db.Raw("SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&n)
	}
	return n
}

// isWorkDir reports whether name is a restore's staging or old-files folder.
func isWorkDir(name string) bool {
	return strings.HasPrefix(name, ".restore-") || strings.HasPrefix(name, ".old-")
}

func skipped(p string, skip []string) bool {
	for _, s := range skip {
		if s == p {
			return true
		}
	}
	return false
}
