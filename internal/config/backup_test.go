package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupDefaultsAndPaths(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte("[app]\nname = \"x\"\n"), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "backups", cfg.Backup.Dir)
	assert.Equal(t, 10240, cfg.Backup.MaxUploadMB)

	base := filepath.Join(string(filepath.Separator), "data", "whatomate")
	cfg.ResolvePaths(base)
	assert.Equal(t, filepath.Join(base, "backups"), cfg.Backup.Dir)

	abs := filepath.Join(string(filepath.Separator), "srv", "bk")
	cfg.Backup.Dir = abs
	cfg.ResolvePaths(base)
	assert.Equal(t, abs, cfg.Backup.Dir)
}

func TestBackupSettingsFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte("[backup]\ndir = \"/x/y\"\nmax_upload_mb = 50\n"), 0o600))
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "/x/y", cfg.Backup.Dir)
	assert.Equal(t, 50, cfg.Backup.MaxUploadMB)
}
