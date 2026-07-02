package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/artifact/downloader"
	"github.com/goplus/llar/internal/artifact/uploader"
	"github.com/goplus/llar/mod/module"
)

type ArtifactCacheOptions struct {
	Store       artifact.Store
	Uploader    uploader.Uploader
	Downloader  downloader.Downloader
	ArchiveType string
	Attrs       map[string]string
}

type artifactCache struct {
	store       artifact.Store
	uploader    uploader.Uploader
	downloader  downloader.Downloader
	archiveType string
	attrs       map[string]string
}

type artifactMetadata struct {
	Metadata string   `json:"metadata"`
	Deps     []string `json:"deps,omitempty"`
}

func NewArtifactCache(opts ArtifactCacheOptions) Cache {
	archiveType := opts.ArchiveType
	if archiveType == "" {
		archiveType = "tar.gz"
	}
	return &artifactCache{
		store:       opts.Store,
		uploader:    opts.Uploader,
		downloader:  opts.Downloader,
		archiveType: archiveType,
		attrs:       opts.Attrs,
	}
}

func (c *artifactCache) Get(ctx context.Context, key CacheKey, outputDir string) (CacheEntry, bool, error) {
	if c.store == nil {
		return CacheEntry{}, false, errors.New("artifact cache store is required")
	}
	if c.downloader == nil {
		return CacheEntry{}, false, errors.New("artifact cache downloader is required")
	}
	stored, ok, err := c.store.Get(ctx, artifactKey(key))
	if err != nil {
		return CacheEntry{}, false, fmt.Errorf("get artifact: %w", err)
	}
	if !ok {
		return CacheEntry{}, false, nil
	}
	if stored.Source.Type != c.downloader.Type() {
		return CacheEntry{}, false, fmt.Errorf("artifact source type %q does not match downloader type %q", stored.Source.Type, c.downloader.Type())
	}
	body, err := c.downloader.Download(ctx, stored.Source, stored.Checksum)
	if err != nil {
		return CacheEntry{}, false, err
	}
	archiveType := stored.Type
	if archiveType == "" {
		archiveType = c.archiveType
	}
	tmpDir, err := os.MkdirTemp("", "llar-artifact-cache-*")
	if err != nil {
		return CacheEntry{}, false, err
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, "artifact."+archiveType)
	if err := os.WriteFile(archivePath, body, 0o644); err != nil {
		return CacheEntry{}, false, err
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return CacheEntry{}, false, err
	}
	if err := archiver.Unpack(archivePath, outputDir); err != nil {
		return CacheEntry{}, false, fmt.Errorf("unpack artifact: %w", err)
	}
	return CacheEntry{Metadata: stored.Metadata}, true, nil
}

func (c *artifactCache) Put(ctx context.Context, key CacheKey, outputDir string, entry CacheEntry) (CacheEntry, error) {
	if c.store == nil {
		return CacheEntry{}, errors.New("artifact cache store is required")
	}
	if c.uploader == nil {
		return CacheEntry{}, errors.New("artifact cache uploader is required")
	}
	uploadType := c.uploader.Type()
	if uploadType == "" {
		return CacheEntry{}, errors.New("artifact cache uploader type is required")
	}

	keyForStore := artifactKey(key)

	stored, ok, err := c.store.Get(ctx, keyForStore)
	if err != nil {
		return CacheEntry{}, fmt.Errorf("get artifact: %w", err)
	}
	if ok {
		entry.Metadata = stored.Metadata
		return entry, nil
	}

	opts := uploader.Options{
		Name:  key.Module.Path,
		Tag:   key.Module.Version,
		Type:  c.archiveType,
		Attrs: c.uploadAttrs(c.uploader.Type(), key),
	}
	if seeder, ok := c.uploader.(uploader.PackageSeeder); ok {
		if err := seeder.Seed(ctx, opts); err != nil {
			return CacheEntry{}, fmt.Errorf("seed artifact package: %w", err)
		}
	}
	uploaded, err := c.upload(ctx, outputDir, entry, uploadType, opts)
	if err != nil {
		return CacheEntry{}, err
	}
	// GHCR does not give concurrent writers a stable "already exists" result for
	// the artifact tag itself, so the database remains the source of truth.
	//
	// Scenario 1: artifact already exists
	//   Get -> artifact found -> skip seed/upload
	//
	// Scenario 2: two builders miss the cache at the same time
	//   builder A -> seed -> upload -> Put stores artifact A
	//   builder B -> seed -> upload -> Put returns artifact A instead of replacing it
	stored, err = c.store.Put(ctx, keyForStore, uploaded)
	if err != nil {
		return CacheEntry{}, fmt.Errorf("put artifact: %w", err)
	}
	entry.Metadata = stored.Metadata
	return entry, nil
}

func (c *artifactCache) upload(ctx context.Context, outputDir string, entry CacheEntry, uploadType string, opts uploader.Options) (artifact.Artifact, error) {
	tmpDir, err := os.MkdirTemp("", "llar-artifact-cache-*")
	if err != nil {
		return artifact.Artifact{}, err
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, "artifact."+c.archiveType)
	body, err := json.MarshalIndent(artifactMetadata{
		Metadata: entry.Metadata,
		Deps:     artifactDepStrings(entry.Deps),
	}, "", "  ")
	if err != nil {
		return artifact.Artifact{}, err
	}
	if err := archiver.Pack(outputDir, archivePath, json.RawMessage(append(body, '\n'))); err != nil {
		return artifact.Artifact{}, fmt.Errorf("pack artifact: %w", err)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return artifact.Artifact{}, err
	}
	defer archive.Close()

	uploaded, err := c.uploader.Upload(ctx, archive, opts)
	if err != nil {
		return artifact.Artifact{}, fmt.Errorf("upload artifact: %w", err)
	}
	return artifact.Artifact{
		Source: artifact.Source{
			Type: uploadType,
			URL:  uploaded.URL,
		},
		Type:     c.archiveType,
		Metadata: entry.Metadata,
		Checksum: uploaded.Checksum,
	}, nil
}

func (c *artifactCache) uploadAttrs(uploadType string, key CacheKey) map[string]string {
	switch uploadType {
	case "ghcr":
		attrs := make(map[string]string, len(c.attrs)+1)
		for k, v := range c.attrs {
			attrs[k] = v
		}
		attrs["org.llar.matrix"] = key.Matrix
		return attrs
	default:
		return nil
	}
}

func artifactKey(key CacheKey) artifact.Key {
	return artifact.Key{
		Module:    key.Module.Path,
		Version:   key.Module.Version,
		MatrixStr: key.Matrix,
	}
}

func artifactDepStrings(deps []module.Version) []string {
	if len(deps) == 0 {
		return nil
	}
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		out = append(out, dep.Path+"@"+dep.Version)
	}
	return out
}
