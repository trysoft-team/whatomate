package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/trysoft/whatomate/internal/backup"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/database"
	"github.com/zerodha/logf"
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
	cfg, _ := loadConfig(*configPath, lo)
	db, err := database.Open(&cfg.Database, false)
	if err != nil {
		lo.Fatal("Failed to connect to database", "error", err)
	}
	info, err := backup.Create(context.Background(), db, cfg.Backup.Dir, backup.Options{
		Kind: backup.KindCLI, AppVersion: Version, EncryptionKey: cfg.App.EncryptionKey,
		IncludeFiles: !*noFiles, Areas: cliAreas(cfg), Skip: []string{cfg.Backup.Dir}, Path: *out,
	}, printProgress)
	fmt.Println()
	if err != nil {
		lo.Fatal("Backup failed", "error", err)
	}
	where := *out
	if where == "" {
		where = cfg.Backup.Dir + string(os.PathSeparator) + info.Name
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
	cfg, _ := loadConfig(*configPath, lo)
	if pid, running := backup.ServerRunning(cfg.Backup.Dir); running {
		lo.Fatal("whatomate server is running; stop it first or restore from Settings → Backup & Restore", "pid", pid)
	}
	db, err := database.Open(&cfg.Database, false)
	if err != nil {
		lo.Fatal("Failed to connect to database", "error", err)
	}
	sum, err := backup.Inspect(file, db, cfg.App.EncryptionKey)
	if err != nil {
		lo.Fatal("Cannot restore this file", "error", err)
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
			os.Exit(1)
		}
	}
	areas := cliAreas(cfg)
	safety, err := backup.Create(context.Background(), db, cfg.Backup.Dir, backup.Options{
		Kind: backup.KindPreRestore, AppVersion: Version, EncryptionKey: cfg.App.EncryptionKey,
		IncludeFiles: m.IncludeFiles, Areas: areas, Skip: []string{cfg.Backup.Dir},
	}, printProgress)
	fmt.Println()
	if err != nil {
		lo.Fatal("Safety backup failed; nothing was changed", "error", err)
	}
	fmt.Printf("Safety backup: %s\n", safety.Name)
	err = backup.Restore(context.Background(), db, file, backup.RestoreOptions{
		EncryptionKey: cfg.App.EncryptionKey, Areas: areas, Skip: []string{cfg.Backup.Dir},
	}, printProgress)
	fmt.Println()
	if errors.Is(err, backup.ErrFiles) {
		lo.Fatal("Restore incomplete", "error", err, "undo_with", safety.Name)
	}
	if err != nil {
		lo.Fatal("Restore failed; nothing was changed", "error", err)
	}
	fmt.Println("Restore finished. Start whatomate and sign in with an account from the backup.")
}
