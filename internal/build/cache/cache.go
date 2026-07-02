package cache

import (
	"context"
	"io/fs"

	"github.com/goplus/llar/mod/module"
)

type Key struct {
	Module module.Version
	Matrix string
}

type Entry struct {
	Metadata string
	Deps     []module.Version
}

type Cache interface {
	Get(ctx context.Context, key Key) (Entry, bool, error)
	Put(ctx context.Context, key Key, output fs.FS, entry Entry) (Entry, error)
}
