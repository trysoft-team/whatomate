package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/trysoft/whatomate/internal/handlers"
	"github.com/trysoft/whatomate/internal/store"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

func cliLogger() logf.Logger {
	return logf.New(logf.Opts{EnableColor: true, Level: logf.InfoLevel, TimestampFormat: "2006-01-02 15:04:05", DefaultFields: []any{"app", "whatomate"}})
}

// cliAreas mirrors handlers.App.backupAreas for the command line.
func cliAreas(cfg *config.Config) []backup.FileArea {
	media := cfg.Storage.LocalPath
	if media == "" {
		media = "./media"
	}
	return backup.Areas(media, cfg.Calling.AudioDir)
}

// cliLock is the server lock a command holds so a server starting on this host
// meanwhile leaves its temp files alone. release only removes a lock this
// process wrote.
type cliLock struct {
	dir string
	lo  logf.Logger
}

func (l cliLock) release() { backup.RemoveServerLock(l.dir) }

// fatal releases the lock first, since lo.Fatal skips deferred calls.
func (l cliLock) fatal(msg string, kv ...any) {
	l.release()
	l.lo.Fatal(msg, kv...)
}

// clearRestoreState clears what the server clears after a restore. Without
// Redis the caches live in the server's memory and the sessions in the
// database the restore already emptied, so only Redis needs it.
func clearRestoreState(cfg *config.Config, db *gorm.DB, lo logf.Logger) {
	if cfg.Redis.Host == "" {
		return
	}
	stores, err := store.New(&cfg.Redis, db)
	if err != nil {
		lo.Warn("Could not connect to Redis to clear caches and sign everyone out; do this before starting whatomate", "error", err)
		return
	}
	defer stores.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	handlers.ClearRestoreState(ctx, stores.Cache, stores.State, lo)
}

func printProgress(p backup.Progress) {
	switch {
	case p.Total > 0:
		fmt.Printf("\r  %-8s %-32s %d/%d   ", p.Phase, p.Table, p.Done, p.Total)
	default:
		fmt.Printf("\r  %-8s %-32s %d   ", p.Phase, p.Table, p.Done)
	}
}

func runBackupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file")
	noFiles := fs.Bool("no-files", false, "Leave out uploaded media and audio")
	out := fs.String("o", "", "Write to this file instead of the backup folder")
	_ = fs.Parse(args)

	lo := cliLogger()
	if _, err := config.Locate(*configPath); errors.Is(err, config.ErrNotFound) {
		lo.Fatal("No config file found; run whatomate server first to set one up")
	}
	cfg, cfgPath := loadConfig(*configPath, lo)
	_, _, _ = backup.ClaimCLILock(cfg.Backup.Dir)
	lock := cliLock{dir: cfg.Backup.Dir, lo: lo}
	defer lock.release()
	fatal := lock.fatal
	db, err := database.Open(&cfg.Database, false)
	if err != nil {
		fatal("Failed to connect to database", "error", err)
	}
	opts := backup.Options{
		Kind: backup.KindCLI, AppVersion: Version, EncryptionKey: cfg.App.EncryptionKey, IncludeFiles: !*noFiles,
		Areas: cliAreas(cfg), Skip: backup.ProtectedPaths(cfg.Backup.Dir, cfgPath, cfg.Database), Path: *out, Warn: lo.Warn,
	}
	spaceDir := cfg.Backup.Dir
	if *out != "" {
		spaceDir = filepath.Dir(*out)
	}
	if err := backup.EnsureSpace(spaceDir, backup.EstimateSize(db, opts.Areas, opts.IncludeFiles, opts.Skip)); err != nil {
		fatal("Cannot back up", "error", err)
	}
	info, err := backup.Create(context.Background(), db, cfg.Backup.Dir, opts, printProgress)
	fmt.Println()
	if err != nil {
		fatal("Backup failed", "error", err)
	}
	where := *out
	if where == "" {
		where = filepath.Join(cfg.Backup.Dir, info.Name)
	}
	fmt.Printf("Backup written: %s (%d MB)\n", where, info.Size>>20)
}

