package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"github.com/zerodha/logf"
)

// restoreGrace lets processor ticks and recipient jobs that started just
// before maintenance mode finish; the gate is only checked when they start.
var restoreGrace = 2 * time.Second

// BackupJob is the running or most recent backup or restore.
type BackupJob struct {
	Kind       string     `json:"kind"` // "backup" or "restore"
	Running    bool       `json:"running"`
	Phase      string     `json:"phase"`
	Table      string     `json:"table,omitempty"`
	Done       int64      `json:"done"`
	Total      int64      `json:"total"`
	Name       string     `json:"name,omitempty"`
	SafetyName string     `json:"safety_name,omitempty"`
	Error      string     `json:"error,omitempty"`
	DBReplaced bool       `json:"db_replaced,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// backupJobs allows one backup or restore at a time.
type backupJobs struct {
	mu  sync.Mutex
	job BackupJob
	// done is closed when the running job finishes.
	done chan struct{}
}

func (j *backupJobs) start(kind, name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job.Running {
		return false
	}
	j.job = BackupJob{Kind: kind, Running: true, Name: name, StartedAt: time.Now().UTC()}
	j.done = make(chan struct{})
	return true
}

func (j *backupJobs) progress(p backup.Progress) {
	j.mu.Lock()
	j.job.Phase, j.job.Table, j.job.Done, j.job.Total = p.Phase, p.Table, p.Done, p.Total
	j.mu.Unlock()
}

func (j *backupJobs) update(fn func(*BackupJob)) {
	j.mu.Lock()
	fn(&j.job)
	j.mu.Unlock()
}

func (j *backupJobs) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now().UTC()
	j.job.Running, j.job.FinishedAt = false, &now
	if err != nil {
		j.job.Error = err.Error()
		j.job.Phase = "failed"
	} else {
		j.job.Phase = "done"
	}
	if j.done != nil {
		close(j.done)
		j.done = nil
	}
}

func (j *backupJobs) get() BackupJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.job
}

func (a *App) backupAreas() []backup.FileArea {
	return backup.Areas(a.getMediaStoragePath(), a.Config.Calling.AudioDir)
}

func (a *App) backupSkip() []string {
	return backup.ProtectedPaths(a.Config.Backup.Dir, a.ConfigPath, a.Config.Database)
}

func (a *App) backupOptions(kind string, includeFiles bool) backup.Options {
	return backup.Options{
		Kind: kind, AppVersion: a.Version, EncryptionKey: a.Config.App.EncryptionKey,
		IncludeFiles: includeFiles, Areas: a.backupAreas(), Skip: a.backupSkip(), Warn: a.Log.Warn,
	}
}

var (
	ensureSpace  = backup.EnsureSpace
	beforeBackup = func() {}
)

// goBackupJob runs fn in the background. A panic fails the job and turns
// maintenance mode off.
func (a *App) goBackupJob(fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				a.Log.Error("Backup or restore crashed", "panic", p, "stack", string(debug.Stack()))
				a.Maintenance.Set(false)
				a.backupJob.finish(fmt.Errorf("internal error: %v", p))
			}
		}()
		fn()
	}()
}

// RunningBackupJob returns the kind ("backup" or "restore") of the job that
// is running, if any.
func (a *App) RunningBackupJob() (string, bool) {
	j := a.backupJob.get()
	return j.Kind, j.Running
}

// WaitForRestore blocks until a running restore finishes or stop is closed,
// and reports whether it finished. A running backup is not waited for.
func (a *App) WaitForRestore(stop <-chan struct{}) bool {
	a.backupJob.mu.Lock()
	done := a.backupJob.done
	restoring := a.backupJob.job.Running && a.backupJob.job.Kind == "restore"
	a.backupJob.mu.Unlock()
	if !restoring || done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-stop:
		return false
	}
}

// ListBackups returns the saved backups, the backup folder and the current job.
func (a *App) ListBackups(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	list, err := backup.List(a.Config.Backup.Dir)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, fmt.Sprintf("Could not read %s: %v", a.Config.Backup.Dir, err), nil, "")
	}
	if list == nil {
		list = []backup.Info{}
	}
	return r.SendEnvelope(map[string]any{"dir": a.Config.Backup.Dir, "backups": list, "job": a.backupJob.get()})
}

// CreateBackup starts a backup in the background.
func (a *App) CreateBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	var req struct {
		IncludeFiles *bool `json:"include_files"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	includeFiles := req.IncludeFiles == nil || *req.IncludeFiles
	if _, running := a.RunningBackupJob(); running {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "A backup or restore is already running", nil, "")
	}
	opts := a.backupOptions(backup.KindManual, includeFiles)
	if err := ensureSpace(a.Config.Backup.Dir, backup.EstimateSize(a.DB, opts.Areas, includeFiles, opts.Skip)); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInsufficientStorage, err.Error(), nil, "")
	}
	if !a.backupJob.start("backup", "") {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "A backup or restore is already running", nil, "")
	}
	a.goBackupJob(func() {
		beforeBackup()
		info, err := backup.Create(context.Background(), a.DB, a.Config.Backup.Dir, opts, a.backupJob.progress)
		if err != nil {
			a.Log.Error("Backup failed", "error", err)
		} else {
			a.Log.Info("Backup written", "name", info.Name, "size", info.Size)
			a.backupJob.update(func(j *BackupJob) { j.Name = info.Name })
		}
		a.backupJob.finish(err)
	})
	return r.SendEnvelope(a.backupJob.get())
}

