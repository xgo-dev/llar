package crosscompile

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/mod/module"
)

func TestLoadWithoutCrossCompileTarget(t *testing.T) {
	tests := []struct {
		name   string
		matrix formula.Matrix
	}{
		{
			name: "native",
			matrix: formula.Matrix{Require: map[string][]string{
				"os":   {runtime.GOOS},
				"arch": {runtime.GOARCH},
			}},
		},
		{
			name: "unsupported",
			matrix: formula.Matrix{Require: map[string][]string{
				"os":   {"unsupported"},
				"arch": {"unsupported"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := Load(context.Background(), module.Version{Path: "owner/repo", Version: "v1.0.0"}, Config{Matrix: tt.matrix})
			if err != nil {
				t.Fatal(err)
			}
			if target != nil {
				t.Fatalf("Load target = %T, want nil", target)
			}
		})
	}
}

func TestLoadBootstrapTarget(t *testing.T) {
	installFakeLLVM(t)
	_, triple := linuxCrossMatrix()
	tests := []struct {
		name string
		root module.Version
		libc bool
	}{
		{
			name: "custom libc",
			root: module.Version{Path: "owner/repo", Version: "v1.0.0"},
			libc: true,
		},
		{
			name: "sysroot formula",
			root: module.Version{Path: "bminor/glibc", Version: "glibc-2.24"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, _ := linuxCrossMatrix()
			if tt.libc {
				selected.Require["libc"] = []string{"custom"}
			}
			target, err := Load(context.Background(), tt.root, Config{Matrix: selected})
			if err != nil {
				t.Fatal(err)
			}
			if target == nil {
				t.Fatal("Load target = nil")
			}
			if closer, ok := target.(io.Closer); ok {
				t.Cleanup(func() { _ = closer.Close() })
			}
			patch := target.Use(build.Command{Name: "cc"})
			if filepath.Base(patch.Name) != "clang" {
				t.Fatalf("compiler = %q, want fake clang", patch.Name)
			}
			args := strings.Join(patch.PrependArg, " ")
			if !strings.Contains(args, "--target="+triple) {
				t.Fatalf("compiler args = %q, want target %q", args, triple)
			}
			if strings.Contains(args, "sysroot") {
				t.Fatalf("bootstrap compiler args = %q, want no sysroot", args)
			}
		})
	}
}

func TestLoadDefaultSysroot(t *testing.T) {
	installFakeLLVM(t)
	matrix, _ := linuxCrossMatrix()
	arch := matrix.Require["arch"][0]
	sysroot := fakeCrossSysroot(t, arch, "gnu")
	target, err := Load(context.Background(), module.Version{Path: "owner/repo", Version: "v1.0.0"}, Config{
		Store:        localSysrootFormulas(t),
		Matrix:       matrix,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
		WorkspaceDir: t.TempDir(),
		Cache:        metadataCache{metadata: "--sysroot=" + sysroot},
	})
	if err != nil {
		t.Fatal(err)
	}
	if target == nil {
		t.Fatal("Load target = nil")
	}
	if closer, ok := target.(io.Closer); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	patch := target.Use(build.Command{Name: "cc"})
	if args := strings.Join(patch.PrependArg, " "); !strings.Contains(args, "--sysroot="+sysroot) {
		t.Fatalf("compiler args = %q, want configured sysroot", args)
	}
}

func TestLoadCustomLibcUsesCommandSysroot(t *testing.T) {
	installFakeLLVM(t)
	matrix, _ := linuxCrossMatrix()
	arch := matrix.Require["arch"][0]
	matrix.Require["libc"] = []string{"custom"}
	target, err := Load(context.Background(), module.Version{Path: "owner/repo", Version: "v1.0.0"}, Config{Matrix: matrix})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := target.(io.Closer); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}

	sysroot := fakeCrossSysroot(t, arch, "musl")
	patch := target.Use(build.Command{Name: "cc", Args: []string{"--sysroot=" + sysroot}})
	args := strings.Join(patch.PrependArg, " ")
	wantTriple := "x86_64-linux-musl"
	if arch == "arm64" {
		wantTriple = "aarch64-linux-musl"
	}
	if !strings.Contains(args, "--target="+wantTriple) {
		t.Fatalf("compiler args = %q, want target %q", args, wantTriple)
	}
}

func TestCommandSysroot(t *testing.T) {
	tests := []struct {
		name    string
		command build.Command
		want    string
	}{
		{
			name:    "autotools environment",
			command: build.Command{Env: []string{"CFLAGS=-O2 --sysroot=/autotools-sdk -isysroot/autotools-sdk"}},
			want:    "/autotools-sdk",
		},
		{
			name:    "cmake definition",
			command: build.Command{Args: []string{"-DCMAKE_SYSROOT:STRING=/cmake-sdk"}},
			want:    "/cmake-sdk",
		},
		{
			name:    "compiler option",
			command: build.Command{Args: []string{"-c", "source.c", "-isysroot/compiler-sdk"}},
			want:    "/compiler-sdk",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := commandSysroot(tt.command)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("commandSysroot = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	matrix, _ := linuxCrossMatrix()
	root := module.Version{Path: "owner/repo", Version: "v1.0.0"}
	cacheErr := errors.New("cache unavailable")

	t.Run("toolchain", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		selected, _ := linuxCrossMatrix()
		selected.Require["libc"] = []string{"custom"}
		_, err := Load(context.Background(), root, Config{Matrix: selected})
		if err == nil || !strings.Contains(err.Error(), "prepare C toolchain") {
			t.Fatalf("Load error = %v, want toolchain error", err)
		}
	})

	t.Run("formula", func(t *testing.T) {
		installFakeLLVM(t)
		formulaErr := errors.New("formula unavailable")
		_, err := Load(context.Background(), root, Config{
			Store:  formulaStore{err: formulaErr},
			Matrix: matrix,
		})
		if !errors.Is(err, formulaErr) || !strings.Contains(err.Error(), "failed to load sysroot") {
			t.Fatalf("Load error = %v, want formula error", err)
		}
	})

	tests := []struct {
		name     string
		cache    cache.Cache
		wantText string
		wantErr  error
	}{
		{
			name:     "cache",
			cache:    metadataCache{err: cacheErr},
			wantText: "failed to build sysroot",
			wantErr:  cacheErr,
		},
		{
			name:     "metadata",
			cache:    metadataCache{metadata: "--sysroot"},
			wantText: "failed to parse sysroot metadata",
		},
		{
			name:     "missing sysroot",
			cache:    metadataCache{metadata: "-I/include"},
			wantText: "has no sysroot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeLLVM(t)
			_, err := Load(context.Background(), root, Config{
				Store:        localSysrootFormulas(t),
				Matrix:       matrix,
				Stdout:       io.Discard,
				Stderr:       io.Discard,
				WorkspaceDir: t.TempDir(),
				Cache:        tt.cache,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("Load error = %v, want %q", err, tt.wantText)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Load error = %v, want wrapped %v", err, tt.wantErr)
			}
		})
	}
}

func linuxCrossMatrix() (formula.Matrix, string) {
	arch := "amd64"
	triple := "x86_64-linux-gnu"
	if runtime.GOOS == "linux" && runtime.GOARCH == arch {
		arch = "arm64"
		triple = "aarch64-linux-gnu"
	}
	return formula.Matrix{Require: map[string][]string{
		"os":   {"linux"},
		"arch": {arch},
	}}, triple
}

func installFakeLLVM(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"clang", "clang++", "ld.lld", "llvm-ar", "llvm-ranlib", "llvm-nm", "llvm-strip"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func fakeCrossSysroot(t *testing.T, arch, environment string) string {
	t.Helper()
	root := t.TempDir()
	var loader string
	switch arch + "-" + environment {
	case "amd64-gnu":
		loader = "lib64/ld-linux-x86-64.so.2"
	case "amd64-musl":
		loader = "lib/ld-musl-x86_64.so.1"
	case "arm64-gnu":
		loader = "lib/ld-linux-aarch64.so.1"
	case "arm64-musl":
		loader = "lib/ld-musl-aarch64.so.1"
	default:
		t.Fatalf("unsupported fake Linux sysroot %s/%s", arch, environment)
	}
	path := filepath.Join(root, filepath.FromSlash(loader))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func localSysrootFormulas(t *testing.T) formulaStore {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return formulaStore{root: filepath.Join(filepath.Dir(filename), "..", "..", "testdata", "crosscompile-e2e", "formulas")}
}

type formulaStore struct {
	root string
	err  error
}

func (s formulaStore) ModuleFS(_ context.Context, modPath string) (fs.FS, error) {
	if s.err != nil {
		return nil, s.err
	}
	return os.DirFS(filepath.Join(s.root, filepath.FromSlash(modPath))), nil
}

func (formulaStore) LockModule(string) (func(), error) {
	return func() {}, nil
}

type metadataCache struct {
	metadata string
	err      error
}

func (c metadataCache) Get(context.Context, cache.Key) (cache.Entry, bool, error) {
	if c.err != nil {
		return cache.Entry{}, false, c.err
	}
	return cache.Entry{Metadata: c.metadata}, true, nil
}

func (metadataCache) Put(context.Context, cache.Key, fs.FS, cache.Entry) (cache.Entry, error) {
	return cache.Entry{}, errors.New("unexpected cache Put")
}
