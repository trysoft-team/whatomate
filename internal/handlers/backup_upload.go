package handlers

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// backupUploads returns the upload engine, checking disk space through
// ensureSpace so tests can replace it.
func (a *App) backupUploads() *backup.Uploads {
	a.uploadsInit.Do(func() {
		a.uploads.Space = func(dir string, need int64) error { return ensureSpace(dir, need) }
	})
	return &a.uploads
}

func sendUploadError(r *fastglue.Request, err error) error {
	var off *backup.OffsetError
	var big *backup.TooLargeError
	var inc *backup.IncompleteError
	var sp *backup.SpaceError
	switch {
	case errors.Is(err, backup.ErrUploadNotFound):
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Upload not found", nil, "")
	case errors.Is(err, backup.ErrNotBackupFile):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose a whatomate backup (.zip)", nil, "")
	case errors.Is(err, backup.ErrBadChunk):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Bad chunk size", nil, "")
	case errors.As(err, &off):
		return r.SendErrorEnvelope(fasthttp.StatusConflict, fmt.Sprintf("Expected offset %d", off.Received), map[string]int64{"received": off.Received}, "")
	case errors.As(err, &big):
		return r.SendErrorEnvelope(fasthttp.StatusRequestEntityTooLarge,
			fmt.Sprintf("The backup is larger than the %d MB upload limit ([backup] max_upload_mb)", big.LimitMB), nil, "")
	case errors.As(err, &inc):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, fmt.Sprintf("Received %d of %d bytes", inc.Received, inc.Size), nil, "")
	case errors.As(err, &sp):
		return r.SendErrorEnvelope(fasthttp.StatusInsufficientStorage, sp.Err.Error(), nil, "")
	}
	return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, err.Error(), nil, "")
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
	id, err := a.backupUploads().Start(a.Config.Backup.Dir, req.Name, req.Size, a.Config.Backup.MaxUploadMB)
	if err != nil {
		return sendUploadError(r, err)
	}
	return r.SendEnvelope(map[string]any{"id": id, "received": 0, "chunk_max": backup.ChunkMax})
}

// PutBackupUploadChunk appends one chunk at ?offset=N.
func (a *App) PutBackupUploadChunk(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	offset, err := strconv.ParseInt(string(r.RequestCtx.QueryArgs().Peek("offset")), 10, 64)
	if err != nil {
		offset = -1
	}
	n, err := a.backupUploads().Put(id, offset, r.RequestCtx.PostBody())
	if err != nil {
		return sendUploadError(r, err)
	}
	return r.SendEnvelope(map[string]int64{"received": n})
}

// FinishBackupUpload checks the uploaded file and adds it to the list.
func (a *App) FinishBackupUpload(r *fastglue.Request) error {
	if _, _, err := a.requireSuperAdmin(r); err != nil {
		return nil
	}
	id, _ := r.RequestCtx.UserValue("id").(string)
	final, err := a.backupUploads().Finish(id)
	if err != nil {
		return sendUploadError(r, err)
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
	a.backupUploads().Cancel(id)
	return r.SendEnvelope(map[string]string{"cancelled": id})
}