// GetBackupJob returns the running or most recent backup or restore.
func (a *App) GetBackupJob(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	return r.SendEnvelope(a.backupJob.get())
}

// backupPath resolves the {name} path parameter to a file in the backup
// folder, or answers 404.
func (a *App) backupPath(r *fastglue.Request) (string, bool) {
	name, _ := r.RequestCtx.UserValue("name").(string)
	if !backup.ValidName(name) {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Backup not found", nil, "")
		return "", false
	}
	p := filepath.Join(a.Config.Backup.Dir, name)
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Backup not found", nil, "")
		return "", false
	}
	return p, true
}

// IsBackupDownload reports whether a request downloads a backup; those get a
// long write timeout.
func IsBackupDownload(method, uri string) bool {
	path, _, _ := strings.Cut(uri, "?")
	return method == fasthttp.MethodGet && strings.Contains(path, "/api/admin/backups/") && strings.HasSuffix(path, "/download")
}

// IsBackupUploadChunk reports whether a request sends a backup upload chunk.
func IsBackupUploadChunk(method, uri string) bool {
	path, _, _ := strings.Cut(uri, "?")
	return method == fasthttp.MethodPut && strings.Contains(path, "/api/admin/backup-uploads/")
}

// LongUploadRead reports whether a request gets the long read timeout for
// backup upload chunks: only chunks that carry credentials, since the timeout
// is granted before authentication runs.
func LongUploadRead(h *fasthttp.RequestHeader) bool {
	if !IsBackupUploadChunk(string(h.Method()), string(h.RequestURI())) {
		return false
	}
	return len(h.Cookie(cookieAccessName)) > 0 || len(h.Peek(fasthttp.HeaderAuthorization)) > 0
}

// DownloadBackup streams a backup file.
func (a *App) DownloadBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	fasthttp.ServeFileUncompressed(r.RequestCtx, p)
	r.RequestCtx.Response.Header.Set("Content-Type", "application/zip")
	r.RequestCtx.Response.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(p)))
	return nil
}

// InspectBackup describes what restoring a backup would do.
func (a *App) InspectBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	sum, err := backup.Inspect(p, a.DB, a.Config.App.EncryptionKey)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, err.Error(), nil, "")
	}
	return r.SendEnvelope(sum)
}

// RestoreBackup starts a restore after the typed confirmation.
func (a *App) RestoreBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Confirm != "RESTORE" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Type RESTORE to confirm", nil, "")
	}
	sum, err := backup.Inspect(p, a.DB, a.Config.App.EncryptionKey)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, err.Error(), nil, "")
	}
	// The safety backup comes first, then the backup's files are staged.
	need := backup.EstimateSize(a.DB, a.backupAreas(), sum.Manifest.IncludeFiles, a.backupSkip())
	if sum.Manifest.IncludeFiles {
		for _, st := range sum.Manifest.Files {
			need += st.Bytes
		}
	}
	if err := ensureSpace(a.Config.Backup.Dir, need); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInsufficientStorage, err.Error(), nil, "")
	}
	if !a.backupJob.start("restore", sum.Name) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "A backup or restore is already running", nil, "")
	}
	a.goBackupJob(func() { a.runRestore(p, sum) })
	return r.SendEnvelope(a.backupJob.get())
}

