// Package modlocal resolves local filesystem patterns into module paths
// and their corresponding directories. It scans for versions.json files
// to discover modules on disk.
package modlocal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goplus/llar/mod/versions"
)

// Module represents a module discovered from the local filesystem.
type Module struct {
	Path    string // module path (e.g. "madler/zlib")
	Dir     string // absolute directory containing the formula
	Version string // optional pinned version from pattern
}

// Resolve resolves a local file pattern to a list of modules.
//   - pattern="" (from "."): walk up from cwd to find versions.json
//   - pattern="owner/repo": read cwd/owner/repo/versions.json
func Resolve(cwd, pattern string) ([]Module, error) {
	if err := validatePattern(cwd, pattern); err != nil {
		return nil, err
	}

	switch {
	case pattern == "":
		return resolveCurrentDir(cwd)
	default:
		return resolveSingleLocal(cwd, pattern)
	}
}

func validatePattern(cwd, pattern string) error {
	if pattern == "" {
		return nil
	}
	if strings.Contains(pattern, "...") {
		return fmt.Errorf("invalid local pattern %q: \"...\" wildcard is not supported", pattern)
	}
	root := findLocalRoot(cwd)
	target := resolvePatternDir(cwd, pattern)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("invalid local pattern %q: %w", pattern, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("invalid local pattern %q: path escapes local root %q", pattern, root)
	}
	return nil
}

func resolvePatternDir(cwd, pattern string) string {
	if filepath.IsAbs(pattern) {
		return filepath.Clean(pattern)
	}
	return filepath.Clean(filepath.Join(cwd, pattern))
}

// findLocalRoot returns the nearest ancestor directory containing versions.json.
// If none is found, cwd itself is treated as the root boundary.
func findLocalRoot(cwd string) string {
	dir := filepath.Clean(cwd)
	for {
		if _, err := os.Stat(filepath.Join(dir, "versions.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// resolveCurrentDir finds the module in the current directory by walking up
// to find a versions.json file, then reading its path field.
func resolveCurrentDir(cwd string) ([]Module, error) {
	dir := cwd
	for {
		vFile := filepath.Join(dir, "versions.json")
		if _, err := os.Stat(vFile); err == nil {
			v, err := versions.Parse(vFile, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %s: %w", vFile, err)
			}
			if v.Path == "" {
				return nil, fmt.Errorf("versions.json at %s has no path field", dir)
			}
			return []Module{{Path: v.Path, Dir: dir}}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("no versions.json found in %s or any parent directory", cwd)
		}
		dir = parent
	}
}

// resolveSingleLocal resolves a single local module at cwd/pattern.
func resolveSingleLocal(cwd, pattern string) ([]Module, error) {
	dir := resolvePatternDir(cwd, pattern)
	vFile := filepath.Join(dir, "versions.json")
	v, err := versions.Parse(vFile, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", vFile, err)
	}
	if v.Path == "" {
		return nil, fmt.Errorf("versions.json at %s has no path field", dir)
	}
	return []Module{{Path: v.Path, Dir: dir}}, nil
}
