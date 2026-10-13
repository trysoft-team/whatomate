package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEnv builds a locateEnv rooted in a temp dir. Files listed in exist are
// created; the returned env reports goos and the given env vars.
func fakeEnv(t *testing.T, goos string, vars map[string]string, exist ...string) (locateEnv, string) {
	t.Helper()
	root := t.TempDir()
	for _, p := range exist {
		full := filepath.Join(root, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("[app]\n"), 0o600))
	}
	return locateEnv{
		goos:   goos,
		getenv: func(k string) string { return vars[k] },
		cwd:    filepath.Join(root, "cwd"),
		home:   filepath.Join(root, "home"),
		root:   root,
	}, root
}

func TestLocate_FlagWinsAndMustExist(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil, "custom.toml", "cwd/config.toml")
	src, err := locate(filepath.Join(root, "custom.toml"), e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "custom.toml"), src.Path)
	assert.Empty(t, src.BaseDir, "explicit config keeps paths relative to the working directory")

	_, err = locate(filepath.Join(root, "missing.toml"), e)
	assert.Error(t, err)
}

func TestLocate_EnvVarBeforeWorkingDir(t *testing.T) {
	e, root := fakeEnv(t, "linux", map[string]string{"WHATOMATE_CONFIG": "/env.toml"}, "env.toml", "cwd/config.toml")
	src, err := locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "env.toml"), src.Path)
	assert.Empty(t, src.BaseDir)
}

func TestLocate_EnvVarMissingFileFails(t *testing.T) {
	e, _ := fakeEnv(t, "linux", map[string]string{"WHATOMATE_CONFIG": "/nope.toml"}, "cwd/config.toml")
	_, err := locate("", e)
	assert.Error(t, err)
}

func TestLocate_WorkingDirBeforeUserAndSystem(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil, "cwd/config.toml", "home/.config/whatomate/config.toml", "etc/whatomate/config.toml")
	src, err := locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "cwd", "config.toml"), src.Path)
	assert.Empty(t, src.BaseDir)
}

func TestLocate_LinuxUserDir(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil, "home/.config/whatomate/config.toml", "etc/whatomate/config.toml")
	src, err := locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "home/.config/whatomate/config.toml"), src.Path)
	assert.Equal(t, filepath.Join(root, "home/.config/whatomate"), src.BaseDir)
}

func TestLocate_LinuxXDGConfigHome(t *testing.T) {
	e, root := fakeEnv(t, "linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "xdg/whatomate/config.toml")
	src, err := locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "xdg/whatomate/config.toml"), src.Path)
}

func TestLocate_LinuxSystemDirUsesVarLib(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil, "etc/whatomate/config.toml")
	src, err := locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "etc/whatomate/config.toml"), src.Path)
	assert.Equal(t, filepath.Join(root, "var/lib/whatomate"), src.BaseDir)
}

func TestLocate_MacOS(t *testing.T) {
	e, root := fakeEnv(t, "darwin", nil, "home/Library/Application Support/whatomate/config.toml")
	src, err := locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "home/Library/Application Support/whatomate"), src.BaseDir)

	e, root = fakeEnv(t, "darwin", nil, "home/.config/whatomate/config.toml")
	src, err = locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "home/.config/whatomate/config.toml"), src.Path)

	e, root = fakeEnv(t, "darwin", nil, "opt/homebrew/etc/whatomate/config.toml")
	src, err = locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "opt/homebrew/var/whatomate"), src.BaseDir)

	e, root = fakeEnv(t, "darwin", nil, "usr/local/etc/whatomate/config.toml")
	src, err = locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "usr/local/var/whatomate"), src.BaseDir)
}

func TestLocate_Windows(t *testing.T) {
	vars := map[string]string{"APPDATA": `/appdata`, "ProgramData": `/programdata`}
	e, root := fakeEnv(t, "windows", vars, "appdata/whatomate/config.toml")
	src, err := locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "appdata/whatomate"), src.BaseDir)

	e, root = fakeEnv(t, "windows", vars, "programdata/whatomate/config.toml")
	src, err = locate("", e)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "programdata/whatomate/config.toml"), src.Path)
	assert.Equal(t, filepath.Join(root, "programdata/whatomate"), src.BaseDir)
}

func TestLocate_NothingFoundListsSearchedPaths(t *testing.T) {
	e, root := fakeEnv(t, "linux", nil)
	_, err := locate("", e)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, filepath.Join(root, "cwd", "config.toml"))
	assert.Contains(t, msg, filepath.Join(root, "home/.config/whatomate/config.toml"))
	assert.Contains(t, msg, filepath.Join(root, "etc/whatomate/config.toml"))
	assert.Contains(t, msg, "-config")
}

func TestResolvePaths(t *testing.T) {
	cfg := &Config{}
	cfg.Database.Path = "./whatomate.db"
	cfg.Storage.LocalPath = "uploads"
	cfg.Calling.AudioDir = "audio"
	cfg.Calling.HoldMusicFile = "hold.ogg"
	cfg.TTS.PiperModel = "voices/en.onnx"
	cfg.TTS.PiperBinary = "bin/piper"
	cfg.TTS.OpusencBinary = "opusenc"

	abs := filepath.Join(t.TempDir(), "abs.db")
	cfg.ResolvePaths("")
	assert.Equal(t, "./whatomate.db", cfg.Database.Path, "empty base leaves paths alone")

	base := filepath.Join(t.TempDir(), "base")
	cfg.ResolvePaths(base)
	assert.Equal(t, filepath.Join(base, "whatomate.db"), cfg.Database.Path)
	assert.Equal(t, filepath.Join(base, "uploads"), cfg.Storage.LocalPath)
	assert.Equal(t, filepath.Join(base, "audio"), cfg.Calling.AudioDir)
	assert.Equal(t, "hold.ogg", cfg.Calling.HoldMusicFile, "relative to audio_dir, not the base")
	assert.Equal(t, filepath.Join(base, "voices/en.onnx"), cfg.TTS.PiperModel)
	assert.Equal(t, filepath.Join(base, "bin/piper"), cfg.TTS.PiperBinary)
	assert.Equal(t, "opusenc", cfg.TTS.OpusencBinary, "bare command names are looked up on PATH")

	cfg.Database.Path = abs
	cfg.ResolvePaths(base)
	assert.Equal(t, abs, cfg.Database.Path, "absolute paths are kept")
}
