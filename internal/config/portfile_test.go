package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSetServerPort_MovesSetupOrigins(t *testing.T) {
	before := "# Written by whatomate setup on 2026-10-13.\n\n[app]\nenvironment = \"production\"\n\n" +
		"[server]\nhost = \"0.0.0.0\"\nport = 4747\nallowed_origins = \"http://localhost:4747,http://192.168.1.5:4747\"\n\n" +
		"[database]\ndriver = \"sqlite\"\npath = \"whatomate.db\"\n\n[jwt]\nsecret = \"s\"\n"
	p := writeTemp(t, before)
	require.NoError(t, SetServerPort(p, 4790))
	want := strings.Replace(before, "port = 4747\n", "port = 4790\n", 1)
	want = strings.Replace(want, "\"http://localhost:4747,http://192.168.1.5:4747\"", "\"http://localhost:4790,http://192.168.1.5:4790\"", 1)
	assert.Equal(t, want, read(t, p))
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:4790,http://192.168.1.5:4790", cfg.Server.AllowedOrigins)
}

func TestSetServerPort_MovesOnlyOriginsOnOldPort(t *testing.T) {
	p := writeTemp(t, "[server]\nport = 4747\nallowed_origins = \"https://app.example.com, http://localhost:4747 ,http://10.0.0.5:9000\"\n")
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, "[server]\nport = 4790\nallowed_origins = \"https://app.example.com,http://localhost:4790,http://10.0.0.5:9000\"\n", read(t, p))
}

func TestSetServerPort_OriginsKeptUnlessOnlySchemeHostPort(t *testing.T) {
	origins := "http://localhost:4747/,http://localhost:4747/app,http://localhost:4747?x=1,http://localhost:4747#f," +
		"http://u@localhost:4747,ws://localhost:4747,localhost:4747,http://localhost,http://localhost:04747,%zz"
	content := "[server]\nport = 4747\nallowed_origins = \"" + origins + "\"\n"
	p := writeTemp(t, content)
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, strings.Replace(content, "port = 4747", "port = 4790", 1), read(t, p))
}

func TestSetServerPort_MovesHTTPSAndIPv6Origins(t *testing.T) {
	p := writeTemp(t, "[server]\nport = 4747\nallowed_origins = \"https://example.com:4747,http://[::1]:4747\"\n")
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, "[server]\nport = 4790\nallowed_origins = \"https://example.com:4790,http://[::1]:4790\"\n", read(t, p))
}

func TestSetServerPort_OriginsOnSchemeDefaultPortOmitIt(t *testing.T) {
	for name, tc := range map[string]struct {
		port          int
		before, after string
	}{
		"http to 80":            {80, "http://localhost:4747,http://[::1]:4747", "http://localhost,http://[::1]"},
		"https to 443":          {443, "https://example.com:4747,https://[::1]:4747", "https://example.com,https://[::1]"},
		"http to 443 keeps it":  {443, "http://localhost:4747", "http://localhost:443"},
		"https to 80 keeps it":  {80, "https://example.com:4747", "https://example.com:80"},
		"dedupes after omitted": {80, "http://localhost:4747,http://localhost", "http://localhost"},
	} {
		t.Run(name, func(t *testing.T) {
			p := writeTemp(t, "[server]\nport = 4747\nallowed_origins = \""+tc.before+"\"\n")
			require.NoError(t, SetServerPort(p, tc.port))
			assert.Equal(t, fmt.Sprintf("[server]\nport = %d\nallowed_origins = \"%s\"\n", tc.port, tc.after), read(t, p))
		})
	}
}

func TestSetServerPort_DropsDuplicateOrigins(t *testing.T) {
	p := writeTemp(t, "[server]\nport = 4747\nallowed_origins = \"http://localhost:4747,http://localhost:4790\"\n")
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, "[server]\nport = 4790\nallowed_origins = \"http://localhost:4790\"\n", read(t, p))
}

func TestSetServerPort_OldPortDefaultsWhenFileHasNone(t *testing.T) {
	p := writeTemp(t, "[server]\nallowed_origins = \"http://localhost:4747\"\n")
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, "[server]\nport = 4790\nallowed_origins = \"http://localhost:4790\"\n", read(t, p))
}

func TestSetServerPort_OriginsKeepCommentAndCRLF(t *testing.T) {
	p := writeTemp(t, "[server]\r\nport = 4747\r\n  allowed_origins  =  \"http://localhost:4747\"   # browsers\r\n[database]\r\nport = 5432\r\n")
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, "[server]\r\nport = 4790\r\n  allowed_origins  =  \"http://localhost:4790\"   # browsers\r\n[database]\r\nport = 5432\r\n", read(t, p))
}

func TestSetServerPort_SamePortLeavesOrigins(t *testing.T) {
	content := "[server]\nport = 4747\nallowed_origins = \"http://localhost:4747 , http://localhost:4747\"\n"
	p := writeTemp(t, content)
	require.NoError(t, SetServerPort(p, 4747))
	assert.Equal(t, content, read(t, p))
}

func TestSetServerPort_UnchangedOriginsLineNotRewritten(t *testing.T) {
	content := "[server]\nport = 4747\nallowed_origins = \"https://app.example.com , http://10.0.0.5:9000,https://app.example.com\"\n"
	p := writeTemp(t, content)
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, strings.Replace(content, "port = 4747", "port = 4790", 1), read(t, p))
}

// TestSetServerPort_OriginsItCannotEdit covers allowed_origins forms that are
// not a single-line basic string in [server]: the port is still saved and the
// origins are left byte-identical.
func TestSetServerPort_OriginsItCannotEdit(t *testing.T) {
	for name, origins := range map[string]string{
		"literal string":    "allowed_origins = 'http://localhost:4747'\n",
		"array":             "allowed_origins = [\"http://localhost:4747\"]\n",
		"multi-line string": "allowed_origins = \"\"\"http://localhost:4747\"\"\"\n",
		"dotted key":        "server.allowed_origins = \"http://localhost:4747\"\n",
		"escaped string":    "allowed_origins = \"http://localhost:4747\\u002Chttp://x:4747\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			content := "[server]\nport = 4747\n" + origins + "\n[database]\nport = 5432\n"
			p := writeTemp(t, content)
			require.NoError(t, SetServerPort(p, 4790))
			assert.Equal(t, "[server]\nport = 4790\n"+origins+"\n[database]\nport = 5432\n", read(t, p))
		})
	}
}

func TestSetServerPort_OriginsOutsideServerTable(t *testing.T) {
	content := "[server]\nport = 4747\n\n[server.extra]\nallowed_origins = \"http://localhost:4747\"\n\n" +
		"[[calling.ice_servers]]\nallowed_origins = \"http://localhost:4747\"\n"
	p := writeTemp(t, content)
	require.NoError(t, SetServerPort(p, 4790))
	assert.Equal(t, strings.Replace(content, "port = 4747", "port = 4790", 1), read(t, p))
}

func TestSetServerPort_RefusesWhenOriginsLineIsInsideAString(t *testing.T) {
	content := "[server]\nport = 4747\nnote = \"\"\"\nallowed_origins = \"http://localhost:4747\"\n\"\"\"\n"
	p := writeTemp(t, content)
	assert.ErrorIs(t, SetServerPort(p, 4790), ErrCannotEditPort)
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
