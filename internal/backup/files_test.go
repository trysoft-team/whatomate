package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
