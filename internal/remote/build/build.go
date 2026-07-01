package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/modules/modlocal"
	"github.com/goplus/llar/internal/upload"
	"golang.org/x/sync/singleflight"
)

type Target struct {
	Module  string
	Version string
}

type Request struct {
	Target Target
	Matrix formula.Matrix
}

type TargetArtifact struct {
	Target   string
	Artifact artifact.Artifact
}

type makeResult struct {
	Archive  io.ReadSeeker
	Type     string
	Metadata string
}

type Options struct {
	Store       artifact.Store
	Uploader    upload.Uploader
	ArchiveType string
}

type Builds struct {
	store       artifact.Store
	uploader    upload.Uploader
	archiveType string
	flights     singleflight.Group
}

func New(opts Options) *Builds {
	return &Builds{
		store:       opts.Store,
		uploader:    opts.Uploader,
		archiveType: opts.ArchiveType,
	}
}

func (b *Builds) Build(ctx context.Context, req Request, info io.Writer) ([]TargetArtifact, error) {
	if b.store == nil {
		return nil, errors.New("build store is required")
	}
	modulePath, err := targetModulePath(req.Target)
	if err != nil {
		return nil, err
	}
	matrixStr := req.Matrix.Combinations()[0]
	key := artifact.Key{
		Module:    modulePath,
		Version:   req.Target.Version,
		MatrixStr: matrixStr,
	}
	stored, ok, err := b.store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get artifact: %w", err)
	}
	if ok {
		return []TargetArtifact{{
			Target:   modulePath + "@" + req.Target.Version,
			Artifact: stored,
		}}, nil
	}

	ch := b.flights.DoChan(flightKey(key), func() (any, error) {
		return b.makeUploadStore(ctx, req, key, modulePath, matrixStr, info)
	})
	select {
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.([]TargetArtifact), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *Builds) makeUploadStore(ctx context.Context, req Request, key artifact.Key, modulePath, matrixStr string, info io.Writer) ([]TargetArtifact, error) {
	made, err := b.make(ctx, req, info)
	if err != nil {
		return nil, err
	}
	uploaded, archiveType, uploadType, err := b.upload(ctx, req, modulePath, matrixStr, made)
	if err != nil {
		return nil, err
	}
	return b.put(ctx, req, key, modulePath, made, uploaded, archiveType, uploadType)
}

func (b *Builds) make(ctx context.Context, req Request, info io.Writer) (makeResult, error) {
	made, err := runLLARMake(ctx, req, info)
	if err != nil {
		return makeResult{}, fmt.Errorf("run build: %w", err)
	}
	if made.Archive == nil {
		return makeResult{}, errors.New("run build: archive is required")
	}
	return made, nil
}

func (b *Builds) upload(ctx context.Context, req Request, modulePath, matrixStr string, made makeResult) (upload.Result, string, string, error) {
	if b.uploader == nil {
		return upload.Result{}, "", "", errors.New("build uploader is required")
	}
	uploadType := b.uploader.Type()
	if uploadType == "" {
		return upload.Result{}, "", "", errors.New("build uploader type is required")
	}
	archiveType := made.Type
	if archiveType == "" {
		archiveType = b.archiveType
	}
	if archiveType == "" {
		archiveType = "tar.gz"
	}
	uploaded, err := b.uploader.Upload(ctx, made.Archive, upload.Options{
		Name:  modulePath + ":" + req.Target.Version,
		Type:  archiveType,
		Attrs: uploadAttrs(uploadType, matrixStr, req.Matrix),
	})
	if err != nil {
		return upload.Result{}, "", "", fmt.Errorf("upload artifact: %w", err)
	}
	return uploaded, archiveType, uploadType, nil
}

func (b *Builds) put(ctx context.Context, req Request, key artifact.Key, modulePath string, made makeResult, uploaded upload.Result, archiveType, uploadType string) ([]TargetArtifact, error) {
	stored, err := b.store.Put(ctx, key, artifact.Artifact{
		Source: artifact.Source{
			Type: uploadType,
			URL:  uploaded.URL,
		},
		Type:     archiveType,
		Metadata: made.Metadata,
		Checksum: uploaded.Checksum,
	})
	if err != nil {
		return nil, fmt.Errorf("put artifact: %w", err)
	}
	return []TargetArtifact{{
		Target:   modulePath + "@" + req.Target.Version,
		Artifact: stored,
	}}, nil
}

func targetModulePath(target Target) (string, error) {
	if !filepath.IsAbs(target.Module) {
		return target.Module, nil
	}
	mods, err := modlocal.Resolve(target.Module, target.Module)
	if err != nil {
		return "", fmt.Errorf("read local target module %s: %w", target.Module, err)
	}
	if len(mods) != 1 || mods[0].Path == "" {
		return "", fmt.Errorf("local target module %s has empty path", target.Module)
	}
	return mods[0].Path, nil
}

func uploadAttrs(uploadType, matrixStr string, matrix formula.Matrix) map[string]string {
	switch uploadType {
	case "ghcr":
		attrs := map[string]string{
			"org.llar.matrix": matrixStr,
		}
		if values := matrix.Require["os"]; len(values) > 0 && values[0] != "" {
			attrs["os"] = values[0]
		}
		if values := matrix.Require["arch"]; len(values) > 0 && values[0] != "" {
			attrs["arch"] = values[0]
		}
		return attrs
	default:
		return nil
	}
}

func flightKey(key artifact.Key) string {
	return key.Module + key.Version + key.MatrixStr
}
