package c

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/mod/module"
)

func fakeToolchain(t *testing.T, targetOS string, compilerArgs ...string) Toolchain {
	t.Helper()
	cc := append([]string{"/llvm/bin/clang"}, compilerArgs...)
	cxx := append([]string{"/llvm/bin/clang++"}, compilerArgs...)
	linker := "/llvm/bin/ld.lld"
	if targetOS == "darwin" {
		linker = "/llvm/bin/ld64.lld"
	}
	return NewToolchain(
		cc,
		cxx,
		[]string{linker},
		"/llvm/bin/llvm-ar",
		"/llvm/bin/llvm-ranlib",
		"/llvm/bin/llvm-nm",
		"/llvm/bin/llvm-strip",
	)
}

func TestSysroot(t *testing.T) {
	linux := module.Version{Path: "bminor/glibc", Version: "glibc-2.24"}
	darwin := module.Version{Path: "joseluisq/macosx-sdks", Version: "14.5"}
	for _, arch := range []string{"amd64", "arm64"} {
		got, ok := Sysroot("linux", arch)
		if !ok || got != linux {
			t.Fatalf("Sysroot(linux, %s) = %+v, %v; want %+v, true", arch, got, ok, linux)
		}
		got, ok = Sysroot("darwin", arch)
		if !ok || got != darwin {
			t.Fatalf("Sysroot(darwin, %s) = %+v, %v; want %+v, true", arch, got, ok, darwin)
		}
	}
	for _, target := range [][2]string{{"darwin", "riscv64"}, {"linux", "riscv64"}, {"", "esp32"}} {
		if got, ok := Sysroot(target[0], target[1]); ok {
			t.Fatalf("Sysroot(%q, %q) = %+v, true; want unsupported", target[0], target[1], got)
		}
	}
}

func TestDarwinTarget(t *testing.T) {
	toolchain := fakeToolchain(t, "darwin", "--target=x86_64-apple-macos10.13", "-fuse-ld=lld", "-isysroot/sdk")
	target, err := NewTarget(Config{Matrix: "amd64-darwin|shared", Toolchain: toolchain, Sysroot: "/sdk"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })

	patch := target.Use(build.Command{Name: "cc", Args: []string{"-c", "a.c"}})
	want := []string{"--target=x86_64-apple-macos10.13", "-fuse-ld=lld", "-isysroot/sdk"}
	if !reflect.DeepEqual(patch.PrependArg, want) {
		t.Fatalf("PrependArg = %q, want %q", patch.PrependArg, want)
	}
	patch = target.Use(build.Command{Name: "cc", Args: []string{"a.o", "-shared"}})
	if !reflect.DeepEqual(patch.PrependArg, want) {
		t.Fatalf("link PrependArg = %q, want %q", patch.PrependArg, want)
	}

	patch = target.Use(build.Command{
		Name: "cc",
		Args: []string{"--target=custom", "-isysroot/custom", "-fuse-ld=custom"},
	})
	want = []string{"--target=x86_64-apple-macos10.13", "-fuse-ld=lld", "-isysroot/sdk"}
	if !reflect.DeepEqual(patch.PrependArg, want) {
		t.Fatalf("PrependArg = %q, want prepared defaults %q", patch.PrependArg, want)
	}

	configure := filepath.Join(t.TempDir(), "configure")
	if err := os.WriteFile(configure, []byte("#!/bin/sh\nCHOST=${CHOST-}\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	patch = target.Use(build.Command{Name: configure})
	if len(patch.AppendArg) != 0 {
		t.Fatalf("configure AppendArg = %q, want no unsupported arguments", patch.AppendArg)
	}
	for key, values := range map[string][]string{
		"CC": {"/llvm/bin/clang", "--target=x86_64-apple-macos10.13", "-fuse-ld=lld", "-isysroot/sdk"},
		"LD": {"/llvm/bin/ld64.lld"},
	} {
		got, _ := envValue(patch.Env, key)
		for _, value := range values {
			if !slices.Contains(strings.Fields(got), value) {
				t.Fatalf("%s = %q, want %q", key, got, value)
			}
		}
	}

	patch = target.Use(build.Command{Name: "cmake", Args: []string{"-S", ".", "-B", "build"}})
	data, err := os.ReadFile(strings.TrimPrefix(patch.AppendArg[0], "-DCMAKE_TOOLCHAIN_FILE:FILEPATH="))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"set(CMAKE_SYSTEM_NAME \"Darwin\")",
		"CMAKE_OSX_ARCHITECTURES",
		"CMAKE_OSX_SYSROOT",
		"CMAKE_LINKER",
		"/llvm/bin/ld64.lld",
		"x86_64-apple-macos10.13",
		"-fuse-ld=lld",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("toolchain file does not contain %q:\n%s", want, content)
		}
	}
}

