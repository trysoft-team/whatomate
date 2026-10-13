package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/test/testutil"
)

// SetRestoreGraceForTest shortens the pause before a restore for the test.
func SetRestoreGraceForTest(t *testing.T, d time.Duration) {
	old := restoreGrace
	restoreGrace = d
	t.Cleanup(func() { restoreGrace = old })
}

func TestBackupJobs_OneAtATime(t *testing.T) {
	var j backupJobs
	assert.True(t, j.start("backup", ""))
	assert.False(t, j.start("restore", "x.zip"))
	j.finish(errors.New("boom"))
	got := j.get()
	assert.False(t, got.Running)
	assert.Equal(t, "boom", got.Error)
	assert.True(t, j.start("restore", "x.zip"))
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
