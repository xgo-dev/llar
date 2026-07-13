// Package cmake wraps the cmake configure/build/install workflow.
package cmake

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/goplus/llar/internal/execbroker"
)

type defineValue struct {
	value    string
	typeName string
}

// CMake drives CMake-based builds.
type CMake struct {
	sourceDir  string
	buildDir   string
	installDir string
	generator  string
	buildType  string
	toolchain  string
	defines    map[string]defineValue
}

// New returns a ready-to-use CMake.
func New(sourceDir, buildDir, installDir string) *CMake {
	return &CMake{
		sourceDir:  sourceDir,
		buildDir:   buildDir,
		installDir: installDir,
		defines:    make(map[string]defineValue),
	}
}

// Source overrides the source directory.
func (c *CMake) Source(dir string) { c.sourceDir = dir }

// Generator sets the CMake generator (e.g. "Ninja", "Unix Makefiles").
func (c *CMake) Generator(name string) { c.generator = name }

// BuildType sets CMAKE_BUILD_TYPE (e.g. "Release", "Debug").
func (c *CMake) BuildType(name string) { c.buildType = name }

// Toolchain sets CMAKE_TOOLCHAIN_FILE.
func (c *CMake) Toolchain(path string) { c.toolchain = path }

// Define adds a -D<key>:STRING=<value> definition.
func (c *CMake) Define(key, value string) {
	c.defines[key] = defineValue{value: value, typeName: "STRING"}
}

// DefineBool adds a -D<key>:BOOL=ON/OFF definition.
func (c *CMake) DefineBool(key string, value bool) {
	v := "OFF"
	if value {
		v = "ON"
	}
	c.defines[key] = defineValue{value: v, typeName: "BOOL"}
}

// Use configures the process environment so that CMake and compilers find
// headers, libraries and pkg-config files from a non-system dependency
// installed at root.
func (c *CMake) Use(root string) {
	includeDir := filepath.Join(root, "include")
	libDir := filepath.Join(root, "lib")
	pkgconfigDir := filepath.Join(libDir, "pkgconfig")

	_, errInclude := os.Stat(includeDir)
	_, errLib := os.Stat(libDir)
	hasInclude := errInclude == nil
	hasLib := errLib == nil

	if hasLib {
		if _, err := os.Stat(pkgconfigDir); err == nil {
			prependPath("PKG_CONFIG_PATH", pkgconfigDir)
		}
	}
	prependPath("CMAKE_PREFIX_PATH", root)
	if hasInclude {
		prependPath("CMAKE_INCLUDE_PATH", includeDir)
	}
	if hasLib {
		prependPath("CMAKE_LIBRARY_PATH", libDir)
	}

	if runtime.GOOS == "windows" {
		if hasInclude {
			prependPath("INCLUDE", includeDir)
		}
		if hasLib {
			prependPath("LIB", libDir)
		}
	} else {
		if hasInclude {
			appendFlag("CPPFLAGS", "-I"+includeDir)
		}
		if hasLib {
			appendFlag("LDFLAGS", "-L"+libDir)
		}
	}
}

// Configure runs "cmake -S <source> -B <build>" with all configured options.
// Extra args are appended at the end.
func (c *CMake) Configure(args ...string) error {
	if err := os.MkdirAll(c.buildDir, 0o755); err != nil {
		return err
	}
	cmakeArgs := []string{"-S", c.sourceDir, "-B", c.buildDir}
	if c.generator != "" {
		cmakeArgs = append(cmakeArgs, "-G", c.generator)
	}
	if c.installDir != "" {
		cmakeArgs = append(cmakeArgs, "-DCMAKE_INSTALL_PREFIX:STRING="+c.installDir)
	}
	if c.toolchain != "" {
		cmakeArgs = append(cmakeArgs, "-DCMAKE_TOOLCHAIN_FILE:STRING="+c.toolchain)
	}
	if c.buildType != "" {
		cmakeArgs = append(cmakeArgs, "-DCMAKE_BUILD_TYPE:STRING="+c.buildType)
	}
	cmakeArgs = append(cmakeArgs, c.definesArgs()...)
	cmakeArgs = append(cmakeArgs, args...)
	return c.run("cmake", cmakeArgs)
}

// Build runs "cmake --build <build>" with optional extra arguments.
func (c *CMake) Build(args ...string) error {
	cmakeArgs := []string{"--build", c.buildDir}
	if c.buildType != "" {
		cmakeArgs = append(cmakeArgs, "--config", c.buildType)
	}
	cmakeArgs = append(cmakeArgs, args...)
	return c.run("cmake", cmakeArgs)
}

// Install runs "cmake --install <build>" with optional extra arguments.
func (c *CMake) Install(args ...string) error {
	cmakeArgs := []string{"--install", c.buildDir}
	if c.installDir != "" {
		cmakeArgs = append(cmakeArgs, "--prefix", c.installDir)
	}
	cmakeArgs = append(cmakeArgs, args...)
	return c.run("cmake", cmakeArgs)
}

// OutputDir returns installDir if set, otherwise buildDir.
func (c *CMake) OutputDir() string {
	if c.installDir != "" {
		return c.installDir
	}
	return c.buildDir
}

func (c *CMake) run(name string, args []string) error {
	cmd := execbroker.Command(name, args...)
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func (c *CMake) definesArgs() []string {
	if len(c.defines) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.defines))
	for k := range c.defines {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys))
	for _, k := range keys {
		d := c.defines[k]
		args = append(args, "-D"+k+":"+d.typeName+"="+d.value)
	}
	return args
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
