package backup

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/config"
)

func TestCreate_WritesEveryTableAndManifest(t *testing.T) {
	db := newSQLiteDB(t)
	seedEveryTable(t, db, time.Nanosecond)
	dir := t.TempDir()

	var phases []string
	info, err := Create(context.Background(), db, dir, Options{Kind: KindManual, AppVersion: "v9", EncryptionKey: "k"}, func(p Progress) {
		phases = append(phases, p.Phase)
	})
	require.NoError(t, err)
	assert.Regexp(t, `^whatomate-backup-\d{8}T\d{6}Z\.zip$`, info.Name)
	assert.Contains(t, phases, PhaseTables)

	m, err := ReadManifest(filepath.Join(dir, info.Name))
	require.NoError(t, err)
	assert.Equal(t, FormatVersion, m.Format)
	assert.Equal(t, "sqlite", m.SourceDriver)
	assert.Equal(t, KeyFingerprint("k"), m.KeyFingerprint)
	assert.Len(t, m.KeyFingerprint, 12)
	for _, te := range m.Tables {
		var n int64
		require.NoError(t, db.Table(te.Name).Count(&n).Error)
		assert.Equal(t, n, te.Rows, te.Name)
		assert.NotEmpty(t, te.SHA256)
		assert.NotEmpty(t, te.Columns)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".tmp-"), "temp file left: %s", e.Name())
	}
}

func TestCreate_FilesSkipSymlinksAndSkipDirs(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(media, "flow_media", "org"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(media, "flow_media", "org", "a.jpg"), []byte("jpeg"), 0o640))
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(media, "link")))
	backups := filepath.Join(media, "backups")
	require.NoError(t, os.MkdirAll(backups, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(backups, "old.zip"), []byte("x"), 0o640))

	info, err := Create(context.Background(), db, backups, Options{
		Kind: KindManual, IncludeFiles: true, Areas: []FileArea{{Name: "media", Dir: media}}, Skip: []string{backups},
	}, nil)
	require.NoError(t, err)

	zr, err := zip.OpenReader(filepath.Join(backups, info.Name))
	require.NoError(t, err)
	defer func() { _ = zr.Close() }()
	var names []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "files/") {
			names = append(names, f.Name)
		}
	}
	assert.Equal(t, []string{"files/media/flow_media/org/a.jpg"}, names)
	assert.Equal(t, FileStats{Count: 1, Bytes: 4}, info.Manifest.Files["media"])
}

func TestCreate_FollowsSymlinkedAreaRoot(t *testing.T) {
	db := newSQLiteDB(t)
	target := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(target, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(target, "sub", "a.txt"), []byte("hi"), 0o640))
	require.NoError(t, os.MkdirAll(filepath.Join(target, "backups"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(target, "backups", "old.zip"), []byte("x"), 0o640))
	link := filepath.Join(t.TempDir(), "media")
	require.NoError(t, os.Symlink(target, link))
	backups := filepath.Join(link, "backups")

	info, err := Create(context.Background(), db, backups, Options{
		Kind: KindManual, IncludeFiles: true, Areas: []FileArea{{Name: "media", Dir: link}}, Skip: []string{backups},
	}, nil)
	require.NoError(t, err)

	zr, err := zip.OpenReader(filepath.Join(backups, info.Name))
	require.NoError(t, err)
	defer func() { _ = zr.Close() }()
	var names []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "files/") {
			names = append(names, f.Name)
		}
	}
	assert.Equal(t, []string{"files/media/sub/a.txt"}, names)
	assert.Equal(t, FileStats{Count: 1, Bytes: 2}, info.Manifest.Files["media"])
}

