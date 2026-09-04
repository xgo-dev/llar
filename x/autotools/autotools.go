// Package autotools wraps the classic configure/make/make-install workflow.
package autotools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/goplus/llar/internal/execbroker"
	"github.com/goplus/llar/x/pkgconfig"
)

// AutoTools drives Autotools-style builds.
type AutoTools struct {
	sourceDir  string
	buildDir   string
	installDir string
}

// New returns a ready-to-use AutoTools.
func New(sourceDir, buildDir, installDir string) *AutoTools {
	return &AutoTools{
		sourceDir:  sourceDir,
		buildDir:   buildDir,
		installDir: installDir,
	}
}

// Source overrides the source directory.
func (a *AutoTools) Source(dir string) { a.sourceDir = dir }

// Use configures the process environment so that Autotools, compilers, and
// pkg-config find a non-system dependency installed at root.
func (a *AutoTools) Use(root string) {
	includeDir := filepath.Join(root, "include")
	libDir := filepath.Join(root, "lib")

	pkgconfig.Use(root)
	prependPath("CMAKE_PREFIX_PATH", root)
	if _, err := os.Stat(includeDir); err == nil {
		prependPath("CMAKE_INCLUDE_PATH", includeDir)
	}
	if _, err := os.Stat(libDir); err == nil {
		prependPath("CMAKE_LIBRARY_PATH", libDir)
	}

	if runtime.GOOS == "windows" {
		if _, err := os.Stat(includeDir); err == nil {
			prependPath("INCLUDE", includeDir)
		}
		if _, err := os.Stat(libDir); err == nil {
			prependPath("LIB", libDir)
		}
	} else {
		if _, err := os.Stat(includeDir); err == nil {
			appendFlag("CPPFLAGS", "-I"+includeDir)
		}
		if _, err := os.Stat(libDir); err == nil {
			appendFlag("LDFLAGS", "-L"+libDir)
		}
	}
}

// Configure runs the configure script from sourceDir in the build directory.
// --prefix is prepended automatically when installDir is set.
// Extra flags are appended after --prefix.
func (a *AutoTools) Configure(args ...string) {
	dir := a.workDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	exe := filepath.Join(a.sourceDir, "configure")
	if dir == "." {
		exe = "./configure"
	}
	flags := make([]string, 0, 1+len(args))
	if a.installDir != "" {
		prefix := a.installDir
		// Keep the pipe character out of generated Autotools shell recipes
		// without changing the physical output directory.
		if strings.ContainsRune(prefix, '|') {
			prefix = a.shellSafePrefix(dir, prefix)
		}
		flags = append(flags, "--prefix="+prefix)
	}
	a.run(exe, append(flags, args...))
}

func (a *AutoTools) shellSafePrefix(workDir, installDir string) string {
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		panic(err)
	}
	alias := filepath.Join(workDir, ".llar-prefix")
	target := installDir
	if !filepath.IsAbs(target) {
		target, err = filepath.Abs(filepath.Join(workDir, target))
		if err != nil {
			panic(err)
		}
	}
	// A dangling symlink cannot be used by mkdir -p to create its children.
	if err := os.MkdirAll(target, 0o755); err != nil {
		panic(err)
	}

	info, err := os.Lstat(alias)
	switch {
	case os.IsNotExist(err):
		if err := os.Symlink(target, alias); err != nil {
			panic(err)
		}
	case err != nil:
		panic(err)
	case info.Mode()&os.ModeSymlink == 0:
		panic(fmt.Errorf("autotools prefix alias %q already exists and is not a symlink", alias))
	default:
		got, err := os.Readlink(alias)
		if err != nil {
			panic(err)
		}
		if got != target {
			panic(fmt.Errorf("autotools prefix alias %q points to %q, want %q", alias, got, target))
		}
	}
	return alias
}

// Build runs "make" with optional extra arguments.
func (a *AutoTools) Build(args ...string) {
	a.run("make", args)
}

// Install runs "make install" with optional extra arguments appended.
func (a *AutoTools) Install(args ...string) {
	a.run("make", append([]string{"install"}, args...))
}

// OutputDir returns installDir if set, otherwise buildDir.
func (a *AutoTools) OutputDir() string {
	if a.installDir != "" {
		return a.installDir
	}
	return a.buildDir
}

func (a *AutoTools) workDir() string {
	if a.buildDir == "" {
		return "."
	}
	return a.buildDir
}

func (a *AutoTools) run(name string, args []string) {
	cmd := execbroker.Command(name, args...)
	cmd.Dir = a.workDir()
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	if err != nil {
		panic(err)
	}
}

// prependPath prepends value to a PATH-style env var.
func prependPath(key, value string) {
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	if cur := execbroker.Getenv(key); cur != "" {
		value += sep + cur
	}
	_ = execbroker.Setenv(key, value)
}

// appendFlag appends a space-separated flag to an env var.
func appendFlag(key, flag string) {
	if cur := execbroker.Getenv(key); cur != "" {
		flag = cur + " " + flag
	}
	_ = execbroker.Setenv(key, flag)
}
