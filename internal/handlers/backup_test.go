package handlers_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/maintenance"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// backupApp is a test app on its own SQLite database, so restores never touch
// the shared test database.
func backupApp(t *testing.T) (*handlers.App, uuid.UUID, uuid.UUID) {
	t.Helper()
	app := newTestApp(t)
	root := t.TempDir()
	db, err := database.Open(&config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(root, "w.db")}, false)
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			_ = s.Close()
		}
	})
	state := store.NewSQLKV(db)
	t.Cleanup(state.Close)
	app.DB, app.State = db, state
	app.Maintenance = &maintenance.Gate{}
	app.Version = "test"
	app.Config.Backup = config.BackupConfig{Dir: filepath.Join(root, "backups"), MaxUploadMB: 1}
	app.Config.Storage.LocalPath = filepath.Join(root, "uploads")
	app.Config.Calling.AudioDir = filepath.Join(root, "audio")
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithEmail(testutil.UniqueEmail("bk-admin")), testutil.WithSuperAdmin())
	return app, org.ID, admin.ID
}

func call(t *testing.T, h fastglue.FastRequestHandler, req *fastglue.Request, orgID, userID uuid.UUID) int {
	t.Helper()
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req)
}

func waitJob(t *testing.T, app *handlers.App, orgID, adminID uuid.UUID) handlers.BackupJob {
	t.Helper()
	var job handlers.BackupJob
	require.Eventually(t, func() bool {
		req := testutil.NewGETRequest(t)
		call(t, app.GetBackupJob, req, orgID, adminID)
		testutil.ParseEnvelopeResponse(t, req, &job)
		return !job.Running
	}, 30*time.Second, 50*time.Millisecond)
	return job
}

func TestBackupAPI_SuperAdminOnly(t *testing.T) {
	app, orgID, _ := backupApp(t)
	user := testutil.CreateTestUser(t, app.DB, orgID, testutil.WithEmail(testutil.UniqueEmail("bk-user")))
	for name, h := range map[string]fastglue.FastRequestHandler{
		"list": app.ListBackups, "create": app.CreateBackup, "job": app.GetBackupJob, "download": app.DownloadBackup,
		"inspect": app.InspectBackup, "restore": app.RestoreBackup, "delete": app.DeleteBackup, "upload": app.StartBackupUpload,
	} {
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetPathParam(req, "name", "x.zip")
		assert.Equal(t, fasthttp.StatusForbidden, call(t, h, req, orgID, user.ID), name)
	}
}

func TestBackupAPI_CreateListDownloadDelete(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	require.Equal(t, fasthttp.StatusOK, call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID))
	job := waitJob(t, app, orgID, adminID)
	require.Empty(t, job.Error)
	require.NotEmpty(t, job.Name)

	list := testutil.NewGETRequest(t)
	call(t, app.ListBackups, list, orgID, adminID)
	var resp struct {
		Dir     string        `json:"dir"`
		Backups []backup.Info `json:"backups"`
	}
	testutil.ParseEnvelopeResponse(t, list, &resp)
	require.Len(t, resp.Backups, 1)
	assert.Equal(t, job.Name, resp.Backups[0].Name)

	dl := testutil.NewGETRequest(t)
	testutil.SetPathParam(dl, "name", job.Name)
	assert.Equal(t, fasthttp.StatusOK, call(t, app.DownloadBackup, dl, orgID, adminID))
	assert.Contains(t, string(dl.RequestCtx.Response.Header.Peek("Content-Disposition")), job.Name)
	assert.True(t, bytes.HasPrefix(dl.RequestCtx.Response.Body(), []byte("PK")))

	for _, bad := range []string{"../w.db", "x.txt", "missing.zip"} {
		req := testutil.NewGETRequest(t)
		testutil.SetPathParam(req, "name", bad)
		assert.Equal(t, fasthttp.StatusNotFound, call(t, app.DownloadBackup, req, orgID, adminID), bad)
	}

	del := testutil.NewRequest(t)
	testutil.SetPathParam(del, "name", job.Name)
	assert.Equal(t, fasthttp.StatusOK, call(t, app.DeleteBackup, del, orgID, adminID))
	assert.NoFileExists(t, filepath.Join(app.Config.Backup.Dir, job.Name))
}

