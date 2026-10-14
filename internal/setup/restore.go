package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

const (
	phasePrepare = "prepare"
	phaseConfig  = "config"
)

type restoreRequest struct {
	targetRequest
	Backup        backupRef `json:"backup"`
	EncryptionKey string    `json:"encryption_key"`
}

// restoreState is the restore job's progress, polled by the page.
type restoreState struct {
	Running     bool   `json:"running"`
	Phase       string `json:"phase"`
	Table       string `json:"table,omitempty"`
	Done        int64  `json:"done"`
	Total       int64  `json:"total"`
	Finished    bool   `json:"finished"`
	Failed      bool   `json:"failed"`
	FilesFailed bool   `json:"files_failed"`
	Error       string `json:"error,omitempty"`
}

// busy is true from the start of a job until it fails or setup hands over.
func (r restoreState) busy() bool {
	return r.Running || (r.Finished && !r.Failed)
}

func (s *server) getRestore() restoreState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.restore
}

func (s *server) setRestore(st restoreState) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.restore = st
}

func (s *server) updateRestore(fn func(*restoreState)) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	fn(&s.restore)
}

// startRestore checks the request like a fresh finish, then restores in the
// background.
func (s *server) startRestore(ctx *fasthttp.RequestCtx) {
	s.completeMu.Lock()
	defer s.completeMu.Unlock()
	if s.isFinished() != nil {
		writeError(ctx, fasthttp.StatusConflict, "setup is already complete")
		return
	}
	if s.getRestore().busy() {
		writeError(ctx, fasthttp.StatusConflict, "a restore is already running")
		return
	}
	var req restoreRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	tg, code, err := s.checkTarget(req.targetRequest)
	if err != nil {
		writeError(ctx, code, err.Error())
		return
	}
	path, err := s.resolveBackup(tg.cand, req.Backup)
	if err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	key := strings.TrimSpace(req.EncryptionKey)
	sum, err := backup.InspectFile(path, tg.db.Driver, key)
	if err != nil {
		writeError(ctx, fasthttp.StatusUnprocessableEntity, err.Error())
		return
	}
	if key != "" && !sum.KeyMatches {
		writeError(ctx, fasthttp.StatusBadRequest, "This encryption key does not match the backup")
		return
	}
	db, closeDB, code, err := openTarget(tg.db)
	if err != nil {
		writeError(ctx, code, err.Error())
		return
	}
	dbTarget := databaseTarget(tg.db)
	// Users left by an earlier restore into this database that stopped before
	// the config was written are fine: the restore empties the tables first.
	if _, err := s.reusableAdmin(db, dbTarget); err != nil {
		hasUsers := errors.Is(err, errHasUsers)
		if !hasUsers || !restorePending(tg.cand, dbTarget) {
			closeDB()
			if hasUsers {
				writeError(ctx, fasthttp.StatusConflict, err.Error())
			} else {
				writeError(ctx, fasthttp.StatusInternalServerError, err.Error())
			}
			return
		}
	}
	if err := markRestorePending(tg.cand, dbTarget); err != nil {
		closeDB()
		writeError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	origin, https := browserOrigin(ctx, req.Port, s.listenPort)
	s.setRestore(restoreState{Running: true, Phase: phasePrepare})
	go s.runRestore(db, closeDB, tg, path, key, finished{port: tg.port, url: origin}, https)
	writeData(ctx, map[string]bool{"started": true})
}

func (s *server) runRestore(db *gorm.DB, closeDB func(), tg target, path, key string, f finished, https bool) {
	fail := func(err error) {
		s.o.Log.Error("Restoring the backup failed", "error", err)
		s.updateRestore(func(r *restoreState) {
			r.Running, r.Finished, r.Failed, r.Error = false, true, true, err.Error()
		})
	}
	err := s.restoreInto(db, tg, path, key)
	closeDB()
	filesFailed := errors.Is(err, backup.ErrFiles)
	if err != nil && !filesFailed {
		fail(err)
		return
	}
	if filesFailed {
		s.o.Log.Error("Restoring the backup's files failed", "error", err)
	}
	s.updateRestore(func(r *restoreState) {
		r.Phase, r.Table, r.Done, r.Total = phaseConfig, "", 0, 0
		if filesFailed {
			r.FilesFailed, r.Error = true, err.Error()
		}
	})
	if _, err := s.writeConfig(tg, f.url, https, key); err != nil {
		fail(err)
		return
	}
	s.clearRestorePending(tg.cand)
	s.updateRestore(func(r *restoreState) { r.Running, r.Finished = false, true })
	// Setup's server stops at the hand-off, so give the page a moment to read
	// the result first.
	select {
	case <-s.restoreSeen:
	case <-time.After(s.restoreAckWait):
	}
	s.finishSetup(f)
}

// restoreInto prepares db and restores the backup at path into it and the
// default file folders. An error wrapping backup.ErrFiles means only the
// files failed.
func (s *server) restoreInto(db *gorm.DB, tg target, path, key string) error {
	if err := s.migrate(db, nil); err != nil {
		return fmt.Errorf("set up the database: %w", err)
	}
	media, audio := mediaDir(tg.cand), audioDir(tg.cand)
	for _, d := range []string{media, audio} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	return backup.Restore(context.Background(), db, path, backup.RestoreOptions{
		EncryptionKey: key,
		Areas:         backup.Areas(media, audio),
		Skip:          backup.ProtectedPaths(backupDir(tg.cand), tg.cand.Path, tg.db),
	}, func(p backup.Progress) {
		s.updateRestore(func(r *restoreState) { r.Phase, r.Table, r.Done, r.Total = p.Phase, p.Table, p.Done, p.Total })
	})
}

// pendingName marks a data folder whose database a restore may have filled
// before the config file was written. It holds only databaseTarget, never a
// password, and stays until the config is written.
const pendingName = ".setup-restore-pending"

func pendingPath(cand candidateInfo) string { return filepath.Join(cand.DataDir, pendingName) }

func markRestorePending(cand candidateInfo, target string) error {
	if err := os.MkdirAll(cand.DataDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", cand.DataDir, err)
	}
	if err := os.WriteFile(pendingPath(cand), []byte(target+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", pendingPath(cand), err)
	}
	return nil
}

func (s *server) clearRestorePending(cand candidateInfo) {
	if err := os.Remove(pendingPath(cand)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.o.Log.Warn("Could not remove the restore marker", "error", err)
	}
}

// restorePending reports whether the marker names target.
func restorePending(cand candidateInfo, target string) bool {
	b, err := os.ReadFile(pendingPath(cand))
	return err == nil && strings.TrimSpace(string(b)) == target
}

func (s *server) restoreStatus(ctx *fasthttp.RequestCtx) {
	st := s.getRestore()
	if st.Finished && !st.Failed {
		select {
		case s.restoreSeen <- struct{}{}:
		default:
		}
	}
	writeData(ctx, st)
}