func TestDarwinArm64Compiler(t *testing.T) {
	toolchain := fakeToolchain(t, "darwin", "--target=arm64-apple-macos11.0", "-fuse-ld=lld", "-isysroot/sdk")
	target, err := NewTarget(Config{Matrix: "arm64-darwin", Toolchain: toolchain, Sysroot: "/sdk"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })

	patch := target.Use(build.Command{Name: "cc"})
	if !slices.Contains(patch.PrependArg, "--target=arm64-apple-macos11.0") {
		t.Fatalf("PrependArg = %q, want prepared arm64 compiler target", patch.PrependArg)
	}
}

func TestBootstrapTargetOmitsSysroot(t *testing.T) {
	c, err := NewTarget(Config{Matrix: "arm64-linux", Toolchain: fakeToolchain(t, "linux", "--target=aarch64-linux-gnu", "-fuse-ld=lld")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	patch := c.Use(build.Command{Name: "cc", Args: []string{"-c", "a.c"}})
	if got, want := patch.PrependArg, []string{"--target=aarch64-linux-gnu", "-fuse-ld=lld"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PrependArg = %q, want %q", got, want)
	}
	if patch := c.Use(build.Command{Name: "pkg-config"}); patch.Env != nil {
		t.Fatalf("pkg-config Patch = %+v, want no sysroot environment", patch)
	}

	patch = c.Use(build.Command{Name: "cmake", Args: []string{"-S", ".", "-B", "build"}})
	data, err := os.ReadFile(strings.TrimPrefix(patch.AppendArg[0], "-DCMAKE_TOOLCHAIN_FILE:FILEPATH="))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "CMAKE_SYSROOT") {
		t.Fatalf("bootstrap CMake toolchain contains CMAKE_SYSROOT:\n%s", data)
	}
}

func TestUseCMakeWritesToolchainLazily(t *testing.T) {
	c := newTestTarget(t)
	if c.toolchainFile != "" || c.tempDir != "" {
		t.Fatalf("New created CMake files: toolchainFile=%q tempDir=%q", c.toolchainFile, c.tempDir)
	}

	c.Use(build.Command{Name: "cmake", Args: []string{"-S", ".", "-B", "build"}})
	path := c.toolchainFile
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"CMAKE_SYSTEM_NAME", "CMAKE_LINKER", "aarch64", "/llvm/bin/clang", "/llvm/bin/ld.lld", "aarch64-linux-gnu", "/sdk", "-fuse-ld=lld"} {
		if !strings.Contains(content, want) {
			t.Fatalf("toolchain file does not contain %q:\n%s", want, content)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("toolchain file still exists after Close: %v", err)
	}
}

func TestUseCMake(t *testing.T) {
	c := newTestTarget(t)
	patch := c.Use(build.Command{
		Name: "cmake",
		Args: []string{"-S", ".", "-B", "build"},
		Env:  []string{"PKG_CONFIG_PATH=/deps/lib/pkgconfig"},
	})
	if got, want := patch.AppendArg, []string{"-DCMAKE_TOOLCHAIN_FILE:FILEPATH=" + c.toolchainFile}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AppendArg = %q, want %q", got, want)
	}
	if got, ok := envValue(patch.Env, "PKG_CONFIG_SYSROOT_DIR"); ok {
		t.Fatalf("PKG_CONFIG_SYSROOT_DIR = %q, want unset", got)
	}
	if got, _ := envValue(patch.Env, "PKG_CONFIG_LIBDIR"); got != "/deps/lib/pkgconfig" {
		t.Fatalf("PKG_CONFIG_LIBDIR = %q, want /deps/lib/pkgconfig", got)
	}
	patch = c.Use(build.Command{Name: "cmake", Args: []string{"--build", "build"}})
	if len(patch.AppendArg) != 0 {
		t.Fatalf("build Patch = %+v, want no toolchain argument", patch)
	}
	patch = c.Use(build.Command{Name: "cmake", Args: []string{"-S", ".", "--toolchain", "/custom.cmake"}})
	if len(patch.AppendArg) != 0 {
		t.Fatalf("explicit toolchain Patch = %+v", patch)
	}
}

