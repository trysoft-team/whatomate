package database

import (
	"database/sql"
	"fmt"
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
		dsn := cfg.Path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		// A single connection serialises writers, avoiding "database is locked".
		sqlDB.SetMaxOpenConns(1)
		return portableDialector{
			Dialector: sqlite.Dialector{DriverName: "sqlite", DSN: dsn, Conn: utcPool{sqlDB}},
			types:     sqliteTypes,
		}, nil
	}
	return nil, fmt.Errorf("database driver %q is not supported yet", cfg.Driver)
}

// Open connects to the configured database.
func Open(cfg *config.DatabaseConfig, debug bool) (*gorm.DB, error) {
	dialector, err := OpenDialector(cfg)
	if err != nil {
		return nil, err
	}

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
