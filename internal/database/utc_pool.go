package database

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// utcPool converts time arguments to UTC before they reach SQLite, which
// compares timestamps as text including the zone offset. It only sees
// arguments passed through QueryContext, ExecContext and PrepareContext: a
// driver.Valuer returning a local time.Time, or a *sql.Conn taken from
// db.DB(), bypasses it.
type utcPool struct{ db *sql.DB }

func utcArgs(args []any) []any {
	for i, a := range args {
		switch t := a.(type) {
		case time.Time:
			args[i] = t.UTC()
		case *time.Time:
			if t != nil {
				u := t.UTC()
				args[i] = &u
			}
		}
	}
	return args
}

func (p utcPool) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, q)
}

func (p utcPool) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, q, utcArgs(args)...)
}

func (p utcPool) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, q, utcArgs(args)...)
}

func (p utcPool) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, q, utcArgs(args)...)
}

func (p utcPool) GetDBConn() (*sql.DB, error) { return p.db, nil }

func (p utcPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	// A pointer, because gorm's Commit and Rollback call reflect IsNil on it.
	return &utcTx{tx}, nil
}

type utcTx struct{ tx *sql.Tx }

func (t utcTx) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return t.tx.PrepareContext(ctx, q)
}

func (t utcTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, q, utcArgs(args)...)
}

func (t utcTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, utcArgs(args)...)
}

func (t utcTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, q, utcArgs(args)...)
}

func (t utcTx) Commit() error   { return t.tx.Commit() }
func (t utcTx) Rollback() error { return t.tx.Rollback() }