func TestBackupAPI_Restore(t *testing.T) {
	handlers.SetRestoreGraceForTest(t, 10*time.Millisecond)
	app, orgID, adminID := backupApp(t)
	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": true}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)
	later := models.Organization{Name: "later", Slug: "later-" + uuid.NewString()[:8]}
	require.NoError(t, app.DB.Create(&later).Error)

	noConfirm := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetPathParam(noConfirm, "name", made.Name)
	assert.Equal(t, fasthttp.StatusBadRequest, call(t, app.RestoreBackup, noConfirm, orgID, adminID))

	insp := testutil.NewGETRequest(t)
	testutil.SetPathParam(insp, "name", made.Name)
	assert.Equal(t, fasthttp.StatusOK, call(t, app.InspectBackup, insp, orgID, adminID))

	req := testutil.NewJSONRequest(t, map[string]any{"confirm": "RESTORE"})
	testutil.SetPathParam(req, "name", made.Name)
	require.Equal(t, fasthttp.StatusOK, call(t, app.RestoreBackup, req, orgID, adminID))
	job := waitJob(t, app, orgID, adminID)
	require.Empty(t, job.Error)
	assert.NotEmpty(t, job.SafetyName)
	assert.False(t, app.Maintenance.Active())

	var n int64
	require.NoError(t, app.DB.Model(&models.Organization{}).Where("id = ?", later.ID).Count(&n).Error)
	assert.Zero(t, n)
	m, err := backup.ReadManifest(filepath.Join(app.Config.Backup.Dir, job.SafetyName))
	require.NoError(t, err)
	assert.Equal(t, backup.KindPreRestore, m.Kind)

	pub := testutil.NewGETRequest(t)
	require.NoError(t, app.GetMaintenance(pub))
	var st handlers.MaintenanceStatus
	testutil.ParseEnvelopeResponse(t, pub, &st)
	require.NotNil(t, st.Restore)
	assert.True(t, st.Restore.Finished)
	assert.False(t, st.Restore.Failed)
	assert.True(t, st.Restore.DBReplaced)
}

func TestBackupAPI_ShutdownWaitsForRestore(t *testing.T) {
	handlers.SetRestoreGraceForTest(t, 200*time.Millisecond)
	app, orgID, adminID := backupApp(t)
	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)

	req := testutil.NewJSONRequest(t, map[string]any{"confirm": "RESTORE"})
	testutil.SetPathParam(req, "name", made.Name)
	require.Equal(t, fasthttp.StatusOK, call(t, app.RestoreBackup, req, orgID, adminID))
	kind, running := app.RunningBackupJob()
	require.True(t, running)
	assert.Equal(t, "restore", kind)
	assert.True(t, app.WaitForRestore(nil))
	_, running = app.RunningBackupJob()
	assert.False(t, running, "shutdown waits for the restore")
	assert.False(t, app.Maintenance.Active())
}

func TestBackupAPI_ShutdownStopsWaitingOnSecondSignal(t *testing.T) {
	handlers.SetRestoreGraceForTest(t, 2*time.Second)
	app, orgID, adminID := backupApp(t)
	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)

	req := testutil.NewJSONRequest(t, map[string]any{"confirm": "RESTORE"})
	testutil.SetPathParam(req, "name", made.Name)
	require.Equal(t, fasthttp.StatusOK, call(t, app.RestoreBackup, req, orgID, adminID))
	stop := make(chan struct{})
	close(stop)
	started := time.Now()
	assert.False(t, app.WaitForRestore(stop))
	assert.Less(t, time.Since(started), time.Second)
	_, running := app.RunningBackupJob()
	assert.True(t, running, "the restore is still running")
	require.True(t, app.WaitForRestore(nil))
}

func TestBackupAPI_ShutdownDoesNotWaitForBackup(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	release := make(chan struct{})
	handlers.SetBackupHookForTest(t, func() { <-release })
	require.Equal(t, fasthttp.StatusOK, call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID))
	kind, running := app.RunningBackupJob()
	require.True(t, running)
	require.Equal(t, "backup", kind)
	assert.True(t, app.WaitForRestore(nil), "a running backup is not waited for")
	_, running = app.RunningBackupJob()
	assert.True(t, running)
	close(release)
	waitJob(t, app, orgID, adminID)
}

