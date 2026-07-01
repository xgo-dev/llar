package build

import (
	"context"
	"errors"
	"fmt"
	stdbuild "go/build"
	"io"
	"os"
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

// AllowLocal permits local target modules. It is intended for tests.
var AllowLocal bool

type TargetArtifact struct {
	Target   string
	Artifact artifact.Artifact
}

type Result struct {
	TargetArtifact TargetArtifact
	Deps           []Target
}

type makeResult struct {
	Archive  io.ReadSeeker
	Type     string
	Metadata string
	Deps     []Target
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

func (b *Builds) Build(ctx context.Context, req Request, info io.Writer) (Result, error) {
	if isLocalTarget(req.Target.Module) && !AllowLocal {
		return Result{}, fmt.Errorf("local target module is not allowed: %s", req.Target.Module)
	}
	if b.store == nil {
		return Result{}, errors.New("build store is required")
	}
	modulePath, err := targetModulePath(req.Target)
	if err != nil {
		return Result{}, err
	}
	matrixStr := req.Matrix.Combinations()[0]
	key := artifact.Key{
		Module:    modulePath,
		Version:   req.Target.Version,
		MatrixStr: matrixStr,
	}
	stored, ok, err := b.store.Get(ctx, key)
	if err != nil {
		return Result{}, fmt.Errorf("get artifact: %w", err)
	}
	if ok {
		return Result{
			TargetArtifact: TargetArtifact{
				Target:   modulePath + "@" + req.Target.Version,
				Artifact: stored,
			},
		}, nil
	}

	ch := b.flights.DoChan(flightKey(key), func() (any, error) {
		// GHCR tags are mutable. If two workers upload the same artifact key, the
		// last push wins, so GHCR cannot decide the canonical artifact. The artifact
		// row is the lock and source of truth.
		//
		// Example key: madler/zlib@v1.3.1 linux/amd64
		//
		// Scenario 1: artifact exists
		//   worker B -> Put(empty placeholder) -> source_url != ""
		//   worker B -> return worker A's artifact without make/upload
		//
		// Scenario 2: artifact does not exist
		//   worker A -> Put(empty placeholder) -> source_url == ""
		//   worker A -> GetOrUpdate -> lock row -> make/upload -> update source_url
		//   worker B -> wait for the row lock -> source_url != "" -> return A's artifact
		stored, err := b.store.Put(ctx, key, artifact.Artifact{})
		if err != nil {
			return nil, fmt.Errorf("put artifact placeholder: %w", err)
		}
		if stored.Source.URL != "" {
			return Result{
				TargetArtifact: TargetArtifact{
					Target:   modulePath + "@" + req.Target.Version,
					Artifact: stored,
				},
			}, nil
		}
		var deps []Target
		stored, err = b.store.GetOrUpdate(ctx, key, func() (artifact.Artifact, error) {
			made, err := b.make(ctx, req, info)
			if err != nil {
				return artifact.Artifact{}, err
			}
			deps = made.Deps
			// TODO: When llar make can expose dependency artifacts, upload and
			// Put those artifacts here too. Remote build currently stores only
			// the root target artifact returned by llar make.
			uploaded, archiveType, uploadType, err := b.upload(ctx, req, modulePath, matrixStr, made)
			if err != nil {
				return artifact.Artifact{}, err
			}
			return artifact.Artifact{
				Source: artifact.Source{
					Type: uploadType,
					URL:  uploaded.URL,
				},
				Type:     archiveType,
				Metadata: made.Metadata,
				Checksum: uploaded.Checksum,
			}, nil
		})
		if err != nil {
			return nil, fmt.Errorf("get or update artifact: %w", err)
		}
		return Result{
			TargetArtifact: TargetArtifact{
				Target:   modulePath + "@" + req.Target.Version,
				Artifact: stored,
			},
			Deps: deps,
		}, nil
	})
	select {
	case result := <-ch:
		if result.Err != nil {
			return Result{}, result.Err
		}
		return result.Val.(Result), nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
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
		Name:  modulePath,
		Tag:   req.Target.Version,
		Type:  archiveType,
		Attrs: uploadAttrs(uploadType, matrixStr, req.Matrix),
	})
	if err != nil {
		return upload.Result{}, "", "", fmt.Errorf("upload artifact: %w", err)
	}
	return uploaded, archiveType, uploadType, nil
}

func targetModulePath(target Target) (string, error) {
	module, local := localTargetPattern(target.Module)
	if !local {
		return target.Module, nil
	}
	cwd := module
	if cwd == "" || !filepath.IsAbs(cwd) {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	mods, err := modlocal.Resolve(cwd, module)
	if err != nil {
		return "", fmt.Errorf("read local target module %s: %w", target.Module, err)
	}
	if len(mods) != 1 || mods[0].Path == "" {
		return "", fmt.Errorf("local target module %s has empty path", target.Module)
	}
	return mods[0].Path, nil
}

func localTargetPattern(module string) (string, bool) {
	if !isLocalTarget(module) {
		return module, false
	}
	module = filepath.Clean(module)
	if module == "." {
		module = ""
	}
	return module, true
}

func isLocalTarget(module string) bool {
	return stdbuild.IsLocalImport(module) || filepath.IsAbs(module)
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
