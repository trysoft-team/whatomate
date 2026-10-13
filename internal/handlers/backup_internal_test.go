package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/maintenance"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/trysoft/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
)

// SetRestoreGraceForTest shortens the pause before a restore for the test.
func SetRestoreGraceForTest(t *testing.T, d time.Duration) {
	old := restoreGrace
	restoreGrace = d
	t.Cleanup(func() { restoreGrace = old })
}

// SetEnsureSpaceForTest replaces the free disk check for the test.
func SetEnsureSpaceForTest(t *testing.T, fn func(dir string, need int64) error) {
	old := ensureSpace
	ensureSpace = fn
	t.Cleanup(func() { ensureSpace = old })
}

func TestGoBackupJob_PanicFailsJob(t *testing.T) {
	a := &App{Log: testutil.NopLogger(), Maintenance: &maintenance.Gate{}}
	require.True(t, a.backupJob.start("restore", "x.zip"))
	a.Maintenance.Set(true)
	a.goBackupJob(func() { panic("boom") })
	a.WaitForBackgroundTasks()
	job := a.backupJob.get()
	assert.False(t, job.Running)
	assert.Equal(t, "failed", job.Phase)
	assert.Contains(t, job.Error, "boom")
	assert.False(t, a.Maintenance.Active())
}

func TestRunningBackupJob(t *testing.T) {
	a := &App{}
	_, ok := a.RunningBackupJob()
	assert.False(t, ok)
	a.backupJob.start("restore", "x.zip")
	kind, ok := a.RunningBackupJob()
	assert.True(t, ok)
	assert.Equal(t, "restore", kind)
}

func TestClearRestoreState(t *testing.T) {
	ctx := context.Background()
	cache, state := store.NewMemory(), store.NewMemory()
	t.Cleanup(cache.Close)
	t.Cleanup(state.Close)
	require.NoError(t, cache.Set(ctx, settingsCachePrefix+"x", []byte("1"), 0))
	require.NoError(t, cache.Set(ctx, "ivr_flow:y", []byte("1"), 0))
	require.NoError(t, cache.Set(ctx, "unrelated", []byte("1"), 0))
	require.NoError(t, state.Set(ctx, "refresh:abc", []byte("1"), 0))
	require.NoError(t, state.Set(ctx, "oauth:keep", []byte("1"), 0))
	ClearRestoreState(ctx, cache, state, testutil.NopLogger())
	for _, k := range []string{settingsCachePrefix + "x", "ivr_flow:y"} {
		_, err := cache.Get(ctx, k)
		assert.ErrorIs(t, err, store.ErrNotFound, k)
	}
	_, err := state.Get(ctx, "refresh:abc")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = cache.Get(ctx, "unrelated")
	assert.NoError(t, err)
	_, err = state.Get(ctx, "oauth:keep")
	assert.NoError(t, err)
	ClearRestoreState(ctx, nil, nil, testutil.NopLogger())
}

func TestLongUploadRead(t *testing.T) {
	h := func(method, uri string, set func(*fasthttp.RequestHeader)) *fasthttp.RequestHeader {
		var hdr fasthttp.RequestHeader
		hdr.SetMethod(method)
		hdr.SetRequestURI(uri)
		if set != nil {
			set(&hdr)
		}
		return &hdr
	}
	cookie := func(hdr *fasthttp.RequestHeader) { hdr.SetCookie("whm_access", "tok") }
	bearer := func(hdr *fasthttp.RequestHeader) { hdr.Set("Authorization", "Bearer tok") }
	assert.True(t, LongUploadRead(h("PUT", "/api/admin/backup-uploads/abc?offset=0", cookie)))
	assert.True(t, LongUploadRead(h("PUT", "/api/admin/backup-uploads/abc", bearer)))
	assert.False(t, LongUploadRead(h("PUT", "/api/admin/backup-uploads/abc", nil)), "no credentials, no long timeout")
	assert.False(t, LongUploadRead(h("PUT", "/api/admin/backup-uploads/abc", func(hdr *fasthttp.RequestHeader) { hdr.SetCookie("whm_access", "") })))
	assert.False(t, LongUploadRead(h("POST", "/api/admin/backup-uploads/abc/finish", cookie)))
	assert.False(t, LongUploadRead(h("PUT", "/api/contacts/abc", bearer)))
}

func TestBackupJobs_OneAtATime(t *testing.T) {
	var j backupJobs
	assert.True(t, j.start("backup", ""))
	assert.False(t, j.start("restore", "x.zip"))
	j.update(func(b *BackupJob) { b.DBReplaced = true })
	j.finish(errors.New("boom"))
	got := j.get()
	assert.False(t, got.Running)
	assert.Equal(t, "boom", got.Error)
	assert.True(t, j.start("restore", "x.zip"))
	assert.False(t, j.get().DBReplaced)
}

func maintenanceStatus(t *testing.T, a *App) MaintenanceStatus {
	t.Helper()
	req := testutil.NewGETRequest(t)
	require.NoError(t, a.GetMaintenance(req))
	var st MaintenanceStatus
	testutil.ParseEnvelopeResponse(t, req, &st)
	return st
}

func TestGetMaintenance_LongRestoreStaysVisible(t *testing.T) {
	a := &App{}
	a.backupJob.job = BackupJob{Kind: "restore", Running: true, Phase: "load", StartedAt: time.Now().Add(-2 * time.Hour)}
	st := maintenanceStatus(t, a)
	require.NotNil(t, st.Restore)
	assert.True(t, st.Restore.Running)
	assert.False(t, st.Restore.Finished)
}

func TestGetMaintenance_OldRestoreHidden(t *testing.T) {
	a := &App{}
	finished := time.Now().Add(-2 * time.Hour)
	a.backupJob.job = BackupJob{Kind: "restore", Phase: "done", StartedAt: finished.Add(-time.Minute), FinishedAt: &finished}
	assert.Nil(t, maintenanceStatus(t, a).Restore)

	recent := time.Now().Add(-time.Minute)
	a.backupJob.job.FinishedAt = &recent
	st := maintenanceStatus(t, a)
	require.NotNil(t, st.Restore)
	assert.True(t, st.Restore.Finished)
}

func TestIsBackupDownload(t *testing.T) {
	assert.True(t, IsBackupDownload("GET", "/api/admin/backups/a.zip/download"))
	assert.True(t, IsBackupDownload("GET", "/base/api/admin/backups/a.zip/download?x=1"))
	assert.False(t, IsBackupDownload("POST", "/api/admin/backups/a.zip/download"))
	assert.False(t, IsBackupDownload("GET", "/api/admin/backups"))
}

func TestIsBackupUploadChunk(t *testing.T) {
	assert.True(t, IsBackupUploadChunk("PUT", "/api/admin/backup-uploads/abc?offset=0"))
	assert.True(t, IsBackupUploadChunk("PUT", "/base/api/admin/backup-uploads/abc"))
	assert.False(t, IsBackupUploadChunk("POST", "/api/admin/backup-uploads/abc/finish"))
	assert.False(t, IsBackupUploadChunk("PUT", "/api/admin/backup-uploads"))
	assert.False(t, IsBackupUploadChunk("PUT", "/api/admin/server-settings?x=/api/admin/backup-uploads/a"))
}
