package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(s), 0o640))
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

func TestRestore_PutsFilesBack(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	backups := filepath.Join(media, "backups")
	write(t, filepath.Join(media, "a.txt"), "a1")
	write(t, filepath.Join(media, "sub", "b.txt"), "b1")
	areas := []FileArea{{Name: "media", Dir: media}}
	info, err := Create(context.Background(), db, backups, Options{Kind: KindManual, IncludeFiles: true, Areas: areas, Skip: []string{backups}}, nil)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(media, "a.txt")))
	write(t, filepath.Join(media, "sub", "b.txt"), "b2")
	write(t, filepath.Join(media, "c.txt"), "new")

	require.NoError(t, Restore(context.Background(), db, filepath.Join(backups, info.Name), RestoreOptions{Areas: areas, Skip: []string{backups}}, nil))
	assert.Equal(t, "a1", read(t, filepath.Join(media, "a.txt")))
	assert.Equal(t, "b1", read(t, filepath.Join(media, "sub", "b.txt")))
	assert.NoFileExists(t, filepath.Join(media, "c.txt"))
	assert.FileExists(t, filepath.Join(backups, info.Name), "the backup folder is left alone")
	entries, _ := os.ReadDir(media)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".restore-") || strings.HasPrefix(e.Name(), ".old-"), e.Name())
	}
}

func TestRestore_WithoutFilesLeavesFolders(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	write(t, filepath.Join(media, "a.txt"), "a1")
	dir := t.TempDir()
	info, err := Create(context.Background(), db, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	write(t, filepath.Join(media, "a.txt"), "changed")
	require.NoError(t, Restore(context.Background(), db, filepath.Join(dir, info.Name), RestoreOptions{Areas: []FileArea{{Name: "media", Dir: media}}}, nil))
	assert.Equal(t, "changed", read(t, filepath.Join(media, "a.txt")))
}

func TestRestore_LeavesAreaNotInBackup(t *testing.T) {
	db := newSQLiteDB(t)
	media, audio := t.TempDir(), t.TempDir()
	write(t, filepath.Join(media, "a.txt"), "a1")
	dir := t.TempDir()
	info, err := Create(context.Background(), db, dir, Options{Kind: KindManual, IncludeFiles: true, Areas: []FileArea{{Name: "media", Dir: media}}}, nil)
	require.NoError(t, err)

	write(t, filepath.Join(media, "a.txt"), "changed")
	write(t, filepath.Join(audio, "keep.opus"), "voice")
	areas := []FileArea{{Name: "media", Dir: media}, {Name: "audio", Dir: audio}}
	require.NoError(t, Restore(context.Background(), db, filepath.Join(dir, info.Name), RestoreOptions{Areas: areas}, nil))
	assert.Equal(t, "a1", read(t, filepath.Join(media, "a.txt")))
	assert.Equal(t, "voice", read(t, filepath.Join(audio, "keep.opus")), "an area the backup does not hold is left alone")
}

func TestRestore_RejectsNestedBackupFolder(t *testing.T) {
	db := newSQLiteDB(t)
	media := t.TempDir()
	backups := filepath.Join(media, "x", "backups")
	write(t, filepath.Join(media, "a.txt"), "a1")
	areas := []FileArea{{Name: "media", Dir: media}}
	opts := Options{Kind: KindManual, IncludeFiles: true, Areas: areas, Skip: []string{backups}}
	info, err := Create(context.Background(), db, backups, opts, nil)
	require.NoError(t, err)

	org := models.Organization{Name: "after", Slug: "after-" + uuid.NewString()[:8]}
	require.NoError(t, db.Create(&org).Error)
	write(t, filepath.Join(media, "a.txt"), "changed")

	err = Restore(context.Background(), db, filepath.Join(backups, info.Name), RestoreOptions{Areas: areas, Skip: []string{backups}}, nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrFiles)
	assert.Contains(t, err.Error(), "must be directly inside")
	var n int64
	require.NoError(t, db.Model(&models.Organization{}).Where("id = ?", org.ID).Count(&n).Error)
	assert.Equal(t, int64(1), n, "the database is unchanged")
	assert.Equal(t, "changed", read(t, filepath.Join(media, "a.txt")))
	assert.FileExists(t, filepath.Join(backups, info.Name))
}

func TestLeftoverWorkDirs(t *testing.T) {
	media, audio := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(media, ".restore-123"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(audio, ".old-456"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(media, "sub", ".old-789"), 0o750))
	write(t, filepath.Join(media, "a.txt"), "a")
	areas := []FileArea{{Name: "media", Dir: media}, {Name: "audio", Dir: audio}, {Name: "gone", Dir: filepath.Join(media, "missing")}}
	assert.Equal(t, []string{filepath.Join(media, ".restore-123"), filepath.Join(audio, ".old-456")}, LeftoverWorkDirs(areas))
	assert.FileExists(t, filepath.Join(media, "a.txt"))
	assert.DirExists(t, filepath.Join(media, ".restore-123"), "nothing is deleted")
}

func TestPutBack_ReportsFailedMoves(t *testing.T) {
	dst, src := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a"), "a")
	require.NoError(t, putBack(dst, src, []string{"a"}))
	assert.Equal(t, "a", read(t, filepath.Join(dst, "a")))
	assert.Error(t, putBack(dst, src, []string{"missing"}))
}

func TestSplitFileEntry(t *testing.T) {
	for _, bad := range []string{"files/media/../x", "files/media//etc/passwd", "files/media/a\\..\\b", "files//x", "files/media"} {
		_, rel, ok := splitFileEntry(bad)
		assert.False(t, ok && rel != "", bad)
	}
	area, rel, ok := splitFileEntry("files/audio/x/y.opus")
	assert.True(t, ok)
	assert.Equal(t, "audio", area)
	assert.Equal(t, "x/y.opus", rel)
}
