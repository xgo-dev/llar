package downloader

import (
	"context"

	"github.com/goplus/llar/internal/artifact"
)

type Downloader interface {
	Type() string
	Download(ctx context.Context, source artifact.Source, checksum string) ([]byte, error)
}
