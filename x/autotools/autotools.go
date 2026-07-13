// Package autotools wraps the classic configure/make/make-install workflow.
package autotools

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/goplus/llar/internal/execbroker"
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

// Use configures the process environment so that compilers and build tools
// find headers, libraries and pkg-config files from a non-system dependency
// installed at root.
func (a *AutoTools) Use(root string) {
	includeDir := filepath.Join(root, "include")
	libDir := filepath.Join(root, "lib")
	pkgconfigDir := filepath.Join(libDir, "pkgconfig")

	if _, err := os.Stat(pkgconfigDir); err == nil {
		prependPath("PKG_CONFIG_PATH", pkgconfigDir)
	}
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
func (a *AutoTools) Configure(args ...string) error {
	dir := a.workDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	exe := filepath.Join(a.sourceDir, "configure")
	if dir == "." {
		exe = "./configure"
	}
	flags := make([]string, 0, 1+len(args))
	if a.installDir != "" {
		flags = append(flags, "--prefix="+a.installDir)
	}
	return a.run(exe, append(flags, args...))
}

// Build runs "make" with optional extra arguments.
func (a *AutoTools) Build(args ...string) error {
	return a.run("make", args)
}

// Install runs "make install" with optional extra arguments appended.
func (a *AutoTools) Install(args ...string) error {
	return a.run("make", append([]string{"install"}, args...))
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

func (a *AutoTools) run(name string, args []string) error {
	cmd := execbroker.Command(name, args...)
	cmd.Dir = a.workDir()
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

// prependPath prepends value to a PATH-style env var.
func prependPath(key, value string) {
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	if cur := os.Getenv(key); cur != "" {
		value += sep + cur
	}
	os.Setenv(key, value)
}

// appendFlag appends a space-separated flag to an env var.
func appendFlag(key, flag string) {
	if cur := os.Getenv(key); cur != "" {
		flag = cur + " " + flag
	}
	os.Setenv(key, flag)
}
