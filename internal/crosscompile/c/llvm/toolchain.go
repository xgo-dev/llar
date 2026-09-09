package llvm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goplus/llar/internal/crosscompile/c"
)

// Toolchain is a prepared LLVM C-family toolchain.
type Toolchain struct {
	c.Toolchain
}

// Config contains the target facts required to prepare LLVM commands.
type Config struct {
	OS      string
	Arch    string
	Sysroot string
}

// New prepares an LLVM Toolchain from commands available in PATH.
func New(config Config) (*Toolchain, error) {
	var triple, linkerName string
	switch config.Arch + "-" + config.OS {
	case "amd64-linux":
		triple = "x86_64-linux-"
		linkerName = "ld.lld"
	case "arm64-linux":
		triple = "aarch64-linux-"
		linkerName = "ld.lld"
	case "amd64-darwin":
		triple = "x86_64-apple-macos10.13"
		linkerName = "ld64.lld"
	case "arm64-darwin":
		triple = "arm64-apple-macos11.0"
		linkerName = "ld64.lld"
	default:
		return nil, fmt.Errorf("unsupported LLVM target %s/%s", config.OS, config.Arch)
	}
	if config.OS == "linux" {
		environment := "gnu"
		if config.Sysroot != "" {
			// The loader family is the sysroot's target-environment fact. For
			// example, any ld-musl* loader requires a Linux musl Clang triple.
			hasLoader := func(prefix string) (bool, error) {
				for _, dir := range []string{"lib", "lib64"} {
					entries, err := os.ReadDir(filepath.Join(config.Sysroot, dir))
					if os.IsNotExist(err) {
						continue
					}
					if err != nil {
						return false, err
					}
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), prefix) {
							return true, nil
						}
					}
				}
				return false, nil
			}
			environment = ""
			if ok, err := hasLoader("ld-linux"); err != nil {
				return nil, fmt.Errorf("inspect GNU loader in %s: %w", config.Sysroot, err)
			} else if ok {
				environment = "gnu"
			}
			if environment == "" {
				if ok, err := hasLoader("ld-musl"); err != nil {
					return nil, fmt.Errorf("inspect musl loader in %s: %w", config.Sysroot, err)
				} else if ok {
					environment = "musl"
				}
			}
			if environment == "" {
				return nil, fmt.Errorf("cannot determine LLVM target environment from sysroot %s: neither ld-linux* nor ld-musl* exists", config.Sysroot)
			}
		}
		triple += environment
	}
	find := func(name string) (string, error) {
		path, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("find prepared LLVM command %q: %w", name, err)
		}
		return path, nil
	}
	ccPath, err := find("clang")
	if err != nil {
		return nil, err
	}
	cxxPath, err := find("clang++")
	if err != nil {
		return nil, err
	}
	linker, err := find(linkerName)
	if err != nil {
		return nil, err
	}
	archiver, err := find("llvm-ar")
	if err != nil {
		return nil, err
	}
	ranlib, err := find("llvm-ranlib")
	if err != nil {
		return nil, err
	}
	nm, err := find("llvm-nm")
	if err != nil {
		return nil, err
	}
	strip, err := find("llvm-strip")
	if err != nil {
		return nil, err
	}
	cc := []string{ccPath, "--target=" + triple, "-fuse-ld=lld"}
	cxx := []string{cxxPath, "--target=" + triple, "-fuse-ld=lld"}
	if config.Sysroot != "" {
		flag := "--sysroot=" + config.Sysroot
		if config.OS == "darwin" {
			flag = "-isysroot" + config.Sysroot
		}
		cc = append(cc, flag)
		cxx = append(cxx, flag)
	}
	toolchain := c.NewToolchain(cc, cxx, []string{linker}, archiver, ranlib, nm, strip)
	return &Toolchain{Toolchain: toolchain}, nil
}
