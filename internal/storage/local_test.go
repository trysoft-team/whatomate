package storage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/storage"
)

func TestLocalFS_Upload_WritesFile(t *testing.T) {
	dir := t.TempDir()
	fs := storage.NewLocalFS(dir)

	err := fs.Upload(context.Background(), "recordings/org/call.ogg", bytes.NewReader([]byte("ogg-bytes")), "audio/ogg")
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "recordings", "org", "call.ogg"))
	require.NoError(t, err)
	assert.Equal(t, []byte("ogg-bytes"), got)
}

func TestLocalFS_Resolve_RejectsTraversal(t *testing.T) {
	fs := storage.NewLocalFS(t.TempDir())
	_, err := fs.Resolve("../secret.ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid storage key")
}

func TestLocalFS_Resolve_RejectsAbsolute(t *testing.T) {
	fs := storage.NewLocalFS(t.TempDir())
	_, err := fs.Resolve("/etc/passwd")
	require.Error(t, err)
}

func TestLocalFS_Upload_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	fs := storage.NewLocalFS(dir)
	err := fs.Upload(context.Background(), "../outside.ogg", bytes.NewReader([]byte("x")), "audio/ogg")
	require.Error(t, err)
}

func TestNewLocalFS_EmptyRootDefaults(t *testing.T) {
	fs := storage.NewLocalFS("")
	assert.Equal(t, "./uploads", fs.Root)
}
