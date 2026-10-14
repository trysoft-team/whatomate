package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// targetRequest is where a finish writes: the config location, the app port
// and the database.
type targetRequest struct {
	ConfigPath string        `json:"config_path"`
	Port       int           `json:"port"`
	Database   databaseInput `json:"database"`
}

type target struct {
	cand candidateInfo
	port int
	db   config.DatabaseConfig
}

// checkTarget validates req and returns the HTTP status to use with an error.
func (s *server) checkTarget(req targetRequest) (target, int, error) {
	tg, err := s.parseTarget(req)
	if err != nil {
		return target{}, fasthttp.StatusBadRequest, err
	}
	if err := tg.checkFree(); err != nil {
		return target{}, fasthttp.StatusConflict, err
	}
	return tg, 0, nil
}

// parseTarget validates req's fields; every failure is a bad request.
func (s *server) parseTarget(req targetRequest) (target, error) {
	cand, ok := s.lookup(req.ConfigPath)
	if !ok {
		return target{}, errors.New("unknown config location")
	}
	if req.Port < 1 || req.Port > 65535 {
		return target{}, errors.New("port must be between 1 and 65535")
	}
	if req.Port != s.listenPort && !s.o.Checker.PortFree(req.Port) {
		return target{}, fmt.Errorf("port %d is in use by another program", req.Port)
	}
	dbCfg := req.Database.config()
	if err := validateDatabase(dbCfg); err != nil {
		return target{}, err
	}
	return target{cand: cand, port: req.Port, db: dbCfg}, nil
}

// checkFree fails when something is already at the config path.
func (tg target) checkFree() error {
	// A directory counts too: the config file could never be written there.
	if _, err := os.Lstat(tg.cand.Path); err == nil {
		return configExists(tg.cand.Path)
	}
	return nil
}

// openTarget connects to the database, creating the SQLite folder first.
func openTarget(c config.DatabaseConfig) (*gorm.DB, func(), int, error) {
	if c.Driver == config.DriverSQLite {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o750); err != nil {
			return nil, func() {}, fasthttp.StatusInternalServerError, fmt.Errorf("create %s: %w", filepath.Dir(c.Path), err)
		}
	}
	db, err := database.Open(&c, false)
	if err != nil {
		return nil, func() {}, fasthttp.StatusBadRequest, fmt.Errorf("connect to the database: %w", err)
	}
	return db, func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}, 0, nil
}

// writeConfig writes the config file for tg. A blank encryptionKey gets a
// new one.
func (s *server) writeConfig(tg target, origin string, https bool, encryptionKey string) (int, error) {
	jwtSecret, err := newSecret(48)
	if err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	encKey := encryptionKey
	if encKey == "" {
		if encKey, err = newSecret(32); err != nil {
			return fasthttp.StatusInternalServerError, err
		}
	}
	stored := tg.db
	if stored.Driver == config.DriverSQLite {
		stored.Path = storedDBPath(tg.db.Path, tg.cand.base)
	}
	if err := os.MkdirAll(tg.cand.DataDir, 0o750); err != nil {
		return fasthttp.StatusInternalServerError, fmt.Errorf("create %s: %w", tg.cand.DataDir, err)
	}
	body := Render(Values{
		Environment: environmentFor(https), Port: tg.port, AllowedOrigins: allowedOrigins(origin, https, s.urls(tg.port)),
		Database: stored, JWTSecret: jwtSecret, EncryptionKey: encKey,
	}, time.Now())
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	if err := config.WriteFileExclusive(tg.cand.Path, body, 0o600); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fasthttp.StatusConflict, configExists(tg.cand.Path)
		}
		return fasthttp.StatusInternalServerError, fmt.Errorf("write %s: %w", tg.cand.Path, err)
	}
	s.o.Log.Info("Setup complete", "config", tg.cand.Path)
	return fasthttp.StatusOK, nil
}

// finishSetup records f and hands over to the app.
func (s *server) finishSetup(f finished) {
	s.stateMu.Lock()
	s.finished = &f
	s.stateMu.Unlock()
	s.done <- f
}

// appBase is where the app resolves relative paths for this config
// location: its base folder, or the working directory for a fixed path
// (see config.ResolvePaths).
func appBase(cand candidateInfo) string {
	if cand.base != "" {
		return cand.base
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func backupDir(cand candidateInfo) string { return filepath.Join(appBase(cand), "backups") }
func mediaDir(cand candidateInfo) string  { return filepath.Join(appBase(cand), "uploads") }
func audioDir(cand candidateInfo) string  { return filepath.Join(appBase(cand), "audio") }