func TestUseDirectCommands(t *testing.T) {
	c := newTestTarget(t)
	patch := c.Use(build.Command{Name: "cc", Args: []string{"-c", "a.c"}})
	if patch.Name != "/llvm/bin/clang" {
		t.Fatalf("Name = %q", patch.Name)
	}
	want := []string{"--target=aarch64-linux-gnu", "-fuse-ld=lld", "--sysroot=/sdk"}
	if !reflect.DeepEqual(patch.PrependArg, want) {
		t.Fatalf("PrependArg = %q, want %q", patch.PrependArg, want)
	}
	patch = c.Use(build.Command{Name: "cc", Args: []string{"a.o", "-o", "a"}})
	if !reflect.DeepEqual(patch.PrependArg, want) {
		t.Fatalf("link PrependArg = %q, want %q", patch.PrependArg, want)
	}
	patch = c.Use(build.Command{Name: "cc", Args: []string{"-c", "a.c", "--target=custom", "--sysroot=/custom"}})
	if !reflect.DeepEqual(patch.PrependArg, want) {
		t.Fatalf("PrependArg = %q, want prepared defaults %q", patch.PrependArg, want)
	}
	if patch := c.Use(build.Command{Name: filepath.Join("custom", "cc")}); patch.Name != "" {
		t.Fatalf("explicit compiler path was rewritten: %+v", patch)
	}
	patch = c.Use(build.Command{Name: "ld"})
	if patch.Name != "/llvm/bin/ld.lld" {
		t.Fatalf("linker Name = %q, want /llvm/bin/ld.lld", patch.Name)
	}
}

func TestUseAutotools(t *testing.T) {
	c := newTestTarget(t)
	configure := filepath.Join(t.TempDir(), "configure")
	if err := os.WriteFile(configure, []byte("#!/bin/sh\n# options: --build=BUILD --host=HOST\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	patch := c.Use(build.Command{
		Name: configure,
		Args: []string{"--build=x86_64-apple-darwin"},
		Env:  []string{"CC=/custom/cc", "CFLAGS=-O2 --target=custom", "PKG_CONFIG_PATH=/deps/lib/pkgconfig"},
	})
	if got, _ := envValue(patch.Env, "CC"); got != "/custom/cc" {
		t.Fatalf("CC override = %q, want /custom/cc", got)
	}
	if got, _ := envValue(patch.Env, "CFLAGS"); got != "-O2 --target=custom" {
		t.Fatalf("CFLAGS = %q", got)
	}
	if got, _ := envValue(patch.Env, "LD"); got != "/llvm/bin/ld.lld" {
		t.Fatalf("LD = %q, want /llvm/bin/ld.lld", got)
	}
	if got, want := patch.AppendArg, []string{"--host=aarch64-linux-gnu"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AppendArg = %q, want %q", got, want)
	}
	if got, ok := envValue(patch.Env, "PKG_CONFIG_SYSROOT_DIR"); ok {
		t.Fatalf("PKG_CONFIG_SYSROOT_DIR = %q, want unset", got)
	}
	if got, _ := envValue(patch.Env, "PKG_CONFIG_LIBDIR"); got != "/deps/lib/pkgconfig" {
		t.Fatalf("PKG_CONFIG_LIBDIR = %q, want /deps/lib/pkgconfig", got)
	}

	patch = c.Use(build.Command{Name: configure, Args: []string{"--host=custom-linux"}})
	if len(patch.AppendArg) != 0 {
		t.Fatalf("explicit host AppendArg = %q, want no duplicate host", patch.AppendArg)
	}
}

func TestUsePkgConfig(t *testing.T) {
	c := newTestTarget(t)
	depPaths := strings.Join([]string{"/deps/a/lib/pkgconfig", "/deps/b/lib/pkgconfig"}, string(os.PathListSeparator))
	patch := c.Use(build.Command{Name: "pkg-config", Env: []string{"PKG_CONFIG_PATH=" + depPaths}})
	if got, ok := envValue(patch.Env, "PKG_CONFIG_SYSROOT_DIR"); ok {
		t.Fatalf("PKG_CONFIG_SYSROOT_DIR = %q, want unset", got)
	}
	got, _ := envValue(patch.Env, "PKG_CONFIG_LIBDIR")
	if got != depPaths {
		t.Fatalf("PKG_CONFIG_LIBDIR = %q, want %q", got, depPaths)
	}
	patch = c.Use(build.Command{Name: "pkg-config", Env: []string{"PKG_CONFIG_LIBDIR=/custom"}})
	if got, _ := envValue(patch.Env, "PKG_CONFIG_LIBDIR"); got != "/custom" {
		t.Fatalf("PKG_CONFIG_LIBDIR override = %q, want /custom", got)
	}
}

func newTestTarget(t *testing.T) *Target {
	t.Helper()
	toolchain := fakeToolchain(t, "linux", "--target=aarch64-linux-gnu", "-fuse-ld=lld", "--sysroot=/sdk")
	c, err := NewTarget(Config{Matrix: "arm64-linux|shared", Toolchain: toolchain, Sysroot: "/sdk"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