func TestCreate_ReadsChildrenBeforeParents(t *testing.T) {
	db := newSQLiteDB(t)
	info, err := Create(context.Background(), db, t.TempDir(), Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	pos := map[string]int{}
	for i, te := range info.Manifest.Tables {
		pos[te.Name] = i
	}
	fks, err := foreignKeys(db)
	require.NoError(t, err)
	checked := 0
	for _, fk := range fks {
		child, okC := pos[fk.Table]
		parent, okP := pos[fk.RefTable]
		if !okC || !okP || fk.Table == fk.RefTable {
			continue
		}
		checked++
		assert.Less(t, child, parent, "%s.%s -> %s read after its parent", fk.Table, fk.Column, fk.RefTable)
	}
	assert.NotZero(t, checked)
}

func TestCreate_CancelLeavesNothing(t *testing.T) {
	db := newSQLiteDB(t)
	seedEveryTable(t, db, time.Nanosecond)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Create(ctx, db, dir, Options{Kind: KindManual}, nil)
	require.Error(t, err)
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)
}

func TestList_NewestFirstAndBrokenFiles(t *testing.T) {
	db := newSQLiteDB(t)
	dir := t.TempDir()
	first, err := Create(context.Background(), db, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	second, err := Create(context.Background(), db, dir, Options{Kind: KindPreRestore}, nil)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(second.Name, "whatomate-pre-restore-"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.zip"), []byte("not a zip"), 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".tmp-x.zip"), []byte("x"), 0o640))

	list, err := List(dir)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, second.Name, list[0].Name)
	assert.Equal(t, first.Name, list[1].Name)
	assert.Equal(t, "broken.zip", list[2].Name)
	assert.NotEmpty(t, list[2].Error)
	assert.Nil(t, list[2].Manifest)
}

func TestNames(t *testing.T) {
	assert.True(t, ValidName("whatomate-backup-20261013T181530Z.zip"))
	for _, bad := range []string{"", ".tmp-1.zip", "../x.zip", "a/b.zip", `a\b.zip`, "x.txt", "x..zip"} {
		assert.False(t, ValidName(bad), bad)
	}
	dir := t.TempDir()
	assert.Equal(t, "my-backup.zip", UploadName(dir, "../../my backup.ZIP"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "my-backup.zip"), nil, 0o640))
	assert.Equal(t, "my-backup-2.zip", UploadName(dir, "my backup.zip"))
	assert.Equal(t, "uploaded-backup.zip", UploadName(dir, "...zip"))
}

func fileEntries(t *testing.T, p string) []string {
	t.Helper()
	zr, err := zip.OpenReader(p)
	require.NoError(t, err)
	defer func() { _ = zr.Close() }()
	var names []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "files/") {
			names = append(names, f.Name)
		}
	}
	return names
}

func TestCreate_SkipsBackslashNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is the path separator on Windows")
	}
	db := newSQLiteDB(t)
	media := t.TempDir()
	write(t, filepath.Join(media, `odd\name.txt`), "x")
	write(t, filepath.Join(media, "ok.txt"), "ok")
	backups := t.TempDir()
	var warned []string
	info, err := Create(context.Background(), db, backups, Options{
		Kind: KindManual, IncludeFiles: true, Areas: []FileArea{{Name: "media", Dir: media}},
		Warn: func(msg string, kv ...any) { warned = append(warned, fmt.Sprint(append([]any{msg}, kv...)...)) },
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"files/media/ok.txt"}, fileEntries(t, filepath.Join(backups, info.Name)))
	require.Len(t, warned, 1)
	assert.Contains(t, warned[0], `odd\name.txt`)
	require.NoError(t, Verify(filepath.Join(backups, info.Name), nil), "the backup stays restorable")
}

func TestProtectedPaths(t *testing.T) {
	db := config.DatabaseConfig{Driver: config.DriverSQLite, Path: "/data/w.db"}
	assert.Equal(t, []string{"/b", "/data/config.toml", "/data/w.db", "/data/w.db-wal", "/data/w.db-shm", "/data/w.db-journal"},
		ProtectedPaths("/b", "/data/config.toml", db))
	assert.Equal(t, []string{"/b"}, ProtectedPaths("/b", "", config.DatabaseConfig{Driver: config.DriverPostgres, Path: "/data/w.db"}))
}

