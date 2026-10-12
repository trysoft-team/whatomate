package database

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/trysoft/whatomate/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OpenDialector returns the GORM dialector for cfg.Driver.
func OpenDialector(cfg *config.DatabaseConfig) (gorm.Dialector, error) {
	switch cfg.Driver {
	case "", config.DriverPostgres:
		return postgres.Open(fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name, cfg.SSLMode,
		)), nil
	case config.DriverSQLite:
		if err := createSQLiteFile(cfg.Path); err != nil {
			return nil, err
		}
		dsn := cfg.Path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		// The driver applies the DSN pragmas to every new connection. Under WAL,
		// readers run concurrently; writers serialise through the IMMEDIATE
		// transaction lock and wait up to busy_timeout instead of failing.
		maxOpen := cfg.MaxOpenConns
		if maxOpen <= 0 {
			maxOpen = config.DefaultMaxOpenConns
		}
		sqlDB.SetMaxOpenConns(maxOpen)
		if cfg.MaxIdleConns > 0 {
			sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
		}
		if cfg.ConnMaxLifetime > 0 {
			sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
		}
		return portableDialector{
			Dialector: sqlite.Dialector{DriverName: "sqlite", DSN: dsn, Conn: utcPool{sqlDB}},
			types:     sqliteTypes,
		}, nil
	case config.DriverMySQL:
		return openMySQL(cfg)
	}
	return nil, fmt.Errorf("database driver %q is not supported yet", cfg.Driver)
}

// createSQLiteFile creates the database file owner-only if it does not exist yet.
// SQLite gives the -wal and -shm files the main file's mode. An existing file
// keeps whatever mode the operator gave it.
func createSQLiteFile(path string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sqlite database directory %q does not exist", dir)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create sqlite database file: %w", err)
	}
	return f.Close()
}

// Open connects to the configured database.
func Open(cfg *config.DatabaseConfig, debug bool) (*gorm.DB, error) {
	dialector, err := OpenDialector(cfg)
	if err != nil {
		return nil, err
	}
	return open(dialector, cfg, debug)
}

// open closes the pool the dialector owns, if any, when it fails.
func open(dialector gorm.Dialector, cfg *config.DatabaseConfig, debug bool) (_ *gorm.DB, err error) {
	defer func() {
		if d, ok := dialector.(portableDialector); ok && err != nil && d.pool != nil {
			_ = d.pool.Close()
		}
	}()

	logLevel := logger.Silent
	if debug {
		logLevel = logger.Info
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:  logger.Default.LogMode(logLevel),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if cfg.Driver == config.DriverMySQL {
		if err := db.Callback().Create().Before("gorm:create").Register("whatomate:literal_json_defaults", fillLiteralJSONDefaults); err != nil {
			return nil, err
		}
	}

	if cfg.Driver != config.DriverSQLite {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("failed to get database instance: %w", err)
		}
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	}

	return db, nil
}

// NewPostgres is kept for callers not yet moved to Open.
func NewPostgres(cfg *config.DatabaseConfig, debug bool) (*gorm.DB, error) {
	return Open(cfg, debug)
}
