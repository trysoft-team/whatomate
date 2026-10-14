package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ChunkMax is the largest upload chunk accepted. Servers taking chunks must
// allow request bodies of at least this size.
const ChunkMax = 8 << 20

// DefaultMaxUploadMB is the upload limit when none is configured.
const DefaultMaxUploadMB = 10240

var (
	ErrUploadNotFound = errors.New("upload not found")
	ErrNotBackupFile  = errors.New("choose a whatomate backup (.zip)")
	ErrBadChunk       = errors.New("bad chunk size")
)

// OffsetError means a chunk did not start where the upload ends.
type OffsetError struct{ Received int64 }

func (e *OffsetError) Error() string { return fmt.Sprintf("expected offset %d", e.Received) }

// TooLargeError means the file is over the upload limit.
type TooLargeError struct{ LimitMB int }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("the backup is larger than the %d MB upload limit", e.LimitMB)
}

// IncompleteError means Finish was called before every byte arrived.
type IncompleteError struct{ Received, Size int64 }

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("received %d of %d bytes", e.Received, e.Size)
}

// SpaceError means the disk has no room for the upload.
type SpaceError struct{ Err error }

func (e *SpaceError) Error() string { return e.Err.Error() }
func (e *SpaceError) Unwrap() error { return e.Err }

// Uploads tracks chunked uploads of backup files.
type Uploads struct {
	// Space checks free disk space; nil means EnsureSpace.
	Space func(dir string, need int64) error

	mu    sync.Mutex
	items map[string]*upload
}

type upload struct {
	mu       sync.Mutex
	dir      string
	name     string
	path     string
	size     int64
	received int64
}

func (u *Uploads) get(id string) *upload {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.items[id]
}

func (u *Uploads) take(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.items, id)
}

// Start creates an empty partial file in dir for a backup of size bytes.
// maxMB <= 0 means DefaultMaxUploadMB.
func (u *Uploads) Start(dir, name string, size int64, maxMB int) (string, error) {
	if !strings.HasSuffix(strings.ToLower(name), ".zip") || size <= 0 {
		return "", ErrNotBackupFile
	}
	if maxMB <= 0 {
		maxMB = DefaultMaxUploadMB
	}
	if size > int64(maxMB)<<20 {
		return "", &TooLargeError{LimitMB: maxMB}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	space := u.Space
	if space == nil {
		space = EnsureSpace
	}
	if err := space(dir, size); err != nil {
		return "", &SpaceError{Err: err}
	}
	id := uuid.NewString()
	p := filepath.Join(dir, UploadPrefix+id+".part")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	u.mu.Lock()
	if u.items == nil {
		u.items = map[string]*upload{}
	}
	u.items[id] = &upload{dir: dir, name: name, path: p, size: size}
	u.mu.Unlock()
	return id, nil
}

// Put appends body at offset and returns the bytes received so far.
func (u *Uploads) Put(id string, offset int64, body []byte) (int64, error) {
	up := u.get(id)
	if up == nil {
		return 0, ErrUploadNotFound
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if offset != up.received {
		return up.received, &OffsetError{Received: up.received}
	}
	if len(body) == 0 || len(body) > ChunkMax || up.received+int64(len(body)) > up.size {
		return up.received, ErrBadChunk
	}
	f, err := os.OpenFile(up.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return up.received, err
	}
	n, err := f.Write(body)
	_ = f.Close()
	up.received += int64(n)
	return up.received, err
}

// Finish renames a complete upload to a free name in its folder and returns
// the new path. The caller checks the file and removes it if it is no backup.
func (u *Uploads) Finish(id string) (string, error) {
	up := u.get(id)
	if up == nil {
		return "", ErrUploadNotFound
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.received != up.size {
		return "", &IncompleteError{Received: up.received, Size: up.size}
	}
	u.take(id)
	final := filepath.Join(up.dir, UploadName(up.dir, up.name))
	if err := os.Rename(up.path, final); err != nil {
		_ = os.Remove(up.path)
		return "", err
	}
	return final, nil
}

// Cancel drops a partial upload.
func (u *Uploads) Cancel(id string) {
	up := u.get(id)
	if up == nil {
		return
	}
	up.mu.Lock()
	_ = os.Remove(up.path)
	up.mu.Unlock()
	u.take(id)
}
