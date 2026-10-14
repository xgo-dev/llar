// Package pkgconfig creates, configures, and queries pkg-config metadata.
package pkgconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goplus/llar/internal/execbroker"
)

// Use makes pkg-config metadata installed at root available to subsequent
// queries.
func Use(root string) {
	dir := filepath.Join(root, "lib", "pkgconfig")
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if current := execbroker.Getenv("PKG_CONFIG_PATH"); current != "" {
		dir += string(os.PathListSeparator) + current
	}
	_ = execbroker.Setenv("PKG_CONFIG_PATH", dir)
}

// Lookup returns the compiler and linker flags for name.
func Lookup(name string) (string, error) {
	return lookup(name, "--cflags", "--libs")
}

// CFlags returns the compiler flags for name.
func CFlags(name string) (string, error) {
	return lookup(name, "--cflags")
}

// Libs returns the linker flags for name.
func Libs(name string) (string, error) {
	return lookup(name, "--libs")
}

// StaticLibs returns the static linker flags for name.
func StaticLibs(name string) (string, error) {
	return lookup(name, "--static", "--libs")
}

func lookup(name string, args ...string) (string, error) {
	args = append(args, name)

	cmd := execbroker.Command("pkg-config", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("pkg-config %q: %w: %s", name, err, detail)
		}
		return "", fmt.Errorf("pkg-config %q: %w", name, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
