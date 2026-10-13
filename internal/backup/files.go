package backup

import (
	"archive/zip"
	"context"
)

func restoreFiles(ctx context.Context, zr *zip.Reader, areas []FileArea, skip []string, progress ProgressFunc) error {
	return nil
}
