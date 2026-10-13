package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// uploadChunkMax keeps each chunk well under the server's 15 MB body limit.
const uploadChunkMax = 8 << 20

const defaultMaxUploadMB = 10240

type backupUpload struct {
	mu       sync.Mutex
	name     string
	path     string
	size     int64
	received int64
}

type uploadStore struct {
	mu    sync.Mutex
	items map[string]*backupUpload
}

func (s *uploadStore) get(id string) *backupUpload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[id]
}

func (s *uploadStore) put(id string, u *backupUpload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]*backupUpload{}
	}
	s.items[id] = u
}

func (s *uploadStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

// StartBackupUpload begins a chunked upload of a backup file.
func (a *App) StartBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	var req struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if !strings.HasSuffix(strings.ToLower(req.Name), ".zip") || req.Size <= 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose a whatomate backup (.zip)", nil, "")
	}
	maxMB := a.Config.Backup.MaxUploadMB
	if maxMB <= 0 {
		maxMB = defaultMaxUploadMB
	}
	if limit := int64(maxMB) << 20; req.Size > limit {
		return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge,
			fmt.Sprintf("The backup is larger than the %d MB upload limit ([backup] max_upload_mb)", maxMB), nil, "")
	}
	if err := os.MkdirAll(a.Config.Backup.Dir, 0o750); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	if err := ensureSpace(a.Config.Backup.Dir, req.Size); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInsufficientStorage, err.Error(), nil, "")
	}
	id := uuid.NewString()
	p := filepath.Join(a.Config.Backup.Dir, backup.UploadPrefix+id+".part")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	_ = f.Close()
	a.uploads.put(id, &backupUpload{name: req.Name, path: p, size: req.Size})
	return r.SendEnvelope(map[string]any{"id": id, "received": 0, "chunk_max": uploadChunkMax})
}

// PutBackupUploadChunk appends one chunk at ?offset=N.
func (a *App) PutBackupUploadChunk(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	u := a.uploads.get(id)
	if u == nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Upload not found", nil, "")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	offset, err := strconv.ParseInt(string(r.RequestCtx.QueryArgs().Peek("offset")), 10, 64)
	if err != nil || offset != u.received {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, fmt.Sprintf("Expected offset %d", u.received), map[string]int64{"received": u.received}, "")
	}
	body := r.RequestCtx.PostBody()
	if len(body) == 0 || len(body) > uploadChunkMax || u.received+int64(len(body)) > u.size {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Bad chunk size", nil, "")
	}
	f, err := os.OpenFile(u.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	n, err := f.Write(body)
	_ = f.Close()
	u.received += int64(n)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]int64{"received": u.received})
}

// FinishBackupUpload checks the uploaded file and adds it to the list.
func (a *App) FinishBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	u := a.uploads.get(id)
	if u == nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Upload not found", nil, "")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.received != u.size {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, fmt.Sprintf("Received %d of %d bytes", u.received, u.size), nil, "")
	}
	a.uploads.remove(id)
	final := filepath.Join(a.Config.Backup.Dir, backup.UploadName(a.Config.Backup.Dir, u.name))
	if err := os.Rename(u.path, final); err != nil {
		_ = os.Remove(u.path)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
	}
	sum, err := backup.Inspect(final, a.DB, a.Config.App.EncryptionKey)
	if err != nil {
		_ = os.Remove(final)
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, err.Error(), nil, "")
	}
	m := sum.Manifest
	return r.SendEnvelope(backup.Info{Name: sum.Name, Size: sum.Size, Manifest: &m})
}

// CancelBackupUpload drops a partial upload.
func (a *App) CancelBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	if u := a.uploads.get(id); u != nil {
		u.mu.Lock()
		_ = os.Remove(u.path)
		u.mu.Unlock()
		a.uploads.remove(id)
	}
	return r.SendEnvelope(map[string]string{"cancelled": id})
}
