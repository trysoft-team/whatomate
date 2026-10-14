package diskspace

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNearestExisting(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, dir, NearestExisting(dir))
	assert.Equal(t, dir, NearestExisting(filepath.Join(dir, "a", "b")))
}

func TestFree(t *testing.T) {
	n, err := Free(filepath.Join(t.TempDir(), "not", "yet"))
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		require.NoError(t, err)
		assert.Positive(t, n)
	default:
		assert.ErrorIs(t, err, ErrUnsupported)
	}
}