func TestBackupAPI_CreateRefusesWithoutSpace(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	var asked int64
	handlers.SetEnsureSpaceForTest(t, func(dir string, need int64) error {
		asked = need
		return errors.New("not enough disk space in " + dir)
	})
	req := testutil.NewJSONRequest(t, map[string]any{"include_files": true})
	assert.Equal(t, fasthttp.StatusInsufficientStorage, call(t, app.CreateBackup, req, orgID, adminID))
	assert.Contains(t, string(req.RequestCtx.Response.Body()), "not enough disk space")
	assert.Positive(t, asked)
	_, running := app.RunningBackupJob()
	assert.False(t, running)
	entries, _ := os.ReadDir(app.Config.Backup.Dir)
	assert.Empty(t, entries)
}

func TestBackupAPI_LeavesConfigAndDatabaseFilesAlone(t *testing.T) {
	handlers.SetRestoreGraceForTest(t, 10*time.Millisecond)
	app, orgID, adminID := backupApp(t)
	root := filepath.Dir(app.Config.Backup.Dir)
	app.Config.Storage.LocalPath = root
	app.Config.Database = config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(root, "w.db")}
	app.ConfigPath = filepath.Join(root, "config.toml")
	require.NoError(t, os.WriteFile(app.ConfigPath, []byte("before"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "media.txt"), []byte("m1"), 0o600))

	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": true}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)
	require.Empty(t, made.Error)
	zr, err := zip.OpenReader(filepath.Join(app.Config.Backup.Dir, made.Name))
	require.NoError(t, err)
	var files []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "files/") {
			files = append(files, f.Name)
		}
	}
	require.NoError(t, zr.Close())
	assert.Equal(t, []string{"files/media/media.txt"}, files)

	require.NoError(t, os.WriteFile(app.ConfigPath, []byte("after"), 0o600))
	req := testutil.NewJSONRequest(t, map[string]any{"confirm": "RESTORE"})
	testutil.SetPathParam(req, "name", made.Name)
	require.Equal(t, fasthttp.StatusOK, call(t, app.RestoreBackup, req, orgID, adminID))
	job := waitJob(t, app, orgID, adminID)
	require.Empty(t, job.Error)
	b, err := os.ReadFile(app.ConfigPath)
	require.NoError(t, err)
	assert.Equal(t, "after", string(b))
	assert.FileExists(t, filepath.Join(root, "w.db"))
	var n int64
	require.NoError(t, app.DB.Model(&models.Organization{}).Count(&n).Error, "the live database file was not moved away")
	assert.Positive(t, n)
}

// corruptTable rewrites the backup with one table's rows changed, so its
// checksum no longer matches the manifest.
func corruptTable(t *testing.T, path, table string) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	require.NoError(t, err)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		if f.Name == "db/"+table+".jsonl" {
			data = append(data, []byte("{}\n")...)
		}
		w, err := zw.Create(f.Name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zr.Close())
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

func TestBackupAPI_RestoreFailsBeforeDatabase(t *testing.T) {
	handlers.SetRestoreGraceForTest(t, 10*time.Millisecond)
	app, orgID, adminID := backupApp(t)
	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)
	require.Empty(t, made.Error)
	corruptTable(t, filepath.Join(app.Config.Backup.Dir, made.Name), "organizations")
	later := models.Organization{Name: "later", Slug: "later-" + uuid.NewString()[:8]}
	require.NoError(t, app.DB.Create(&later).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"confirm": "RESTORE"})
	testutil.SetPathParam(req, "name", made.Name)
	require.Equal(t, fasthttp.StatusOK, call(t, app.RestoreBackup, req, orgID, adminID))
	job := waitJob(t, app, orgID, adminID)
	require.NotEmpty(t, job.Error)
	assert.False(t, job.DBReplaced)

	var n int64
	require.NoError(t, app.DB.Model(&models.Organization{}).Where("id = ?", later.ID).Count(&n).Error)
	assert.Equal(t, int64(1), n)

	pub := testutil.NewGETRequest(t)
	require.NoError(t, app.GetMaintenance(pub))
	var st handlers.MaintenanceStatus
	testutil.ParseEnvelopeResponse(t, pub, &st)
	require.NotNil(t, st.Restore)
	assert.True(t, st.Restore.Failed)
	assert.False(t, st.Restore.DBReplaced)
}

