package storage

import (
	"context"
	"io"
)

// Uploader stores an object at key. S3Client and LocalFS both implement this
// so call recording can target S3 or local_path with the same call site.
type Uploader interface {
	Upload(ctx context.Context, key string, body io.Reader, contentType string) error
}
