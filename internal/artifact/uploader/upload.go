package uploader

import (
	"context"
	"io"
)

type Options struct {
	Name  string
	Tag   string
	Type  string
	Attrs map[string]string
}

type Result struct {
	URL      string
	Size     int64
	Checksum string
}

type Uploader interface {
	Type() string
	Upload(ctx context.Context, r io.ReadSeeker, opts Options) (Result, error)
}

type PackageSeeder interface {
	Seed(ctx context.Context, opts Options) error
}
