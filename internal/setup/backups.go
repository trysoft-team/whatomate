package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
)

const uploadsPath = "/api/setup/backup-uploads"

// backupRef names a backup: a file in the backup folder, or a full path.
type backupRef struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// resolveBackup returns the backup file ref points at.
func (s *server) resolveBackup(cand candidateInfo, ref backupRef) (string, error) {
	var p string
	switch {
	case ref.Name != "" && ref.Path == "":
		if !backup.ValidName(ref.Name) {
			return "", errors.New("invalid backup name")
		}
		p = filepath.Join(backupDir(cand), ref.Name)
	case ref.Path != "" && ref.Name == "":
		p = filepath.Clean(strings.TrimSpace(ref.Path))
		if !filepath.IsAbs(p) || !strings.EqualFold(filepath.Ext(p), ".zip") {
			return "", errors.New("enter the full path to a .zip backup")
		}
	default:
		return "", errors.New("choose a backup")
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("no backup at %s", p)
	}
	return p, nil
}

func isUploadChunk(method, path []byte) bool {
	return string(method) == fasthttp.MethodPut && strings.HasPrefix(string(path), uploadsPath+"/")
}

func uploadError(ctx *fasthttp.RequestCtx, err error) {
	var off *backup.OffsetError
	var big *backup.TooLargeError
	var inc *backup.IncompleteError
	var sp *backup.SpaceError
	switch {
	case errors.Is(err, backup.ErrUploadNotFound):
		writeError(ctx, fasthttp.StatusNotFound, "Upload not found")
	case errors.Is(err, backup.ErrNotBackupFile):
		writeError(ctx, fasthttp.StatusBadRequest, "Choose a whatomate backup (.zip)")
	case errors.Is(err, backup.ErrBadChunk):
		writeError(ctx, fasthttp.StatusBadRequest, "Bad chunk size")
	case errors.As(err, &off):
		writeError(ctx, fasthttp.StatusConflict, fmt.Sprintf("Expected offset %d", off.Received))
	case errors.As(err, &big):
		writeError(ctx, fasthttp.StatusRequestEntityTooLarge, fmt.Sprintf("The backup is larger than the %d MB upload limit", big.LimitMB))
	case errors.As(err, &inc):
		writeError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Received %d of %d bytes", inc.Received, inc.Size))
	case errors.As(err, &sp):
		writeError(ctx, fasthttp.StatusInsufficientStorage, sp.Err.Error())
	default:
		writeError(ctx, fasthttp.StatusInternalServerError, err.Error())
	}
}

func (s *server) startUpload(ctx *fasthttp.RequestCtx) {
	var req struct {
		ConfigPath string `json:"config_path"`
		Name       string `json:"name"`
		Size       int64  `json:"size"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	cand, ok := s.lookup(req.ConfigPath)
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	id, err := s.uploads.Start(backupDir(cand), req.Name, req.Size, backup.DefaultMaxUploadMB)
	if err != nil {
		uploadError(ctx, err)
		return
	}
	writeData(ctx, map[string]any{"id": id, "received": 0, "chunk_max": backup.ChunkMax})
}

// uploadItem serves PUT and DELETE /backup-uploads/{id} and POST .../finish.
func (s *server) uploadItem(ctx *fasthttp.RequestCtx, rest string) {
	id, action, _ := strings.Cut(rest, "/")
	switch {
	case action == "" && ctx.IsPut():
		offset, err := strconv.ParseInt(string(ctx.QueryArgs().Peek("offset")), 10, 64)
		if err != nil {
			offset = -1
		}
		n, err := s.uploads.Put(id, offset, ctx.PostBody())
		if err != nil {
			uploadError(ctx, err)
			return
		}
		writeData(ctx, map[string]int64{"received": n})
	case action == "" && ctx.IsDelete():
		s.uploads.Cancel(id)
		writeData(ctx, map[string]string{"cancelled": id})
	case action == "finish" && ctx.IsPost():
		final, err := s.uploads.Finish(id)
		if err != nil {
			uploadError(ctx, err)
			return
		}
		sum, err := backup.InspectFile(final, "", "")
		if err != nil {
			_ = os.Remove(final)
			writeError(ctx, fasthttp.StatusUnprocessableEntity, err.Error())
			return
		}
		m := sum.Manifest
		writeData(ctx, backup.Info{Name: sum.Name, Size: sum.Size, Manifest: &m})
	default:
		writeError(ctx, fasthttp.StatusNotFound, "not found")
	}
}

func (s *server) listBackups(ctx *fasthttp.RequestCtx) {
	cand, ok := s.lookup(string(ctx.QueryArgs().Peek("config_path")))
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	dir := backupDir(cand)
	list, err := backup.List(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []backup.Info{}
	}
	writeData(ctx, map[string]any{"dir": dir, "backups": list})
}

type inspectRequest struct {
	ConfigPath    string        `json:"config_path"`
	Database      databaseInput `json:"database"`
	Backup        backupRef     `json:"backup"`
	EncryptionKey string        `json:"encryption_key"`
}

type inspectResult struct {
	backup.Summary
	KeyGiven bool `json:"key_given"`
}

func (s *server) inspectBackup(ctx *fasthttp.RequestCtx) {
	var req inspectRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	cand, ok := s.lookup(req.ConfigPath)
	if !ok {
		writeError(ctx, fasthttp.StatusBadRequest, "unknown config location")
		return
	}
	p, err := s.resolveBackup(cand, req.Backup)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(req.EncryptionKey)
	sum, err := backup.InspectFile(p, req.Database.config().Driver, key)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnprocessableEntity, err.Error())
		return
	}
	writeData(ctx, inspectResult{Summary: sum, KeyGiven: key != ""})
}
