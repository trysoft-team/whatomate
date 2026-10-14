package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppBase(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	fixed := candidateInfo{Path: "/srv/w/config.toml", DataDir: "/srv/w", Scope: "fixed"}
	assert.Equal(t, cwd, appBase(fixed), "a fixed -config path resolves relative paths against the working directory")
	assert.Equal(t, filepath.Join(cwd, "backups"), backupDir(fixed))

	user := candidateInfo{Path: "/home/a/.config/whatomate/config.toml", DataDir: "/home/a/.local/share/whatomate", Scope: "user", base: "/home/a/.local/share/whatomate"}
	assert.Equal(t, "/home/a/.local/share/whatomate", appBase(user))
	assert.Equal(t, filepath.Join("/home/a/.local/share/whatomate", "uploads"), mediaDir(user))
	assert.Equal(t, filepath.Join("/home/a/.local/share/whatomate", "audio"), audioDir(user))
	assert.Equal(t, filepath.Join("/home/a/.local/share/whatomate", "backups"), backupDir(user))
}