func TestCreateAndRestore_LeaveConfigAndDatabaseAlone(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	write(t, filepath.Join(media, "config.toml"), "cfg-before")
	write(t, filepath.Join(media, "w.db"), "db-before")
	write(t, filepath.Join(media, "w.db-wal"), "wal-before")
	write(t, filepath.Join(media, "a.txt"), "a1")
	backups := filepath.Join(media, "backups")
	skip := ProtectedPaths(backups, filepath.Join(media, "config.toml"), config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(media, "w.db")})
	areas := []FileArea{{Name: "media", Dir: media}}
	info, err := Create(context.Background(), db, backups, Options{Kind: KindManual, IncludeFiles: true, Areas: areas, Skip: skip}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"files/media/a.txt"}, fileEntries(t, filepath.Join(backups, info.Name)))

	write(t, filepath.Join(media, "config.toml"), "cfg-after")
	write(t, filepath.Join(media, "w.db"), "db-after")
	write(t, filepath.Join(media, "w.db-shm"), "shm-after")
	write(t, filepath.Join(media, "a.txt"), "a2")
	require.NoError(t, Restore(context.Background(), db, filepath.Join(backups, info.Name), RestoreOptions{Areas: areas, Skip: skip}, nil))
	assert.Equal(t, "a1", read(t, filepath.Join(media, "a.txt")))
	assert.Equal(t, "cfg-after", read(t, filepath.Join(media, "config.toml")))
	assert.Equal(t, "db-after", read(t, filepath.Join(media, "w.db")))
	assert.Equal(t, "wal-before", read(t, filepath.Join(media, "w.db-wal")))
	assert.Equal(t, "shm-after", read(t, filepath.Join(media, "w.db-shm")))
}

func TestRestore_RejectsNestedProtectedFile(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	write(t, filepath.Join(media, "data", "w.db"), "db")
	backups := t.TempDir()
	skip := ProtectedPaths(backups, "", config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(media, "data", "w.db")})
	areas := []FileArea{{Name: "media", Dir: media}}
	info, err := Create(context.Background(), db, backups, Options{Kind: KindManual, IncludeFiles: true, Areas: areas, Skip: skip}, nil)
	require.NoError(t, err)
	assert.Empty(t, fileEntries(t, filepath.Join(backups, info.Name)))

	err = Restore(context.Background(), db, filepath.Join(backups, info.Name), RestoreOptions{Areas: areas, Skip: skip}, nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrFiles)
	assert.Contains(t, err.Error(), "must be directly inside")
	assert.Equal(t, "db", read(t, filepath.Join(media, "data", "w.db")))
}

func TestEstimateSize(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	write(t, filepath.Join(media, "a.bin"), strings.Repeat("x", 1000))
	write(t, filepath.Join(media, "backups", "old.zip"), strings.Repeat("x", 5000))
	areas := []FileArea{{Name: "media", Dir: media}}
	dbOnly := EstimateSize(db, areas, false, nil)
	assert.Positive(t, dbOnly)
	assert.Equal(t, dbOnly+1000, EstimateSize(db, areas, true, []string{filepath.Join(media, "backups")}))
}

func TestAreas_DropNestedAndEmpty(t *testing.T) {
	root := realDir(t, t.TempDir())
	assert.Equal(t, []FileArea{{Name: "media", Dir: root}}, Areas(root, filepath.Join(root, "audio")))
	other := realDir(t, t.TempDir())
	assert.Equal(t, []FileArea{{Name: "media", Dir: root}, {Name: "audio", Dir: other}}, Areas(root, other))
	assert.Equal(t, []FileArea{{Name: "audio", Dir: other}}, Areas("", other))

	link := filepath.Join(t.TempDir(), "media-link")
	require.NoError(t, os.Symlink(root, link))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "audio"), 0o750))
	assert.Equal(t, []FileArea{{Name: "media", Dir: root}}, Areas(link, filepath.Join(root, "audio")))
}

func realDir(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

func TestCleanTemp(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{".tmp-a.zip", ".upload-b.part", "keep.zip"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), nil, 0o640))
	}
	CleanTemp(dir)
	entries, _ := os.ReadDir(dir)
	require.Len(t, entries, 1)
	assert.Equal(t, "keep.zip", entries[0].Name())
}
