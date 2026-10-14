package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/trysoft/whatomate/internal/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrFiles means the database was restored but the files were not.
var ErrFiles = errors.New("database restored, but files could not be put back")

// RestoreOptions control a restore.
type RestoreOptions struct {
	EncryptionKey string
	// Areas are the live file folders; an area is restored only when the
	// backup holds an area of the same name.
	Areas []FileArea
	// Skip lists folders and files a restore leaves alone, such as the backup
	// folder (see ProtectedPaths). One inside an area must be directly inside it.
	Skip []string
}

// Restore replaces every backed-up table in db with the backup's rows, in one
// transaction, then puts back the files. On a database error nothing changes.
func Restore(ctx context.Context, db *gorm.DB, path string, opts RestoreOptions, progress ProgressFunc) error {
	progress = orNop(progress)
	sum, err := Inspect(path, db, opts.EncryptionKey)
	if err != nil {
		return err
	}
	var areas []FileArea
	if sum.Manifest.IncludeFiles {
		for _, a := range opts.Areas {
			if _, ok := sum.Manifest.Files[a.Name]; ok {
				areas = append(areas, a)
			}
		}
	}
	if err := checkSkips(areas, opts.Skip); err != nil {
		return err
	}
	if err := Verify(path, progress); err != nil {
		return err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer func() { _ = zr.Close() }()

	if err := database.AutoMigrate(db.WithContext(ctx)); err != nil {
		return fmt.Errorf("update tables: %w", err)
	}
	tbls, err := backupTables(db)
	if err != nil {
		return err
	}
	fks, err := foreignKeys(db)
	if err != nil {
		return err
	}
	byName := map[string]table{}
	var names []string
	for _, t := range tbls {
		byName[t.Name] = t
		names = append(names, t.Name)
	}
	for _, s := range storeTableNames {
		if db.Migrator().HasTable(s) {
			names = append(names, s)
		}
	}
	plan := planLoad(names, fks)
	entries := map[string]TableEntry{}
	for _, te := range sum.Manifest.Tables {
		entries[te.Name] = te
	}

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := clearTables(tx, plan); err != nil {
			return err
		}
		var links []pendingLink
		for _, name := range plan.Order {
			te, ok := entries[name]
			if !ok {
				continue
			}
			l, err := loadTable(ctx, tx, &zr.Reader, byName[name], te, plan.Deferred[name], progress)
			if err != nil {
				return err
			}
			links = append(links, l...)
		}
		return applyLinks(tx, links, progress)
	})
	if err != nil {
		return err
	}
	if len(areas) > 0 {
		if err := restoreFiles(ctx, &zr.Reader, areas, opts.Skip, progress); err != nil {
			return fmt.Errorf("%w: %v", ErrFiles, err)
		}
	}
	return nil
}

// clearTables empties every table in the plan, children first. Deferred
// columns are cleared before any delete because MySQL checks foreign keys
// row by row, so a table that points to itself cannot be deleted in one go.
func clearTables(tx *gorm.DB, p loadPlan) error {
	tablesWithLinks := make([]string, 0, len(p.Deferred))
	for t := range p.Deferred {
		tablesWithLinks = append(tablesWithLinks, t)
	}
	sort.Strings(tablesWithLinks)
	for _, t := range tablesWithLinks {
		sets := map[string]any{}
		for _, c := range p.Deferred[t] {
			sets[c] = nil
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Table(t).Updates(sets).Error; err != nil {
			return fmt.Errorf("clear links in %s: %w", t, err)
		}
	}
	for i := len(p.Order) - 1; i >= 0; i-- {
		if err := tx.Exec("DELETE FROM " + tx.Statement.Quote(p.Order[i])).Error; err != nil {
			return fmt.Errorf("empty %s: %w", p.Order[i], err)
		}
	}
	return nil
}

// pendingLink is a deferred column value set after every table is loaded.
type pendingLink struct {
	Table string
	Key   map[string]any
	Set   map[string]any
}

func loadTable(ctx context.Context, tx *gorm.DB, zr *zip.Reader, t table, te TableEntry, deferred []string, progress ProgressFunc) ([]pendingLink, error) {
	f := findFile(zr, "db/"+t.Name+".jsonl")
	if f == nil {
		return nil, fmt.Errorf("%w: table %s is missing", ErrCorrupt, t.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	cols := columnsByName(t)
	batchSize := 30000 / max(1, len(te.Columns))
	batchSize = min(max(batchSize, 1), 500)
	var (
		batch []map[string]any
		links []pendingLink
		done  int64
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertRows(tx, t.Name, batch); err != nil {
			return fmt.Errorf("table %s, rows %d-%d: %w", t.Name, done-int64(len(batch))+1, done, err)
		}
		batch = nil
		progress(Progress{Phase: PhaseLoad, Table: t.Name, Done: done, Total: te.Rows})
		return nil
	}
	br := bufio.NewReader(rc)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			row, err := decodeRow(line, cols)
			if err != nil {
				return nil, fmt.Errorf("table %s, row %d: %w", t.Name, done+1, err)
			}
			if set := takeDeferred(row, deferred); len(set) > 0 {
				key := map[string]any{}
				for _, pk := range t.Schema.PrimaryFieldDBNames {
					key[pk] = row[pk]
				}
				links = append(links, pendingLink{Table: t.Name, Key: key, Set: set})
			}
			batch = append(batch, row)
			done++
			if len(batch) >= batchSize {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	progress(Progress{Phase: PhaseLoad, Table: t.Name, Done: done, Total: te.Rows})
	return links, nil
}

// insertRows inserts rows into table with one INSERT, a missing column as
// NULL. GORM's map Create cannot be used: on PostgreSQL it asks the driver
// for LastInsertId, which pgx does not support.
func insertRows(tx *gorm.DB, table string, rows []map[string]any) error {
	var cols []string
	seen := map[string]bool{}
	for _, r := range rows {
		for c := range r {
			if !seen[c] {
				seen[c] = true
				cols = append(cols, c)
			}
		}
	}
	sort.Strings(cols)
	vals := clause.Values{Columns: make([]clause.Column, len(cols)), Values: make([][]any, len(rows))}
	for i, c := range cols {
		vals.Columns[i] = clause.Column{Name: c}
	}
	for i, r := range rows {
		vals.Values[i] = make([]any, len(cols))
		for j, c := range cols {
			vals.Values[i][j] = r[c]
		}
	}
	return tx.Exec("INSERT INTO ? ?", clause.Table{Name: table}, vals).Error
}

// takeDeferred moves non-NULL deferred columns out of row (leaving NULL) and
// returns them.
func takeDeferred(row map[string]any, deferred []string) map[string]any {
	set := map[string]any{}
	for _, c := range deferred {
		v, ok := row[c]
		if !ok {
			continue
		}
		if v != nil {
			set[c] = v
		}
		row[c] = nil
	}
	return set
}

func applyLinks(tx *gorm.DB, links []pendingLink, progress ProgressFunc) error {
	for i, l := range links {
		if err := tx.Table(l.Table).Where(l.Key).Updates(l.Set).Error; err != nil {
			return fmt.Errorf("table %s, links: %w", l.Table, err)
		}
		if i%200 == 0 || i == len(links)-1 {
			progress(Progress{Phase: PhaseLinks, Done: int64(i + 1), Total: int64(len(links))})
		}
	}
	return nil
}
