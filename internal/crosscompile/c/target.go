package c

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/mod/module"
	"github.com/kballard/go-shellquote"
)

const (
	linuxSysrootPath     = "bminor/glibc"
	linuxSysrootVersion  = "glibc-2.24"
	darwinSysrootPath    = "joseluisq/macosx-sdks"
	darwinSysrootVersion = "14.5"
)

// Config contains the facts required to prepare a C target.
type Config struct {
	Matrix    string
	Toolchain Toolchain
	Sysroot   string
}

// Target contains prepared C command defaults for one build target.
type Target struct {
	target          string
	systemName      string
	systemProcessor string
	autotoolsHost   string
	sysroot         string
	toolchain       Toolchain
	toolchainFile   string
	tempDir         string
}

// Sysroot returns the fixed compatibility sysroot Formula for a built-in C
// target.
func Sysroot(targetOS, targetArch string) (module.Version, bool) {
	switch targetOS {
	case "linux":
		switch targetArch {
		case "amd64", "arm64":
			return module.Version{Path: linuxSysrootPath, Version: linuxSysrootVersion}, true
		}
	case "darwin":
		switch targetArch {
		case "amd64", "arm64":
			return module.Version{Path: darwinSysrootPath, Version: darwinSysrootVersion}, true
		}
	}
	return module.Version{}, false
}

// NewTarget prepares C command defaults from config.
func NewTarget(config Config) (*Target, error) {
	target := config.Matrix
	if value, _, ok := strings.Cut(config.Matrix, "|"); ok {
		target = value
	}
	var systemName, systemProcessor, autotoolsHost string
	switch target {
	case "amd64-linux":
		systemName = "Linux"
		systemProcessor = "x86_64"
		autotoolsHost = "x86_64-linux-gnu"
	case "arm64-linux":
		systemName = "Linux"
		systemProcessor = "aarch64"
		autotoolsHost = "aarch64-linux-gnu"
	case "amd64-darwin":
		systemName = "Darwin"
		systemProcessor = "x86_64"
		autotoolsHost = "x86_64-apple-darwin"
	case "arm64-darwin":
		systemName = "Darwin"
		systemProcessor = "arm64"
		autotoolsHost = "aarch64-apple-darwin"
	default:
		return nil, fmt.Errorf("unsupported C target %s", target)
	}
	if err := validateToolchain(config.Toolchain); err != nil {
		return nil, fmt.Errorf("prepare C target %s: %w", target, err)
	}
	return &Target{
		target:          target,
		systemName:      systemName,
		systemProcessor: systemProcessor,
		autotoolsHost:   autotoolsHost,
		sysroot:         config.Sysroot,
		toolchain:       config.Toolchain,
	}, nil
}

// Close removes generated build-system configuration.
func (c *Target) Close() error {
	if c.tempDir == "" {
		return nil
	}
	return os.RemoveAll(c.tempDir)
}

// Use returns C target defaults for cmd. Explicit Formula settings are
// preserved.
func (c *Target) Use(cmd build.Command) build.Patch {
	base := filepath.Base(cmd.Name)
	if base == "configure" {
		return c.autotoolsPatch(cmd)
	}
	if filepath.Base(cmd.Name) != cmd.Name {
		return build.Patch{}
	}

	switch base {
	case "cmake":
		if isCMakeConfigure(cmd.Args) && !hasCMakeToolchain(cmd.Args) {
			if c.toolchainFile == "" {
				tempDir, err := os.MkdirTemp("", "llar-c-target-*")
				if err != nil {
					panic(fmt.Errorf("prepare CMake toolchain for %s: %w", c.target, err))
				}
				toolchainFile := filepath.Join(tempDir, "toolchain.cmake")
				if err := os.WriteFile(toolchainFile, []byte(c.cmakeToolchain()), 0o600); err != nil {
					_ = os.RemoveAll(tempDir)
					panic(fmt.Errorf("prepare CMake toolchain for %s: %w", c.target, err))
				}
				c.tempDir = tempDir
				c.toolchainFile = toolchainFile
			}
			patch := c.pkgConfigPatch(cmd.Env)
			patch.AppendArg = []string{"-DCMAKE_TOOLCHAIN_FILE:FILEPATH=" + c.toolchainFile}
			return patch
		}
	case "pkg-config":
		return c.pkgConfigPatch(cmd.Env)
	case "cc", "gcc", "clang":
		return commandPatch(c.toolchain.CC())
	case "c++", "g++", "clang++":
		return commandPatch(c.toolchain.CXX())
	case "ld", "ld.lld", "ld64.lld":
		return commandPatch(c.toolchain.Linker())
	case "ar", "llvm-ar":
		return build.Patch{Name: c.toolchain.Archiver()}
	case "ranlib", "llvm-ranlib":
		return build.Patch{Name: c.toolchain.Ranlib()}
	case "nm", "llvm-nm":
		return build.Patch{Name: c.toolchain.NM()}
	case "strip", "llvm-strip":
		return build.Patch{Name: c.toolchain.Strip()}
	}
	return build.Patch{}
}

func commandPatch(command []string) build.Patch {
	return build.Patch{Name: command[0], PrependArg: command[1:]}
}

