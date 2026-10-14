package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUploads() *Uploads {
	return &Uploads{Space: func(string, int64) error { return nil }}
}

func TestUploads_StartPutFinish(t *testing.T) {
	dir := t.TempDir()
	u := newUploads()
	id, err := u.Start(dir, "My Backup.zip", 6, 0)
	require.NoError(t, err)

	n, err := u.Put(id, 0, []byte("abc"))
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	n, err = u.Put(id, 3, []byte("def"))
	require.NoError(t, err)
	assert.EqualValues(t, 6, n)

	p, err := u.Finish(id)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "My-Backup.zip"), p)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "abcdef", string(b))
	fi, err := os.Stat(p)
	require.NoError(t, err)
	if os.PathSeparator == '/' {
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	_, err = u.Finish(id)
	assert.ErrorIs(t, err, ErrUploadNotFound)
}

func TestUploads_Refusals(t *testing.T) {
	dir := t.TempDir()
	u := newUploads()

	_, err := u.Start(dir, "notes.txt", 5, 0)
	assert.ErrorIs(t, err, ErrNotBackupFile)
	_, err = u.Start(dir, "b.zip", 0, 0)
	assert.ErrorIs(t, err, ErrNotBackupFile)

	var big *TooLargeError
	_, err = u.Start(dir, "b.zip", 2<<20, 1)
	require.ErrorAs(t, err, &big)
	assert.Equal(t, 1, big.LimitMB)

	u.Space = func(string, int64) error { return errors.New("disk full") }
	var sp *SpaceError
	_, err = u.Start(dir, "b.zip", 5, 0)
	require.ErrorAs(t, err, &sp)
	u.Space = func(string, int64) error { return nil }

	id, err := u.Start(dir, "b.zip", 4, 0)
	require.NoError(t, err)
	var off *OffsetError
	_, err = u.Put(id, 2, []byte("ab"))
	require.ErrorAs(t, err, &off)
	assert.EqualValues(t, 0, off.Received)
	_, err = u.Put(id, 0, []byte("abcde"))
	assert.ErrorIs(t, err, ErrBadChunk)
	_, err = u.Put(id, 0, nil)
	assert.ErrorIs(t, err, ErrBadChunk)
	_, err = u.Put("nope", 0, []byte("a"))
	assert.ErrorIs(t, err, ErrUploadNotFound)

	_, err = u.Put(id, 0, []byte("ab"))
	require.NoError(t, err)
	var inc *IncompleteError
	_, err = u.Finish(id)
	require.ErrorAs(t, err, &inc)
	assert.EqualValues(t, 2, inc.Received)
	assert.EqualValues(t, 4, inc.Size)
}

func TestUploads_Cancel(t *testing.T) {
	dir := t.TempDir()
	u := newUploads()
	id, err := u.Start(dir, "b.zip", 4, 0)
	require.NoError(t, err)
	u.Cancel(id)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
	_, err = u.Put(id, 0, []byte("a"))
	assert.ErrorIs(t, err, ErrUploadNotFound)
}
