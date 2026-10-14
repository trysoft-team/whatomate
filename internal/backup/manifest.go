package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// FormatVersion is the archive layout version this build writes and reads.
const FormatVersion = 1

const manifestName = "manifest.json"

// Backup kinds.
const (
	KindManual     = "manual"
	KindPreRestore = "pre-restore"
	KindCLI        = "cli"
)

// Progress phases.
const (
	PhaseTables = "tables"
	PhaseFiles  = "files"
	PhaseVerify = "verify"
	PhaseLoad   = "load"
	PhaseLinks  = "links"
	PhaseSwap   = "swap"
)

// Manifest describes a backup archive.
type Manifest struct {
	Format         int                  `json:"format"`
	AppVersion     string               `json:"app_version"`
	Kind           string               `json:"kind"`
	CreatedAt      time.Time            `json:"created_at"`
	SourceDriver   string               `json:"source_driver"`
	KeyFingerprint string               `json:"key_fingerprint"`
	IncludeFiles   bool                 `json:"include_files"`
	Tables         []TableEntry         `json:"tables"`
	Files          map[string]FileStats `json:"files,omitempty"`
}

// TableEntry is one table in a backup.
type TableEntry struct {
	Name    string   `json:"name"`
	Rows    int64    `json:"rows"`
	SHA256  string   `json:"sha256"`
	Columns []string `json:"columns"`
}

// FileStats counts the files backed up from one folder.
type FileStats struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// Progress reports how far a backup or restore has got.
type Progress struct {
	Phase string `json:"phase"`
	Table string `json:"table,omitempty"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// ProgressFunc receives progress updates; it must not block.
type ProgressFunc func(Progress)

// Info is a backup file in the backup folder.
type Info struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// KeyFingerprint identifies an encryption key without revealing it.
func KeyFingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("whatomate-backup-key:" + key))
	return hex.EncodeToString(sum[:])[:12]
}

func orNop(p ProgressFunc) ProgressFunc {
	if p == nil {
		return func(Progress) {}
	}
	return p
}
