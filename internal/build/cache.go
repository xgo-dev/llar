package build

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/mod/module"
)

// Workspace directory layout:
//
//	workspaceDir/
//	  <escaped>/                      # module-level dir (cacheDir)
//	    .cache.json                   # build cache: maps "version-matrix" → buildEntry
//	  <escaped>@<version>-<matrix>/   # build output dir (installDir)
//	    include/
//	    lib/
//	    ...
const cacheFile = ".cache.json"

// buildEntry contains metadata about a single successful build.
type buildEntry struct {
	Metadata  string    `json:"metadata"`
	BuildTime time.Time `json:"build_time"`
}

// buildCache maps "version-matrixString" keys to their build entries.
type buildCache struct {
	Cache map[string]*buildEntry `json:"cache"`
}

func cacheKey(version, matrix string) string {
	return version + "-" + matrix
}

func (c *buildCache) get(version, matrix string) (*buildEntry, bool) {
	entry, ok := c.Cache[cacheKey(version, matrix)]
	return entry, ok
}

func (c *buildCache) set(version, matrix string, entry *buildEntry) {
	if c.Cache == nil {
		c.Cache = make(map[string]*buildEntry)
	}
	c.Cache[cacheKey(version, matrix)] = entry
}

type localCache struct {
	workspaceDir string
}

// cacheDir returns the module-level directory for cache storage: workspaceDir/<escapedPath>.
func (b *Builder) cacheDir(modPath string) (string, error) {
	return (&localCache{workspaceDir: b.workspaceDir}).cacheDir(modPath)
}

// cacheDir returns the module-level directory for cache storage: workspaceDir/<escapedPath>.
func (c *localCache) cacheDir(modPath string) (string, error) {
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.workspaceDir, escaped), nil
}

// installDir returns the build output directory: workspaceDir/<escapedPath>@<version>-<matrix>.
func (b *Builder) installDir(modPath, version, target string) (string, error) {
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(b.workspaceDir, fmt.Sprintf("%s@%s-%s", escaped, version, target)), nil
}

// loadCache reads the cache file for a module from the workspace directory.
func (b *Builder) loadCache(modPath string) (*buildCache, error) {
	return (&localCache{workspaceDir: b.workspaceDir}).load(modPath)
}

func (c *localCache) Get(ctx context.Context, key cache.Key) (cache.Entry, bool, error) {
	cached, err := c.load(key.Module.Path)
	if err != nil {
		return cache.Entry{}, false, nil
	}
	entry, ok := cached.get(key.Module.Version, key.Matrix)
	if !ok {
		return cache.Entry{}, false, nil
	}
	return cache.Entry{Metadata: entry.Metadata}, true, nil
}

func (c *localCache) Put(ctx context.Context, key cache.Key, output fs.FS, entry cache.Entry) (cache.Entry, error) {
	cached, err := c.load(key.Module.Path)
	if err != nil {
		cached = &buildCache{}
	}
	cached.set(key.Module.Version, key.Matrix, &buildEntry{
		Metadata:  entry.Metadata,
		BuildTime: time.Now(),
	})
	return entry, c.save(key.Module.Path, cached)
}

func (c *localCache) load(modPath string) (*buildCache, error) {
	dir, err := c.cacheDir(modPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, cacheFile))
	if err != nil {
		return nil, err
	}
	var cache buildCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	return &cache, nil
}

// saveCache writes the cache file for a module to the workspace directory.
func (b *Builder) saveCache(modPath string, cache *buildCache) error {
	return (&localCache{workspaceDir: b.workspaceDir}).save(modPath, cache)
}

func (c *localCache) save(modPath string, cache *buildCache) error {
	dir, err := c.cacheDir(modPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, cacheFile), data, 0o644)
}
