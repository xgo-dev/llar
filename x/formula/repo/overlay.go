package repo

import (
	"context"
	"io/fs"
	"os"
)

// NewOverlayStore creates a Store that serves modules from local directories
// when available, falling back to the remote store for everything else.
// The locals map keys are module paths (e.g. "madler/zlib") and values are
// absolute directory paths containing the formula files.
func NewOverlayStore(remote Store, locals map[string]string) Store {
	return &overlayStore{remote: remote, locals: locals}
}

type overlayStore struct {
	remote Store
	locals map[string]string // modPath -> local dir
}

func (s *overlayStore) ModuleFS(ctx context.Context, modPath string) (fs.FS, error) {
	if dir, ok := s.locals[modPath]; ok {
		return os.DirFS(dir), nil
	}
	return s.remote.ModuleFS(ctx, modPath)
}

func (s *overlayStore) LockModule(modPath string) (func(), error) {
	// Locking is module-path scoped across both local and remote sources,
	// because build cache/output paths are keyed by module path.
	// Always delegate lock ownership to the shared backing store.
	return s.remote.LockModule(modPath)
}
