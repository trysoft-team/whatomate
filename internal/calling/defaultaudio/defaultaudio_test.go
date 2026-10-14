package defaultaudio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstall_WritesBundledFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audio")
	written, err := Install(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{HoldMusicFile, RingbackFile}, written)
	for _, name := range written {
		want, err := files.ReadFile(name)
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, want, got, name)
		assert.Equal(t, "OggS", string(got[:4]), "%s is an Ogg file", name)
	}
}

func TestInstall_KeepsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, HoldMusicFile)
	require.NoError(t, os.WriteFile(custom, []byte("my hold music"), 0o600))

	written, err := Install(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{RingbackFile}, written)
	got, err := os.ReadFile(custom)
	require.NoError(t, err)
	assert.Equal(t, "my hold music", string(got))

	written, err = Install(dir)
	require.NoError(t, err)
	assert.Empty(t, written, "a second run writes nothing")
}