func runRestoreCmd(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file")
	yes := fs.Bool("yes", false, "Do not ask to type RESTORE")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Println("Usage: whatomate restore [-config f] [-yes] <backup.zip>")
		os.Exit(2)
	}
	file := fs.Arg(0)

	lo := cliLogger()
	if _, err := config.Locate(*configPath); errors.Is(err, config.ErrNotFound) {
		lo.Fatal("No config file found; run whatomate server first to set one up, then restore")
	}
	cfg, cfgPath := loadConfig(*configPath, lo)
	if claimed, other, err := backup.ClaimCLILock(cfg.Backup.Dir); !claimed && err == nil {
		lo.Fatal(fmt.Sprintf("A whatomate server seems to be running; stop it first or restore from Settings → Backup & Restore. If no server is running, delete %s and try again",
			filepath.Join(cfg.Backup.Dir, "server.lock")), "pid", other.PID, "host", other.Host)
	}
	lock := cliLock{dir: cfg.Backup.Dir, lo: lo}
	defer lock.release()
	fatal := lock.fatal
	db, err := database.Open(&cfg.Database, false)
	if err != nil {
		fatal("Failed to connect to database", "error", err)
	}
	sum, err := backup.Inspect(file, db, cfg.App.EncryptionKey)
	if err != nil {
		fatal("Cannot restore this file", "error", err)
	}
	m := sum.Manifest
	var rows int64
	for _, t := range m.Tables {
		rows += t.Rows
	}
	fmt.Printf("Backup:    %s (%d MB)\nMade:      %s with whatomate %s on %s\nRestoring: into %s\nRows:      %d in %d tables\nFiles:     %v\n",
		sum.Name, sum.Size>>20, m.CreatedAt.Format("2006-01-02 15:04 MST"), m.AppVersion, m.SourceDriver, sum.TargetDriver, rows, len(m.Tables), m.IncludeFiles)
	if !sum.KeyMatches {
		fmt.Println("WARNING: this backup was made with a different encryption_key. Saved WhatsApp tokens, AI keys and SSO secrets will not decrypt and must be entered again.")
	}
	fmt.Println("Everything in the current database will be replaced. A safety backup is taken first.")
	if !*yes {
		fmt.Print("Type RESTORE to continue: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "RESTORE" {
			fmt.Println("Cancelled.")
			lock.release()
			os.Exit(1)
		}
	}
	areas := cliAreas(cfg)
	skip := backup.ProtectedPaths(cfg.Backup.Dir, cfgPath, cfg.Database)
	need := backup.EstimateSize(db, areas, m.IncludeFiles, skip)
	for _, st := range m.Files {
		need += st.Bytes
	}
	if err := backup.EnsureSpace(cfg.Backup.Dir, need); err != nil {
		fatal("Cannot restore; nothing was changed", "error", err)
	}
	safety, err := backup.Create(context.Background(), db, cfg.Backup.Dir, backup.Options{
		Kind: backup.KindPreRestore, AppVersion: Version, EncryptionKey: cfg.App.EncryptionKey,
		IncludeFiles: m.IncludeFiles, Areas: areas, Skip: skip, Warn: lo.Warn,
	}, printProgress)
	fmt.Println()
	if err != nil {
		fatal("Safety backup failed; nothing was changed", "error", err)
	}
	fmt.Printf("Safety backup: %s\n", safety.Name)
	err = backup.Restore(context.Background(), db, file, backup.RestoreOptions{
		EncryptionKey: cfg.App.EncryptionKey, Areas: areas, Skip: skip,
	}, printProgress)
	fmt.Println()
	if err == nil || errors.Is(err, backup.ErrFiles) {
		clearRestoreState(cfg, db, lo)
	}
	if errors.Is(err, backup.ErrFiles) {
		fatal("Restore incomplete", "error", err, "undo_with", safety.Name)
	}
	if err != nil {
		fatal("Restore failed; nothing was changed", "error", err)
	}
	fmt.Println("Restore finished. Start whatomate and sign in with an account from the backup.")
}
