package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/goplus/llar/internal/artfact"
	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/upload"
	"github.com/goplus/llar/mod/module"
)

type ArtifactCacheOptions struct {
	Store        artifact.Store
	Uploader     upload.Uploader
	ArchiveType  string
	Attrs        map[string]string
	GHCRUsername string
	GHCRToken    string
}

type artifactCache struct {
	store        artifact.Store
	uploader     upload.Uploader
	archiveType  string
	attrs        map[string]string
	ghcrUsername string
	ghcrToken    string
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
		store:        opts.Store,
		uploader:     opts.Uploader,
		archiveType:  archiveType,
		attrs:        opts.Attrs,
		ghcrUsername: opts.GHCRUsername,
		ghcrToken:    opts.GHCRToken,
	}
}

func (c *artifactCache) Get(ctx context.Context, key CacheKey, outputDir string) (CacheEntry, bool, error) {
	if c.store == nil {
		return CacheEntry{}, false, errors.New("artifact cache store is required")
	}
	stored, ok, err := c.store.Get(ctx, artifactKey(key))
	if err != nil {
		return CacheEntry{}, false, fmt.Errorf("get artifact: %w", err)
	}
	if !ok {
		return CacheEntry{}, false, nil
	}
	body, err := c.download(ctx, stored.Source, stored.Checksum)
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
	tmpDir, err := os.MkdirTemp("", "llar-artifact-cache-*")
	if err != nil {
		return CacheEntry{}, err
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, "artifact."+c.archiveType)
	body, err := json.MarshalIndent(artifactMetadata{
		Metadata: entry.Metadata,
		Deps:     artifactDepStrings(entry.Deps),
	}, "", "  ")
	if err != nil {
		return CacheEntry{}, err
	}
	if err := archiver.Pack(outputDir, archivePath, json.RawMessage(append(body, '\n'))); err != nil {
		return CacheEntry{}, fmt.Errorf("pack artifact: %w", err)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return CacheEntry{}, err
	}
	defer archive.Close()

	uploaded, err := c.uploader.Upload(ctx, archive, upload.Options{
		Name:  key.Module.Path,
		Tag:   key.Module.Version,
		Type:  c.archiveType,
		Attrs: c.uploadAttrs(key),
	})
	if err != nil {
		return CacheEntry{}, fmt.Errorf("upload artifact: %w", err)
	}
	stored, err := c.store.Put(ctx, artifactKey(key), artifact.Artifact{
		Source: artifact.Source{
			Type: uploadType,
			URL:  uploaded.URL,
		},
		Type:     c.archiveType,
		Metadata: entry.Metadata,
		Checksum: uploaded.Checksum,
	})
	if err != nil {
		return CacheEntry{}, fmt.Errorf("put artifact: %w", err)
	}
	entry.Metadata = stored.Metadata
	return entry, nil
}

func (c *artifactCache) uploadAttrs(key CacheKey) map[string]string {
	attrs := make(map[string]string, len(c.attrs)+1)
	for k, v := range c.attrs {
		attrs[k] = v
	}
	attrs["org.llar.matrix"] = key.Matrix
	return attrs
}

func (c *artifactCache) download(ctx context.Context, source artifact.Source, checksum string) ([]byte, error) {
	switch source.Type {
	case "ghcr":
		return readGHCRBlob(ctx, source.URL, checksum, c.ghcrUsername, c.ghcrToken)
	default:
		return nil, fmt.Errorf("unsupported artifact source type %q", source.Type)
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

func readGHCRBlob(ctx context.Context, sourceURL, checksum, username, token string) ([]byte, error) {
	repo, digest, err := parseGHCRBlobURL(sourceURL)
	if err != nil {
		return nil, err
	}
	if checksum != "" && digest != checksum {
		return nil, fmt.Errorf("artifact source digest = %q, want checksum %q", digest, checksum)
	}
	ref, err := name.NewDigest("ghcr.io/"+repo+"@sha256:"+digest, name.WeakValidation)
	if err != nil {
		return nil, fmt.Errorf("GHCR blob ref: %w", err)
	}
	opts := []remote.Option{remote.WithContext(ctx)}
	if token != "" {
		opts = append(opts, remote.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: username,
			Password: token,
		})))
	}
	layer, err := remote.Layer(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("read GHCR blob %s: %w", ref.String(), err)
	}
	rc, err := layer.Compressed()
	if err != nil {
		return nil, fmt.Errorf("open GHCR blob %s: %w", ref.String(), err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read GHCR blob %s: %w", ref.String(), err)
	}
	return body, nil
}

func parseGHCRBlobURL(sourceURL string) (repo, digest string, err error) {
	const prefix = "https://ghcr.io/v2/"
	if !strings.HasPrefix(sourceURL, prefix) {
		return "", "", fmt.Errorf("source url = %q, want GHCR URL", sourceURL)
	}
	rest := strings.TrimPrefix(sourceURL, prefix)
	repo, digest, ok := strings.Cut(rest, "/blobs/sha256:")
	if !ok || repo == "" || digest == "" {
		return "", "", fmt.Errorf("source url = %q, want GHCR blob URL", sourceURL)
	}
	return repo, digest, nil
}
