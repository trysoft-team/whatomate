package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/trysoft/whatomate/internal/config"
)

var databaseName = regexp.MustCompile(`^[A-Za-z0-9_]{1,63}$`)

// ValidDatabaseName reports whether CreateDatabase accepts name.
func ValidDatabaseName(name string) bool { return databaseName.MatchString(name) }

// Probe connects with cfg. It returns exists=false and no error when the
// server accepts the login but the database does not exist yet.
func Probe(cfg *config.DatabaseConfig) (exists bool, err error) {
	db, err := Open(cfg, false)
	if err != nil {
		if isUnknownDatabase(err) {
			return false, nil
		}
		return false, err
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	return true, nil
}

func isUnknownDatabase(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "3D000" // invalid_catalog_name
	}
	var myErr *mysqldrv.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Number == 1049 // ER_BAD_DB_ERROR
	}
	return false
}

// CreateDatabase creates cfg.Name on a PostgreSQL or MySQL server. It
// connects to the "postgres" maintenance database, or to no database on MySQL.
func CreateDatabase(cfg *config.DatabaseConfig) error {
	if !ValidDatabaseName(cfg.Name) {
		return fmt.Errorf("database name %q: use 1-63 letters, digits or underscores", cfg.Name)
	}
	var driver, dsn, stmt string
	switch cfg.Driver {
	case "", config.DriverPostgres:
		server := *cfg
		if server.SSLMode == "" {
			server.SSLMode = "disable"
		}
		driver = "pgx"
		dsn = postgresDSN(&server, "postgres")
		stmt = `CREATE DATABASE "` + cfg.Name + `"`
	case config.DriverMySQL:
		server := *cfg
		server.Name = ""
		var err error
		if dsn, err = mysqlDSN(&server, ""); err != nil {
			return err
		}
		driver = "mysql"
		stmt = "CREATE DATABASE `" + cfg.Name + "` CHARACTER SET utf8mb4"
	default:
		return fmt.Errorf("creating a %s database is not supported", cfg.Driver)
	}
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("failed to create database %s: %w", cfg.Name, err)
	}
	return nil
}
