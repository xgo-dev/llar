package crosscompile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/crosscompile/c"
	"github.com/goplus/llar/internal/crosscompile/c/llvm"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/mod/module"
	ccmetadata "github.com/goplus/llar/x/metadata/cc"
	"github.com/kballard/go-shellquote"
)

// Config contains the build resources used to prepare a cross-compile target.
type Config struct {
	Store        repo.Store
	Matrix       formula.Matrix
	Stdout       io.Writer
	Stderr       io.Writer
	WorkspaceDir string
	Cache        cache.Cache
}

// customTarget uses the bootstrap toolchain to build libc itself. When a
// consumer such as zlib supplies cmake.Sysroot or autotools.Sysroot, it switches
// to the toolchain selected from that sysroot.
type customTarget struct {
	targetOS     string
	targetArch   string
	targetMatrix string
	bootstrap    *c.Target
	configured   *c.Target
}

func (t *customTarget) Use(command build.Command) build.Patch {
	root, err := commandSysroot(command)
	if err != nil {
		panic(err)
	}
	if root == "" && t.configured == nil {
		return t.bootstrap.Use(command)
	}
	if t.configured == nil {
		toolchain, err := llvm.New(llvm.Config{OS: t.targetOS, Arch: t.targetArch, Sysroot: root})
		if err != nil {
			panic(fmt.Errorf("prepare C toolchain for %s: %w", t.targetMatrix, err))
		}
		target, err := c.NewTarget(c.Config{
			Matrix:    t.targetMatrix,
			Toolchain: toolchain.Toolchain,
			Sysroot:   root,
		})
		if err != nil {
			panic(err)
		}
		t.configured = target
	}
	return t.configured.Use(command)
}

func (t *customTarget) Close() error {
	var configuredErr error
	if t.configured != nil {
		configuredErr = t.configured.Close()
	}
	return errors.Join(t.bootstrap.Close(), configuredErr)
}

func commandSysroot(command build.Command) (string, error) {
	var root string
	for _, entry := range command.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "CPPFLAGS", "CFLAGS", "CXXFLAGS", "LDFLAGS":
			metadata, err := ccmetadata.Parse(value)
			if err != nil {
				return "", fmt.Errorf("parse %s for cross compile target: %w", key, err)
			}
			if metadata.Sysroot() != "" {
				root = metadata.Sysroot()
			}
		}
	}
	metadata, err := ccmetadata.Parse(shellquote.Join(command.Args...))
	if err != nil {
		return "", fmt.Errorf("parse command flags for cross compile target: %w", err)
	}
	if metadata.Sysroot() != "" {
		root = metadata.Sysroot()
	}
	for _, arg := range command.Args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			continue
		}
		key = strings.TrimPrefix(key, "-D")
		key, _, _ = strings.Cut(key, ":")
		if key == "CMAKE_SYSROOT" || key == "CMAKE_OSX_SYSROOT" {
			root = value
		}
	}
	return root, nil
}

// Load returns the target used to cross-compile root. A nil target means the
// requested matrix is native or has no built-in C target policy.
func Load(ctx context.Context, root module.Version, config Config) (build.Target, error) {
	var targetOS, targetArch string
	if values := config.Matrix.Require["os"]; len(values) > 0 {
		targetOS = values[0]
	}
	if values := config.Matrix.Require["arch"]; len(values) > 0 {
		targetArch = values[0]
	}
	if targetOS == runtime.GOOS && targetArch == runtime.GOARCH {
		return nil, nil
	}

	// TODO: Add other language target policies alongside this C case when
	// they provide build.Target implementations.
	cSysroot, ok := c.Sysroot(targetOS, targetArch)
	if !ok {
		return nil, nil
	}
	targetMatrix := targetArch + "-" + targetOS
	bootstrapToolchain, err := llvm.New(llvm.Config{OS: targetOS, Arch: targetArch})
	if err != nil {
		return nil, fmt.Errorf("prepare C toolchain for %s: %w", targetMatrix, err)
	}
	bootstrapTarget, err := c.NewTarget(c.Config{
		Matrix:    targetMatrix,
		Toolchain: bootstrapToolchain.Toolchain,
	})
	if err != nil {
		return nil, err
	}

	_, customLibc := config.Matrix.Require["libc"]
	if root.Path == cSysroot.Path {
		return bootstrapTarget, nil
	}
	if customLibc {
		return &customTarget{
			targetOS:     targetOS,
			targetArch:   targetArch,
			targetMatrix: targetMatrix,
			bootstrap:    bootstrapTarget,
		}, nil
	}
	defer bootstrapTarget.Close()

	// The default sysroot has no dependencies, but modules.Load still owns
	// selecting the Formula whose fromVer applies to cSysroot.Version.
	sysrootMods, err := modules.Load(ctx, cSysroot, modules.Options{
		FormulaStore: config.Store,
		Matrix:       config.Matrix,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load sysroot %s@%s: %w", cSysroot.Path, cSysroot.Version, err)
	}
	selectedSysroot := sysrootMods[0]
	sysrootBuilder, err := build.NewBuilder(build.Options{
		Store:        config.Store,
		MatrixStr:    config.Matrix.Combinations()[0],
		Stdout:       config.Stdout,
		Stderr:       config.Stderr,
		WorkspaceDir: config.WorkspaceDir,
		Cache:        config.Cache,
		Target:       bootstrapTarget,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create sysroot builder: %w", err)
	}
	sysrootResults, err := sysrootBuilder.Build(ctx, sysrootMods)
	if err != nil {
		return nil, fmt.Errorf("failed to build sysroot %s@%s: %w", selectedSysroot.Path, selectedSysroot.Version, err)
	}
	metadata, err := ccmetadata.Parse(sysrootResults[len(sysrootResults)-1].Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sysroot metadata for %s@%s: %w", selectedSysroot.Path, selectedSysroot.Version, err)
	}
	if metadata.Sysroot() == "" {
		return nil, fmt.Errorf("sysroot metadata for %s@%s has no sysroot", selectedSysroot.Path, selectedSysroot.Version)
	}

	configuredToolchain, err := llvm.New(llvm.Config{OS: targetOS, Arch: targetArch, Sysroot: metadata.Sysroot()})
	if err != nil {
		return nil, fmt.Errorf("prepare C toolchain for %s: %w", targetMatrix, err)
	}
	return c.NewTarget(c.Config{
		Matrix:    targetMatrix,
		Toolchain: configuredToolchain.Toolchain,
		Sysroot:   metadata.Sysroot(),
	})
}
