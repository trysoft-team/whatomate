package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func TestIsBackupDownload(t *testing.T) {
	assert.True(t, IsBackupDownload("GET", "/api/admin/backups/a.zip/download"))
	assert.True(t, IsBackupDownload("GET", "/base/api/admin/backups/a.zip/download?x=1"))
	assert.False(t, IsBackupDownload("POST", "/api/admin/backups/a.zip/download"))
	assert.False(t, IsBackupDownload("GET", "/api/admin/backups"))
}