func (a *App) runRestore(path string, sum backup.Summary) {
	ctx := context.Background()
	safety, err := backup.Create(ctx, a.DB, a.Config.Backup.Dir, a.backupOptions(backup.KindPreRestore, sum.Manifest.IncludeFiles), func(p backup.Progress) {
		p.Phase = "safety_backup"
		a.backupJob.progress(p)
	})
	if err != nil {
		a.Log.Error("Safety backup before restore failed", "error", err)
		a.backupJob.finish(fmt.Errorf("the safety backup failed, so nothing was changed: %w", err))
		return
	}
	a.backupJob.update(func(j *BackupJob) { j.SafetyName = safety.Name })

	a.Maintenance.Set(true)
	if a.WSHub != nil {
		a.WSHub.CloseAll()
	}
	time.Sleep(restoreGrace)
	err = backup.Restore(ctx, a.DB, path, backup.RestoreOptions{
		EncryptionKey: a.Config.App.EncryptionKey, Areas: a.backupAreas(), Skip: a.backupSkip(),
	}, a.backupJob.progress)
	if err == nil || errors.Is(err, backup.ErrFiles) {
		clearCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ClearRestoreState(clearCtx, a.Cache, a.State, a.Log)
		cancel()
		a.backupJob.update(func(j *BackupJob) { j.DBReplaced = true })
	}
	a.Maintenance.Set(false)
	if errors.Is(err, backup.ErrFiles) {
		err = fmt.Errorf("%w. Restore %s to undo", err, safety.Name)
	}
	if err != nil {
		a.Log.Error("Restore failed", "backup", sum.Name, "error", err)
	} else {
		a.Log.Info("Restore finished", "backup", sum.Name, "safety_backup", safety.Name)
	}
	a.backupJob.finish(err)
}

// restoreCachePrefixes are every cache key prefix that can hold data from
// the database; a restore clears them all.
var restoreCachePrefixes = []string{
	settingsCachePrefix, flowsCachePrefix, keywordRulesCachePrefix, whatsappAccountCachePrefix,
	webhooksCachePrefix, slaSettingsCacheKey, aiContextsCachePrefix, userPermissionsCachePrefix,
	rolePermissionsCachePrefix, tagsCachePrefix, metaAnalyticsCachePrefix, metaWABACurrencyCachePrefix,
	"team:assignment:", "ivr_flow:", "org:calling_settings:",
}

// ClearRestoreState drops the caches that can hold data from the database and
// signs everyone out, after a restore. Either store may be nil.
func ClearRestoreState(ctx context.Context, cache, state store.KV, log logf.Logger) {
	if cache != nil {
		for _, p := range restoreCachePrefixes {
			if err := cache.DelPrefix(ctx, p); err != nil {
				log.Warn("Could not clear cache after restore", "prefix", p, "error", err)
			}
		}
	}
	if state != nil {
		if err := state.DelPrefix(ctx, "refresh:"); err != nil {
			log.Warn("Could not sign out sessions after restore", "error", err)
		}
	}
}

// DeleteBackup removes a backup file.
func (a *App) DeleteBackup(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	p, ok := a.backupPath(r)
	if !ok {
		return nil
	}
	if job := a.backupJob.get(); job.Running && job.Name == filepath.Base(p) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "That backup is being restored", nil, "")
	}
	if err := os.Remove(p); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]string{"deleted": filepath.Base(p)})
}

// RestoreStatus is the public view of a restore: no error text, no names.
type RestoreStatus struct {
	Running  bool   `json:"running"`
	Phase    string `json:"phase"`
	Table    string `json:"table,omitempty"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
	Finished bool   `json:"finished"`
	Failed   bool   `json:"failed"`
	// DBReplaced is true once the database holds the backup's data, even if
	// putting the files back then failed.
	DBReplaced bool `json:"db_replaced"`
}

// MaintenanceStatus answers GET /api/maintenance.
type MaintenanceStatus struct {
	Active  bool           `json:"active"`
	Restore *RestoreStatus `json:"restore,omitempty"`
}

// GetMaintenance reports maintenance mode and, during and for an hour after a
// restore, its progress. Public: the admin who started a restore may not
// exist in the backup.
func (a *App) GetMaintenance(r *fastglue.Request) error {
	st := MaintenanceStatus{Active: a.Maintenance.Active()}
	job := a.backupJob.get()
	if job.Kind == "restore" && (job.Running || (job.FinishedAt != nil && time.Since(*job.FinishedAt) < time.Hour)) {
		st.Restore = &RestoreStatus{
			Running: job.Running, Phase: job.Phase, Table: job.Table, Done: job.Done, Total: job.Total,
			Finished: !job.Running, Failed: !job.Running && job.Error != "", DBReplaced: job.DBReplaced,
		}
	}
	return r.SendEnvelope(st)
}

// MaintenanceGuard answers 503 to API, websocket and public media requests
// while a restore runs. /api/maintenance, health checks and the web app pass.
func (a *App) MaintenanceGuard(r *fastglue.Request) *fastglue.Request {
	if !a.Maintenance.Active() {
		return r
	}
	path := string(r.RequestCtx.Path())
	if strings.HasSuffix(path, "/api/maintenance") {
		return r
	}
	if strings.Contains(path, "/api/") || strings.HasSuffix(path, "/ws") || strings.Contains(path, "/public/") {
		_ = r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Restore in progress", nil, "")
		return nil
	}
	return r
}
