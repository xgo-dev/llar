package modlocal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeVersionsJSON creates a versions.json with the given path field.
func writeVersionsJSON(t *testing.T, dir, modPath string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, _ := json.Marshal(map[string]any{"path": modPath})
	if err := os.WriteFile(filepath.Join(dir, "versions.json"), data, 0644); err != nil {
		t.Fatalf("write versions.json: %v", err)
	}
}

func TestResolve_CurrentDir(t *testing.T) {
	tmp := t.TempDir()
	writeVersionsJSON(t, tmp, "madler/zlib")

	mods, err := Resolve(tmp, "")
	if err != nil {
		t.Fatalf("Resolve(cwd, \"\") failed: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	if mods[0].Path != "madler/zlib" {
		t.Errorf("path = %q, want %q", mods[0].Path, "madler/zlib")
	}
	if mods[0].Dir != tmp {
		t.Errorf("dir = %q, want %q", mods[0].Dir, tmp)
	}
}

func TestResolve_CurrentDir_WalkUp(t *testing.T) {
	tmp := t.TempDir()
	writeVersionsJSON(t, tmp, "madler/zlib")

	// Create a subdirectory and resolve from there
	subdir := filepath.Join(tmp, "sub", "deep")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	mods, err := Resolve(subdir, "")
	if err != nil {
		t.Fatalf("Resolve from subdir failed: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	if mods[0].Path != "madler/zlib" {
		t.Errorf("path = %q, want %q", mods[0].Path, "madler/zlib")
	}
	if mods[0].Dir != tmp {
		t.Errorf("dir = %q, want %q", mods[0].Dir, tmp)
	}
}

func TestResolve_CurrentDir_WalkUpNearestVersionsWins(t *testing.T) {
	tmp := t.TempDir()
	top := filepath.Join(tmp, "top")
	mid := filepath.Join(top, "mid")
	leaf := filepath.Join(mid, "leaf")
	writeVersionsJSON(t, top, "top/root")
	writeVersionsJSON(t, mid, "top/mid")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}

	mods, err := Resolve(leaf, "")
	if err != nil {
		t.Fatalf("Resolve from nested leaf failed: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	if mods[0].Path != "top/mid" {
		t.Errorf("path = %q, want %q", mods[0].Path, "top/mid")
	}
	if mods[0].Dir != mid {
		t.Errorf("dir = %q, want %q", mods[0].Dir, mid)
	}
}

func TestResolve_CurrentDir_NoVersionsJSON(t *testing.T) {
	tmp := t.TempDir()
	_, err := Resolve(tmp, "")
	if err == nil {
		t.Fatal("expected error when no versions.json exists")
	}
}

func TestResolve_CurrentDir_EmptyPath(t *testing.T) {
	tmp := t.TempDir()
	// versions.json with empty path field
	data, _ := json.Marshal(map[string]any{"path": ""})
	os.WriteFile(filepath.Join(tmp, "versions.json"), data, 0644)

	_, err := Resolve(tmp, "")
	if err == nil {
		t.Fatal("expected error when path field is empty")
	}
}

func TestResolve_SingleLocal(t *testing.T) {
	tmp := t.TempDir()
	modDir := filepath.Join(tmp, "madler", "zlib")
	writeVersionsJSON(t, modDir, "madler/zlib")

	mods, err := Resolve(tmp, "madler/zlib")
	if err != nil {
		t.Fatalf("Resolve single local failed: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	if mods[0].Path != "madler/zlib" {
		t.Errorf("path = %q, want %q", mods[0].Path, "madler/zlib")
	}
	if mods[0].Dir != modDir {
		t.Errorf("dir = %q, want %q", mods[0].Dir, modDir)
	}
}

func TestResolve_SingleLocal_NormalizedWithinRoot(t *testing.T) {
	tmp := t.TempDir()
	modDir := filepath.Join(tmp, "owner", "repo")
	writeVersionsJSON(t, modDir, "owner/repo")

	mods, err := Resolve(tmp, "owner/sub/../repo")
	if err != nil {
		t.Fatalf("Resolve normalized path failed: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	if mods[0].Path != "owner/repo" {
		t.Errorf("path = %q, want %q", mods[0].Path, "owner/repo")
	}
	if mods[0].Dir != modDir {
		t.Errorf("dir = %q, want %q", mods[0].Dir, modDir)
	}
}

func TestResolve_SingleLocal_FromVersionDirParent(t *testing.T) {
	tmp := t.TempDir()
	modDir := filepath.Join(tmp, "madler", "zlib")
	writeVersionsJSON(t, modDir, "madler/zlib")
	verDir := filepath.Join(modDir, "1.0.0")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mods, err := Resolve(verDir, "..")
	if err != nil {
		t.Fatalf("Resolve from version dir failed: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	if mods[0].Path != "madler/zlib" {
		t.Errorf("path = %q, want %q", mods[0].Path, "madler/zlib")
	}
	if mods[0].Dir != modDir {
		t.Errorf("dir = %q, want %q", mods[0].Dir, modDir)
	}
}

func TestResolve_SingleLocal_AbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	modDir := filepath.Join(tmp, "madler", "zlib")
	writeVersionsJSON(t, modDir, "madler/zlib")

	mods, err := Resolve(modDir, modDir)
	if err != nil {
		t.Fatalf("Resolve absolute path failed: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	if mods[0].Path != "madler/zlib" {
		t.Errorf("path = %q, want %q", mods[0].Path, "madler/zlib")
	}
	if mods[0].Dir != modDir {
		t.Errorf("dir = %q, want %q", mods[0].Dir, modDir)
	}
}

func TestResolve_SingleLocal_NotFound(t *testing.T) {
	tmp := t.TempDir()
	_, err := Resolve(tmp, "nonexistent/repo")
	if err == nil {
		t.Fatal("expected error for nonexistent module")
	}
}

func TestResolve_CurrentDir_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "versions.json"), []byte("{invalid"), 0644)

	_, err := Resolve(tmp, "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestResolve_SingleLocal_EmptyPath(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "owner", "repo")
	os.MkdirAll(dir, 0755)
	data, _ := json.Marshal(map[string]any{"path": ""})
	os.WriteFile(filepath.Join(dir, "versions.json"), data, 0644)

	_, err := Resolve(tmp, "owner/repo")
	if err == nil {
		t.Fatal("expected error for empty path field")
	}
}

func TestValidatePattern_CaseMatrix(t *testing.T) {
	tmp := t.TempDir()
	moduleRoot := filepath.Join(tmp, "madler", "zlib")
	writeVersionsJSON(t, moduleRoot, "madler/zlib")
	verDir := filepath.Join(moduleRoot, "1.0.0")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(tmp, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	nestedTop := filepath.Join(tmp, "nest", "top")
	nestedRoot := filepath.Join(nestedTop, "mid")
	nestedLeaf := filepath.Join(nestedRoot, "leaf")
	writeVersionsJSON(t, nestedTop, "nest/top")
	writeVersionsJSON(t, nestedRoot, "nest/mid")
	if err := os.MkdirAll(nestedLeaf, 0o755); err != nil {
		t.Fatal(err)
	}
	absInside := moduleRoot
	absOutside, err := filepath.Abs(filepath.Join(string(os.PathSeparator), "llar-abs-pattern"))
	if err != nil {
		t.Fatalf("Abs() failed: %v", err)
	}

	tests := []struct {
		name       string
		cwd        string
		pattern    string
		wantErr    bool
		errContain string
	}{
		{"empty pattern", moduleRoot, "", false, ""},
		{"wildcard all", moduleRoot, "...", true, "wildcard"},
		{"wildcard owner", moduleRoot, "owner/...", true, "wildcard"},
		{"wildcard all with local prefix", moduleRoot, "./...", true, "wildcard"},
		{"wildcard owner with local prefix", moduleRoot, "./owner/...", true, "wildcard"},
		{"wildcard all with local prefix and version", moduleRoot, "./...@v1.0.0", true, "wildcard"},
		{"wildcard owner with local prefix and version", moduleRoot, "./owner/...@v1.0.0", true, "wildcard"},
		{"version dir to module root", verDir, "..", false, ""},
		{"version dir escapes root", verDir, "../..", true, "escapes local root"},
		{"local prefix parent escapes root", moduleRoot, "./..", true, "escapes local root"},
		{"local prefix parent escapes root nested", moduleRoot, "./../repo", true, "escapes local root"},
		{"normalize within root", moduleRoot, "sub/../repo", false, ""},
		{"rel equals dotdot", moduleRoot, "..", true, "escapes local root"},
		{"nested root allows one up", nestedLeaf, "..", false, ""},
		{"nested root blocks leaving nearest", nestedLeaf, "../..", true, "escapes local root"},
		{"absolute path within root", moduleRoot, absInside, false, ""},
		{"absolute path escapes root", moduleRoot, absOutside, true, "escapes local root"},
		{"no-root cwd escape", plain, "..", true, "escapes local root"},
		{"no-root normalize within cwd", plain, "a/../b", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePattern(tt.cwd, tt.pattern)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validatePattern(%q, %q) expected error", tt.cwd, tt.pattern)
				}
				if tt.errContain != "" && !strings.Contains(err.Error(), tt.errContain) {
					t.Fatalf("validatePattern(%q, %q) error = %q, want contains %q", tt.cwd, tt.pattern, err.Error(), tt.errContain)
				}
				return
			}
			if err != nil {
				t.Fatalf("validatePattern(%q, %q) unexpected error: %v", tt.cwd, tt.pattern, err)
			}
		})
	}
}
