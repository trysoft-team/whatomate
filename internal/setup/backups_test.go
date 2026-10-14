package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/valyala/fasthttp"
)

const oldKey = "old-encryption-key-0123456789abcdef"

// makeBackup writes a backup of a fresh database with one admin
// (old@example.com / old-password-1) and, with files, one media file. It
// returns the backup's path.
func makeBackup(t *testing.T, dir, key string, withFiles bool) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o750))
	src := openSQLite(t, filepath.Join(dir, "src", "src.db"))
	require.NoError(t, database.RunMigrationWithProgress(src, &config.DefaultAdminConfig{
		Email: "old@example.com", Password: "old-password-1", FullName: "Old Admin", OrganizationName: "Old Org",
	}))
	media := filepath.Join(dir, "src", "media")
	require.NoError(t, os.MkdirAll(media, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(media, "photo.jpg"), []byte("jpeg"), 0o600))
	info, err := backup.Create(context.Background(), src, filepath.Join(dir, "made"), backup.Options{
		Kind: backup.KindManual, AppVersion: "test", EncryptionKey: key, IncludeFiles: withFiles,
		Areas: backup.Areas(media, ""),
	}, nil)
	require.NoError(t, err)
	return filepath.Join(dir, "made", info.Name)
}

func putChunk(t *testing.T, s *server, id string, offset int, body []byte) (int, map[string]any) {
	t.Helper()
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod("PUT")
	ctx.Request.SetRequestURI("/api/setup/backup-uploads/" + id + "?offset=" + itoa(offset))
	ctx.Request.Header.SetHost("localhost:4747")
	ctx.Request.Header.SetContentType("application/octet-stream")
	ctx.Request.Header.Set(tokenHeader, s.token)
	ctx.Request.SetBody(body)
	s.handle(&ctx)
	var out map[string]any
	_ = jsonUnmarshal(ctx.Response.Body(), &out)
	return ctx.Response.StatusCode(), out
}

func TestSetupBackups_UploadListInspect(t *testing.T) {
	e := newEnv(t)
	src := makeBackup(t, e.dir, oldKey, true)
	b, err := os.ReadFile(src)
	require.NoError(t, err)

	code, out := call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": e.configPath, "name": "moved.zip", "size": len(b)})
	require.Equal(t, 200, code, out)
	id := data(out)["id"].(string)

	half := len(b) / 2
	code, out = putChunk(t, e.s, id, 0, b[:half])
	require.Equal(t, 200, code, out)
	code, _ = putChunk(t, e.s, id, 0, b[half:])
	assert.Equal(t, 409, code, "a chunk at the wrong offset is refused")
	code, out = putChunk(t, e.s, id, half, b[half:])
	require.Equal(t, 200, code, out)

	code, out = call(t, e.s, "POST", "/api/setup/backup-uploads/"+id+"/finish", map[string]any{})
	require.Equal(t, 200, code, out)
	assert.Equal(t, "moved.zip", data(out)["name"])
	cand, _ := e.s.lookup(e.configPath)
	assert.FileExists(t, filepath.Join(backupDir(cand), "moved.zip"))

	code, out = call(t, e.s, "GET", "/api/setup/backups?config_path="+e.configPath, nil)
	require.Equal(t, 200, code, out)
	list := data(out)["backups"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "moved.zip", list[0].(map[string]any)["name"])

	inspect := func(ref map[string]any, key string) (int, map[string]any) {
		return call(t, e.s, "POST", "/api/setup/backup/inspect", map[string]any{
			"config_path": e.configPath, "database": map[string]any{"driver": "postgres"}, "backup": ref, "encryption_key": key,
		})
	}
	code, out = inspect(map[string]any{"name": "moved.zip"}, "")
	require.Equal(t, 200, code, out)
	assert.Equal(t, "postgres", data(out)["target_driver"])
	assert.Equal(t, false, data(out)["key_given"])
	code, out = inspect(map[string]any{"name": "moved.zip"}, oldKey)
	require.Equal(t, 200, code, out)
	assert.Equal(t, true, data(out)["key_matches"])
	code, out = inspect(map[string]any{"path": src}, "wrong")
	require.Equal(t, 200, code, out)
	assert.Equal(t, false, data(out)["key_matches"])
	assert.Equal(t, true, data(out)["key_given"])
}

func TestSetupBackups_BadReferences(t *testing.T) {
	e := newEnv(t)
	for _, ref := range []map[string]any{
		{"name": "../x.zip"},
		{"name": "x.txt"},
		{"path": "relative/x.zip"},
		{"path": filepath.Join(e.dir, "x.txt")},
		{"path": filepath.Join(e.dir, "missing.zip")},
		{},
		{"name": "a.zip", "path": filepath.Join(e.dir, "a.zip")},
	} {
		code, _ := call(t, e.s, "POST", "/api/setup/backup/inspect", map[string]any{
			"config_path": e.configPath, "database": map[string]any{"driver": "sqlite"}, "backup": ref,
		})
		assert.Equal(t, 400, code, ref)
	}
}

func TestSetupBackups_UploadCancelAndLimits(t *testing.T) {
	e := newEnv(t)
	code, _ := call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": e.configPath, "name": "x.txt", "size": 5})
	assert.Equal(t, 400, code)
	code, _ = call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": "/nowhere/config.toml", "name": "x.zip", "size": 5})
	assert.Equal(t, 400, code)

	code, out := call(t, e.s, "POST", "/api/setup/backup-uploads", map[string]any{"config_path": e.configPath, "name": "x.zip", "size": 5})
	require.Equal(t, 200, code, out)
	id := data(out)["id"].(string)
	code, _ = call(t, e.s, "DELETE", "/api/setup/backup-uploads/"+id, nil)
	assert.Equal(t, 200, code)
	cand, _ := e.s.lookup(e.configPath)
	entries, _ := os.ReadDir(backupDir(cand))
	assert.Empty(t, entries)

	code, _ = call(t, e.s, "POST", "/api/setup/backup-uploads/"+id+"/finish", map[string]any{})
	assert.Equal(t, 404, code)
}

func TestIsUploadChunk(t *testing.T) {
	assert.True(t, isUploadChunk([]byte("PUT"), []byte("/api/setup/backup-uploads/abc")))
	assert.False(t, isUploadChunk([]byte("POST"), []byte("/api/setup/backup-uploads/abc/finish")))
	assert.False(t, isUploadChunk([]byte("PUT"), []byte("/api/setup/complete")))
}

func itoa(n int) string { return strconv.Itoa(n) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
