package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rewriteBackup copies src to a new file, passing each entry through edit;
// edit returns the new content and false to drop the entry.
func rewriteBackup(t *testing.T, src string, edit func(name string, data []byte) ([]byte, bool)) string {
	t.Helper()
	zr, err := zip.OpenReader(src)
	require.NoError(t, err)
	defer func() { _ = zr.Close() }()
	dst := filepath.Join(t.TempDir(), "edited.zip")
	f, err := os.Create(dst)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	for _, e := range zr.File {
		rc, err := e.Open()
		require.NoError(t, err)
		var buf bytes.Buffer
		_, err = buf.ReadFrom(rc)
		require.NoError(t, err)
		_ = rc.Close()
		data, keep := edit(e.Name, buf.Bytes())
		if !keep {
			continue
		}
		w, err := zw.Create(e.Name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return dst
}

func editManifest(t *testing.T, change func(m *Manifest)) func(string, []byte) ([]byte, bool) {
	return func(name string, data []byte) ([]byte, bool) {
		if name != manifestName {
			return data, true
		}
		var m Manifest
		require.NoError(t, json.Unmarshal(data, &m))
		change(&m)
		out, err := json.Marshal(m)
		require.NoError(t, err)
		return out, true
	}
}

func makeBackup(t *testing.T) (string, *gorm.DB) {
	t.Helper()
	db := newSQLiteDB(t)
	seedEveryTable(t, db, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), db, dir, Options{Kind: KindManual, AppVersion: "v1", EncryptionKey: "key-a"}, nil)
	require.NoError(t, err)
	return filepath.Join(dir, info.Name), db
}

func TestInspect_Summary(t *testing.T) {
	path, db := makeBackup(t)
	sum, err := Inspect(path, db, "key-a")
	require.NoError(t, err)
	assert.Equal(t, "sqlite", sum.TargetDriver)
	assert.True(t, sum.KeyMatches)
	assert.Equal(t, filepath.Base(path), sum.Name)

	sum, err = Inspect(path, db, "key-b")
	require.NoError(t, err)
	assert.False(t, sum.KeyMatches)
	require.NoError(t, Verify(path, nil))
}

func TestInspectFile_NeedsNoDatabase(t *testing.T) {
	p, _ := makeBackup(t)
	sum, err := InspectFile(p, "postgres", "key-a")
	require.NoError(t, err)
	assert.Equal(t, "postgres", sum.TargetDriver)
	assert.True(t, sum.KeyMatches)

	sum, err = InspectFile(p, "mysql", "other")
	require.NoError(t, err)
	assert.False(t, sum.KeyMatches)
}

func TestInspect_RefusesNewerBackups(t *testing.T) {
	path, db := makeBackup(t)
	for name, change := range map[string]func(m *Manifest){
		"format": func(m *Manifest) { m.Format = FormatVersion + 1 },
		"table":  func(m *Manifest) { m.Tables = append(m.Tables, TableEntry{Name: "future_things"}) },
		"column": func(m *Manifest) { m.Tables[0].Columns = append(m.Tables[0].Columns, "future_col") },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(rewriteBackup(t, path, editManifest(t, change)), db, "key-a")
			assert.ErrorIs(t, err, ErrNewer)
		})
	}
}

func TestVerify_RefusesDamage(t *testing.T) {
	path, db := makeBackup(t)
	tampered := rewriteBackup(t, path, func(name string, data []byte) ([]byte, bool) {
		if name == "db/organizations.jsonl" {
			return bytes.Replace(data, []byte("0-name"), []byte("0-NAME"), 1), true
		}
		return data, true
	})
	assert.ErrorIs(t, Verify(tampered, nil), ErrCorrupt)

	missing := rewriteBackup(t, path, func(name string, data []byte) ([]byte, bool) { return data, name != "db/users.jsonl" })
	assert.ErrorIs(t, Verify(missing, nil), ErrCorrupt)

	evil := rewriteBackup(t, path, func(_ string, data []byte) ([]byte, bool) { return data, true })
	addEntry(t, evil, "files/media/../../evil.txt", []byte("x"))
	assert.ErrorIs(t, Verify(evil, nil), ErrCorrupt)

	notZip := filepath.Join(t.TempDir(), "n.zip")
	require.NoError(t, os.WriteFile(notZip, []byte("hello"), 0o600))
	_, err := Inspect(notZip, db, "")
	assert.ErrorIs(t, err, ErrCorrupt)
}

// addEntry rewrites the backup at p with one more entry.
func addEntry(t *testing.T, p, name string, data []byte) {
	t.Helper()
	copyPath := rewriteBackup(t, p, func(_ string, d []byte) ([]byte, bool) { return d, true })
	zr, err := zip.OpenReader(copyPath)
	require.NoError(t, err)
	defer func() { _ = zr.Close() }()
	f, err := os.Create(p)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	for _, e := range zr.File {
		require.NoError(t, zw.Copy(e))
	}
	w, err := zw.Create(name)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}
