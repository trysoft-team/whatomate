package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

func TestSetServerPort_OnlyServerTable(t *testing.T) {
	p := writeTemp(t, "# top comment\n[server]\nhost = \"0.0.0.0\"\nport = 8080 # keep me\n\n[database]\nport = 5432\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "# top comment\n[server]\nhost = \"0.0.0.0\"\nport = 9000 # keep me\n\n[database]\nport = 5432\n", read(t, p))
}

func TestSetServerPort_AddsKeyToExistingTable(t *testing.T) {
	p := writeTemp(t, "[server]\nhost = \"0.0.0.0\"\n[jwt]\nsecret = \"x\"\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[server]\nport = 9000\nhost = \"0.0.0.0\"\n[jwt]\nsecret = \"x\"\n", read(t, p))
}

func TestSetServerPort_AddsTable(t *testing.T) {
	p := writeTemp(t, "[app]\nname = \"x\"")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[app]\nname = \"x\"\n\n[server]\nport = 9000\n", read(t, p))
}

func TestSetServerPort_IgnoresSubTablesAndSimilarKeys(t *testing.T) {
	p := writeTemp(t, "[server]\nports = 1\nport_x = 2\n[server.extra]\nport = 3\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[server]\nport = 9000\nports = 1\nport_x = 2\n[server.extra]\nport = 3\n", read(t, p))
}

func TestSetServerPort_KeepsCRLF(t *testing.T) {
	p := writeTemp(t, "[server]\r\nport = 8080\r\n[database]\r\nport = 5432\r\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[server]\r\nport = 9000\r\n[database]\r\nport = 5432\r\n", read(t, p))
}

func TestSetServerPort_HeaderAtEndWithoutNewline(t *testing.T) {
	p := writeTemp(t, "[app]\nname = \"x\"\n[server]")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[app]\nname = \"x\"\n[server]\nport = 9000\n", read(t, p))
	assertPort(t, p, 9000)
}

func TestSetServerPort_ArrayOfTablesEndsServerTable(t *testing.T) {
	p := writeTemp(t, "[server]\nhost = \"0.0.0.0\"\n\n[[calling.ice_servers]]\nurls = [\"stun:stun.example.com\"]\nport = 3478\n")
	require.NoError(t, SetServerPort(p, 9000))
	assert.Equal(t, "[server]\nport = 9000\nhost = \"0.0.0.0\"\n\n[[calling.ice_servers]]\nurls = [\"stun:stun.example.com\"]\nport = 3478\n", read(t, p))
	assertPort(t, p, 9000)
}

// TestSetServerPort_LayoutsItCannotEdit covers files whose [server] table is
// not a plain header: each is either edited into a valid file holding the new
// port, or refused with ErrCannotEditPort and left byte-identical.
func TestSetServerPort_LayoutsItCannotEdit(t *testing.T) {
	for name, content := range map[string]string{
		"dotted key":    "server.port = 8080\nserver.host = \"0.0.0.0\"\n\n[database]\nport = 5432\n",
		"inline table":  "server = { port = 8080, host = \"0.0.0.0\" }\n\n[database]\nport = 5432\n",
		"quoted header": "[\"server\"]\nport = 8080\n\n[database]\nport = 5432\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := writeTemp(t, content)
			err := SetServerPort(p, 9000)
			if err != nil {
				assert.ErrorIs(t, err, ErrCannotEditPort)
				assert.Equal(t, content, read(t, p), "refused edits leave the file untouched")
				return
			}
			assertPort(t, p, 9000)
			cfg, err := Load(p)
			require.NoError(t, err)
			assert.Equal(t, 5432, cfg.Database.Port)
		})
	}
}

func TestSetServerPort_RefusesWhenOtherValuesWouldChange(t *testing.T) {
	// A multi-line string holding a line that looks like the port key.
	content := "[server]\nnote = \"\"\"\nport = 1\n\"\"\"\n"
	p := writeTemp(t, content)
	assert.ErrorIs(t, SetServerPort(p, 9000), ErrCannotEditPort)
	assert.Equal(t, content, read(t, p))
}

func assertPort(t *testing.T, p string, want int) {
	t.Helper()
	cfg, err := Load(p)
	require.NoError(t, err, "the edited file is valid TOML")
	assert.Equal(t, want, cfg.Server.Port)
}

func TestSetServerPort_KeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	p := writeTemp(t, "[server]\nport = 1\n")
	require.NoError(t, os.Chmod(p, 0o640))
	require.NoError(t, SetServerPort(p, 2))
	fi, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm())
}

func TestReadServerPort(t *testing.T) {
	port, err := ReadServerPort(writeTemp(t, "[server]\nport = 9100\n[database]\nport = 5432\n"))
	require.NoError(t, err)
	assert.Equal(t, 9100, port)

	port, err = ReadServerPort(writeTemp(t, "[database]\nport = 5432\n"))
	require.NoError(t, err)
	assert.Equal(t, DefaultPort, port)
}

func TestWriteFileExclusive_CreatesFolderAndMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "config.toml")
	require.NoError(t, WriteFileExclusive(p, []byte("x"), 0o600))
	assert.Equal(t, "x", read(t, p))
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left behind")
}

func TestWriteFileExclusive_NeverReplaces(t *testing.T) {
	p := writeTemp(t, "mine")
	err := WriteFileExclusive(p, []byte("theirs"), 0o600)
	assert.ErrorIs(t, err, fs.ErrExist)
	assert.Equal(t, "mine", read(t, p))
	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left behind")
}

func TestWriteFileExclusive_WithoutHardLinks(t *testing.T) {
	orig := link
	link = func(string, string) error { return errors.New("hard links not supported") }
	t.Cleanup(func() { link = orig })

	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, WriteFileExclusive(p, []byte("x"), 0o600))
	assert.Equal(t, "x", read(t, p))
	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left behind")

	assert.ErrorIs(t, WriteFileExclusive(p, []byte("y"), 0o600), fs.ErrExist)
	assert.Equal(t, "x", read(t, p))
}

func TestWriteFileAtomic_CreatesFolderAndMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "config.toml")
	require.NoError(t, WriteFileAtomic(p, []byte("x"), 0o600))
	assert.Equal(t, "x", read(t, p))
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left behind")
}