func TestMaintenanceGuard(t *testing.T) {
	app, _, _ := backupApp(t)
	pass := func(path string) bool {
		req := testutil.NewGETRequest(t)
		req.RequestCtx.Request.SetRequestURI(path)
		return app.MaintenanceGuard(req) != nil
	}
	assert.True(t, pass("/api/contacts"))
	app.Maintenance.Set(true)
	assert.False(t, pass("/api/contacts"))
	assert.False(t, pass("/ws"))
	assert.False(t, pass("/public/chatbot-media/x"))
	assert.True(t, pass("/api/maintenance"))
	assert.True(t, pass("/health"))
	assert.True(t, pass("/settings/backup"))
}

func TestBackupAPI_ChunkedUpload(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	call(t, app.CreateBackup, testutil.NewJSONRequest(t, map[string]any{"include_files": false}), orgID, adminID)
	made := waitJob(t, app, orgID, adminID)
	data, err := os.ReadFile(filepath.Join(app.Config.Backup.Dir, made.Name))
	require.NoError(t, err)

	start := testutil.NewJSONRequest(t, map[string]any{"name": "From other server.zip", "size": len(data)})
	require.Equal(t, fasthttp.StatusOK, call(t, app.StartBackupUpload, start, orgID, adminID))
	var up struct {
		ID string `json:"id"`
	}
	testutil.ParseEnvelopeResponse(t, start, &up)

	put := func(offset int, chunk []byte) int {
		req := testutil.NewRequest(t)
		req.RequestCtx.Request.Header.SetMethod("PUT")
		req.RequestCtx.Request.SetBody(chunk)
		testutil.SetPathParam(req, "id", up.ID)
		testutil.SetQueryParam(req, "offset", offset)
		return call(t, app.PutBackupUploadChunk, req, orgID, adminID)
	}
	half := len(data) / 2
	assert.Equal(t, fasthttp.StatusOK, put(0, data[:half]))
	assert.Equal(t, fasthttp.StatusConflict, put(0, data[:half]), "wrong offset")
	assert.Equal(t, fasthttp.StatusOK, put(half, data[half:]))

	fin := testutil.NewJSONRequest(t, nil)
	testutil.SetPathParam(fin, "id", up.ID)
	require.Equal(t, fasthttp.StatusOK, call(t, app.FinishBackupUpload, fin, orgID, adminID))
	var info backup.Info
	testutil.ParseEnvelopeResponse(t, fin, &info)
	assert.Equal(t, "From-other-server.zip", info.Name)

	// too large for max_upload_mb = 1
	big := testutil.NewJSONRequest(t, map[string]any{"name": "x.zip", "size": 2 << 20})
	assert.Equal(t, fasthttp.StatusRequestEntityTooLarge, call(t, app.StartBackupUpload, big, orgID, adminID))

	// garbage is refused and removed
	g := testutil.NewJSONRequest(t, map[string]any{"name": "junk.zip", "size": 5})
	call(t, app.StartBackupUpload, g, orgID, adminID)
	testutil.ParseEnvelopeResponse(t, g, &up)
	assert.Equal(t, fasthttp.StatusOK, put(0, []byte("hello")))
	fin2 := testutil.NewJSONRequest(t, nil)
	testutil.SetPathParam(fin2, "id", up.ID)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, call(t, app.FinishBackupUpload, fin2, orgID, adminID))
	assert.NoFileExists(t, filepath.Join(app.Config.Backup.Dir, "junk.zip"))
}

func TestBackupAPI_UploadLimitDefaultsWhenUnset(t *testing.T) {
	app, orgID, adminID := backupApp(t)
	app.Config.Backup.MaxUploadMB = 0
	start := testutil.NewJSONRequest(t, map[string]any{"name": "x.zip", "size": 2 << 20})
	require.Equal(t, fasthttp.StatusOK, call(t, app.StartBackupUpload, start, orgID, adminID))
	var up struct {
		ID string `json:"id"`
	}
	testutil.ParseEnvelopeResponse(t, start, &up)

	cancel := testutil.NewRequest(t)
	testutil.SetPathParam(cancel, "id", up.ID)
	assert.Equal(t, fasthttp.StatusOK, call(t, app.CancelBackupUpload, cancel, orgID, adminID))
	entries, err := os.ReadDir(app.Config.Backup.Dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