func (c *Target) autotoolsPatch(cmd build.Command) build.Patch {
	env := append([]string(nil), cmd.Env...)
	env = setMissingEnv(env, "CC", shellquote.Join(c.toolchain.CC()...))
	env = setMissingEnv(env, "CXX", shellquote.Join(c.toolchain.CXX()...))
	env = setMissingEnv(env, "LD", shellquote.Join(c.toolchain.Linker()...))
	env = setMissingEnv(env, "AR", c.toolchain.Archiver())
	env = setMissingEnv(env, "RANLIB", c.toolchain.Ranlib())
	env = setMissingEnv(env, "NM", c.toolchain.NM())
	env = setMissingEnv(env, "STRIP", c.toolchain.Strip())
	if c.sysroot != "" {
		env = c.pkgConfigPatch(env).Env
	}

	for _, arg := range cmd.Args {
		if arg == "--host" || strings.HasPrefix(arg, "--host=") {
			return build.Patch{Env: env}
		}
	}

	configurePath := cmd.Name
	if !filepath.IsAbs(configurePath) {
		configurePath = filepath.Join(cmd.Dir, configurePath)
	}
	data, err := os.ReadFile(configurePath)
	if err != nil {
		panic(fmt.Errorf("inspect configure options for %s: %w", c.target, err))
	}
	// Only scripts that declare --host receive the Autoconf target tuple.
	// For example, zlib's custom configure declares CHOST but rejects --host.
	patch := build.Patch{Env: env}
	if bytes.Contains(data, []byte("--host")) {
		patch.AppendArg = []string{"--host=" + c.autotoolsHost}
	}
	return patch
}

func (c *Target) pkgConfigPatch(commandEnv []string) build.Patch {
	if c.sysroot == "" {
		return build.Patch{}
	}
	env := append([]string(nil), commandEnv...)
	libDirs, _ := envValue(env, "PKG_CONFIG_PATH")
	// Use stores LLAR dependency .pc directories in PKG_CONFIG_PATH. Restrict
	// lookup to them without rewriting their absolute installation prefixes.
	env = setMissingEnv(env, "PKG_CONFIG_LIBDIR", libDirs)
	return build.Patch{Env: env}
}

func (c *Target) cmakeToolchain() string {
	values := [][2]string{
		{"CMAKE_SYSTEM_NAME", c.systemName},
		{"CMAKE_SYSTEM_PROCESSOR", c.systemProcessor},
	}
	if c.systemName == "Darwin" {
		values = append(values,
			[2]string{"CMAKE_OSX_ARCHITECTURES", c.systemProcessor},
		)
		if c.sysroot != "" {
			values = append(values, [2]string{"CMAKE_OSX_SYSROOT", c.sysroot})
		}
	} else if c.sysroot != "" {
		values = append(values, [2]string{"CMAKE_SYSROOT", c.sysroot})
	}
	values = append(values,
		[2]string{"CMAKE_C_COMPILER", strings.Join(c.toolchain.CC(), ";")},
		[2]string{"CMAKE_CXX_COMPILER", strings.Join(c.toolchain.CXX(), ";")},
		[2]string{"CMAKE_LINKER", strings.Join(c.toolchain.Linker(), ";")},
		[2]string{"CMAKE_AR", c.toolchain.Archiver()},
		[2]string{"CMAKE_RANLIB", c.toolchain.Ranlib()},
		[2]string{"CMAKE_NM", c.toolchain.NM()},
		[2]string{"CMAKE_STRIP", c.toolchain.Strip()},
	)
	if c.sysroot != "" {
		values = append(values,
			[2]string{"CMAKE_FIND_ROOT_PATH_MODE_PROGRAM", "NEVER"},
			[2]string{"CMAKE_FIND_ROOT_PATH_MODE_LIBRARY", "ONLY"},
			[2]string{"CMAKE_FIND_ROOT_PATH_MODE_INCLUDE", "ONLY"},
			[2]string{"CMAKE_FIND_ROOT_PATH_MODE_PACKAGE", "ONLY"},
		)
	}
	var out strings.Builder
	for _, value := range values {
		fmt.Fprintf(&out, "if(NOT DEFINED %s)\n  set(%s \"%s\")\nendif()\n", value[0], value[0], cmakeEscape(value[1]))
	}
	return out.String()
}

func validateToolchain(toolchain Toolchain) error {
	commands := []struct {
		name    string
		command []string
	}{
		{"CC", toolchain.CC()},
		{"CXX", toolchain.CXX()},
		{"linker", toolchain.Linker()},
	}
	for _, command := range commands {
		if len(command.command) == 0 || command.command[0] == "" {
			return fmt.Errorf("%s is required", command.name)
		}
	}
	tools := []struct {
		name string
		path string
	}{
		{"archiver", toolchain.Archiver()},
		{"ranlib", toolchain.Ranlib()},
		{"nm", toolchain.NM()},
		{"strip", toolchain.Strip()},
	}
	for _, tool := range tools {
		if tool.path == "" {
			return fmt.Errorf("%s is required", tool.name)
		}
	}
	return nil
}

func isCMakeConfigure(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "--build", "--install", "--open", "--workflow":
		return false
	default:
		return true
	}
}

func hasCMakeToolchain(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-DCMAKE_TOOLCHAIN_FILE") || arg == "--toolchain" || strings.HasPrefix(arg, "--toolchain=") {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix), true
		}
	}
	return "", false
}

func setMissingEnv(env []string, key, value string) []string {
	if _, ok := envValue(env, key); ok {
		return env
	}
	return append(env, key+"="+value)
}

func cmakeEscape(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
