package internal

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/mod/module"
)

func TestParseModuleArg(t *testing.T) {
	// parseModuleArg only parses CLI shape (local detection + @version split).
	// Pattern filtering (e.g. wildcard/escape checks) is covered in modlocal tests.
	tests := []struct {
		arg         string
		wantPattern string
		wantVersion string
		wantIsLocal bool
	}{
		{"owner/repo@v1.0.0", "owner/repo", "v1.0.0", false},
		{"owner/repo@1.0.0", "owner/repo", "1.0.0", false},
		{"owner/repo@", "owner/repo", "", false},
		{"owner/repo", "owner/repo", "", false},
		{"@", "", "", false},
		{"org/owner/repo@v2.0.0", "org/owner/repo", "v2.0.0", false},
		{"simple@latest", "simple", "latest", false},
		{"no-version", "no-version", "", false},
		{"multiple@at@signs", "multiple@at", "signs", false},
		// Local patterns
		{".", "", "", true},
		{"./", "", "", true},
		{"./@", "", "", true},
		{"./@v1.0.0", "", "v1.0.0", true},
		{"..", "..", "", true},
		{"../owner/repo", "../owner/repo", "", true},
		{"..@v1.0.0", "..", "v1.0.0", true},
		{"./owner/repo", "owner/repo", "", true},
		{"./owner/../repo", "repo", "", true},
		{"./owner/repo@", "owner/repo", "", true},
		{"./owner/repo@v1.0.0", "owner/repo", "v1.0.0", true},
		{"../owner/repo@v1.0.0", "../owner/repo", "v1.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			pattern, version, isLocal, err := parseModuleArg(tt.arg)
			if err != nil {
				t.Fatalf("parseModuleArg(%q) unexpected error: %v", tt.arg, err)
			}
			if pattern != tt.wantPattern {
				t.Errorf("parseModuleArg(%q) pattern = %q, want %q", tt.arg, pattern, tt.wantPattern)
			}
			if version != tt.wantVersion {
				t.Errorf("parseModuleArg(%q) version = %q, want %q", tt.arg, version, tt.wantVersion)
			}
			if isLocal != tt.wantIsLocal {
				t.Errorf("parseModuleArg(%q) isLocal = %v, want %v", tt.arg, isLocal, tt.wantIsLocal)
			}
		})
	}

	absDir := filepath.Join(t.TempDir(), "absmod")
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", absDir, err)
	}
	absCases := []struct {
		arg         string
		wantPattern string
		wantVersion string
	}{
		{absDir, filepath.Clean(absDir), ""},
		{absDir + "@v1.2.3", filepath.Clean(absDir), "v1.2.3"},
	}
	for _, tt := range absCases {
		t.Run(tt.arg, func(t *testing.T) {
			pattern, version, isLocal, err := parseModuleArg(tt.arg)
			if err != nil {
				t.Fatalf("parseModuleArg(%q) unexpected error: %v", tt.arg, err)
			}
			if !isLocal {
				t.Fatalf("parseModuleArg(%q) isLocal = false, want true", tt.arg)
			}
			if pattern != tt.wantPattern {
				t.Fatalf("parseModuleArg(%q) pattern = %q, want %q", tt.arg, pattern, tt.wantPattern)
			}
			if version != tt.wantVersion {
				t.Fatalf("parseModuleArg(%q) version = %q, want %q", tt.arg, version, tt.wantVersion)
			}
		})
	}
}

func TestParseModuleArg_InvalidDotVersion(t *testing.T) {
	invalidArgs := []string{
		".@v1.0.0",
		".@latest",
	}
	for _, arg := range invalidArgs {
		t.Run(arg, func(t *testing.T) {
			_, _, _, err := parseModuleArg(arg)
			if err == nil {
				t.Errorf("parseModuleArg(%q) expected error, got nil", arg)
			}
		})
	}
}

func setupTestSrcDir(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "lib"), 0755)
	os.WriteFile(filepath.Join(src, "lib", "libfoo.a"), []byte("archive"), 0644)
	os.MkdirAll(filepath.Join(src, "include"), 0755)
	os.WriteFile(filepath.Join(src, "include", "foo.h"), []byte("#pragma once"), 0644)
	return src
}

func TestOutputResult_CopyDir(t *testing.T) {
	src := setupTestSrcDir(t)
	dest := filepath.Join(t.TempDir(), "out")

	if err := outputResult(src, dest); err != nil {
		t.Fatalf("outputResult copy: %v", err)
	}

	// Verify files exist
	for _, rel := range []string{"lib/libfoo.a", "include/foo.h"} {
		path := filepath.Join(dest, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}

	// Verify content
	data, err := os.ReadFile(filepath.Join(dest, "lib", "libfoo.a"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "archive" {
		t.Errorf("content = %q, want %q", data, "archive")
	}
}

func TestOutputResult_Zip(t *testing.T) {
	src := setupTestSrcDir(t)
	dest := filepath.Join(t.TempDir(), "out.zip")

	if err := outputResult(src, dest); err != nil {
		t.Fatalf("outputResult zip: %v", err)
	}

	// Open and verify zip contents
	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	want := map[string]bool{
		"lib/libfoo.a":  false,
		"include/foo.h": false,
	}
	for _, f := range r.File {
		if _, ok := want[f.Name]; ok {
			want[f.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("zip missing %s", name)
		}
	}
}

func TestOutputResult_ZipContent(t *testing.T) {
	src := setupTestSrcDir(t)
	dest := filepath.Join(t.TempDir(), "out.zip")

	if err := outputResult(src, dest); err != nil {
		t.Fatalf("outputResult zip: %v", err)
	}

	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	// Verify file content inside zip
	for _, f := range r.File {
		if f.Name == "lib/libfoo.a" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open zip entry: %v", err)
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			if string(data) != "archive" {
				t.Errorf("zip content of lib/libfoo.a = %q, want %q", data, "archive")
			}
		}
	}
}

func TestOutputResult_EmptyDir(t *testing.T) {
	src := t.TempDir() // empty directory

	// Copy empty dir
	destDir := filepath.Join(t.TempDir(), "empty-out")
	if err := outputResult(src, destDir); err != nil {
		t.Fatalf("outputResult copy empty dir: %v", err)
	}
	info, err := os.Stat(destDir)
	if err != nil {
		t.Fatalf("dest dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("dest should be a directory")
	}

	// Zip empty dir
	destZip := filepath.Join(t.TempDir(), "empty.zip")
	if err := outputResult(src, destZip); err != nil {
		t.Fatalf("outputResult zip empty dir: %v", err)
	}
	r, err := zip.OpenReader(destZip)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()
	if len(r.File) != 0 {
		t.Errorf("zip of empty dir has %d entries, want 0", len(r.File))
	}
}

func TestOutputResult_InvalidSrc(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist")

	// Zip with invalid src
	dest := filepath.Join(t.TempDir(), "bad.zip")
	if err := outputResult(nonexistent, dest); err == nil {
		t.Error("expected error for nonexistent src dir")
	}

	// Copy with invalid src
	destDir := filepath.Join(t.TempDir(), "bad-out")
	if err := outputResult(nonexistent, destDir); err == nil {
		t.Error("expected error for nonexistent src dir")
	}
}

func TestOutputResult_NestedDirs(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "a", "b", "c"), 0755)
	os.WriteFile(filepath.Join(src, "a", "b", "c", "deep.txt"), []byte("deep"), 0644)
	os.WriteFile(filepath.Join(src, "a", "top.txt"), []byte("top"), 0644)

	// Test copy
	destDir := filepath.Join(t.TempDir(), "nested-out")
	if err := outputResult(src, destDir); err != nil {
		t.Fatalf("outputResult copy nested: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destDir, "a", "b", "c", "deep.txt"))
	if err != nil {
		t.Fatalf("missing deep file: %v", err)
	}
	if string(data) != "deep" {
		t.Errorf("deep.txt = %q, want %q", data, "deep")
	}

	// Test zip
	destZip := filepath.Join(t.TempDir(), "nested.zip")
	if err := outputResult(src, destZip); err != nil {
		t.Fatalf("outputResult zip nested: %v", err)
	}
	r, err := zip.OpenReader(destZip)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	found := false
	for _, f := range r.File {
		if f.Name == filepath.Join("a", "b", "c", "deep.txt") {
			found = true
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if string(data) != "deep" {
				t.Errorf("zip deep.txt = %q, want %q", data, "deep")
			}
		}
	}
	if !found {
		t.Error("zip missing a/b/c/deep.txt")
	}
}

// Integration tests that run the real `llar make` command.
// Requires network, git, and cmake.

func runMakeCmdStreams(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	// Save and restore cwd — builder.Build may os.Chdir during build
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	defer os.Chdir(origDir)

	// Reset flags to defaults before each run
	makeVerbose = true
	makeOutput = ""

	// Set os.Args to match what Cobra will see, so resolveMatrixStr works.
	origArgs := os.Args
	os.Args = append([]string{"llar", "make"}, args...)
	defer func() { os.Args = origArgs }()

	// Execute rootCmd in-process to keep test coverage. Because build output
	// flows through process-wide os.Stdout/os.Stderr (including nested build
	// commands), redirect to pipes and drain concurrently to avoid blocking on
	// full pipe buffers.
	oldStdout := os.Stdout
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	oldStderr := os.Stderr
	stderrR, stderrW, _ := os.Pipe()
	os.Stderr = stderrW
	defer func() { os.Stderr = oldStderr }()

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&stdoutBuf, stdoutR)
		stdoutDone <- copyErr
	}()
	stderrDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&stderrBuf, stderrR)
		stderrDone <- copyErr
	}()

	cmd := rootCmd
	cmd.SetArgs(append([]string{"make"}, args...))
	err = cmd.Execute()

	_ = stdoutW.Close()
	if copyErr := <-stdoutDone; copyErr != nil {
		t.Fatalf("failed to capture stdout: %v", copyErr)
	}
	_ = stderrW.Close()
	if copyErr := <-stderrDone; copyErr != nil {
		t.Fatalf("failed to capture stderr: %v", copyErr)
	}
	return stdoutBuf.String(), stderrBuf.String(), err
}

func runMakeCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	stdout, _, err := runMakeCmdStreams(t, args...)
	return stdout, err
}

func captureProcessStreams(t *testing.T) (*bytes.Buffer, *bytes.Buffer, func()) {
	t.Helper()

	oldStdout := os.Stdout
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdout = stdoutW

	oldStderr := os.Stderr
	stderrR, stderrW, _ := os.Pipe()
	os.Stderr = stderrW

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&stdoutBuf, stdoutR)
		stdoutDone <- copyErr
	}()
	stderrDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&stderrBuf, stderrR)
		stderrDone <- copyErr
	}()

	return &stdoutBuf, &stderrBuf, func() {
		_ = stdoutW.Close()
		if copyErr := <-stdoutDone; copyErr != nil {
			t.Fatalf("failed to capture stdout: %v", copyErr)
		}
		os.Stdout = oldStdout

		_ = stderrW.Close()
		if copyErr := <-stderrDone; copyErr != nil {
			t.Fatalf("failed to capture stderr: %v", copyErr)
		}
		os.Stderr = oldStderr
	}
}

func TestMakeReal_Verbose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	out, err := runMakeCmd(t, "-v", "madler/zlib@v1.3.1")
	if err != nil {
		t.Fatalf("llar make -v failed: %v", err)
	}
	if !strings.Contains(out, "-lz") {
		t.Errorf("expected metadata '-lz' in output, got: %s", out)
	}
}

func TestMakeReal_Silent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	out, err := runMakeCmd(t, "madler/zlib@v1.3.1")
	if err != nil {
		t.Fatalf("llar make failed: %v", err)
	}
	// Should only have metadata, no cmake output
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) != "-lz" {
		t.Errorf("expected only '-lz', got %d lines: %q", len(lines), out)
	}
}

func TestMakeReal_OutputZip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dest := filepath.Join(t.TempDir(), "zlib.zip")
	_, err := runMakeCmd(t, "-o", dest, "madler/zlib@v1.3.1")
	if err != nil {
		t.Fatalf("llar make -o zip failed: %v", err)
	}

	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	hasLib := false
	hasInclude := false
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "lib/") {
			hasLib = true
		}
		if strings.HasPrefix(f.Name, "include/") {
			hasInclude = true
		}
	}
	if !hasLib {
		t.Error("zip missing lib/ entries")
	}
	if !hasInclude {
		t.Error("zip missing include/ entries")
	}
}

func TestMakeLocal_RealDemoWithRemoteZlibDep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("cmake"); err != nil {
		t.Skip("cmake not found, skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping integration test")
	}

	formulaDir := setupLocalFormulas(t)

	origDir, _ := os.Getwd()
	os.Chdir(formulaDir)
	defer os.Chdir(origDir)

	workspaceDir := isolatedWorkspaceDir(t)
	matrixStr := computeMatrixStr()

	out, err := runMakeCmd(t, "-v", "./MeteorsLiu/llarzdepdemo@0.1.0")
	if err != nil {
		t.Fatalf("llar make local demo lib failed: %v", err)
	}
	t.Logf("llar make output:\n%s", out)
	if !strings.Contains(out, "-lzshim") {
		t.Errorf("expected metadata '-lzshim' in output, got: %s", out)
	}

	zlibInstall := filepath.Join(workspaceDir, fmt.Sprintf("madler/zlib@v1.3.1-%s", matrixStr))
	if _, err := os.Stat(filepath.Join(zlibInstall, "include", "zlib.h")); err != nil {
		t.Fatalf("remote zlib dependency not built correctly at %s: %v", zlibInstall, err)
	}

	demoInstall := filepath.Join(workspaceDir, fmt.Sprintf("MeteorsLiu/llarzdepdemo@0.1.0-%s", matrixStr))
	if _, err := os.Stat(filepath.Join(demoInstall, "include", "zshim.h")); err != nil {
		t.Fatalf("demo lib not built correctly at %s: %v", demoInstall, err)
	}
	if _, err := os.Stat(filepath.Join(demoInstall, "lib", "libzshim.a")); err != nil {
		// Some platforms may produce only shared library.
		if _, err2 := os.Stat(filepath.Join(demoInstall, "lib", "libzshim.dylib")); err2 != nil {
			t.Fatalf("demo lib artifact not found at %s: %v", demoInstall, err)
		}
	}
}

func TestMakeReal_InvalidModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, err := runMakeCmd(t, "nonexistent/fakepkg@v0.0.1")
	if err == nil {
		t.Fatal("expected error for nonexistent module")
	}
	if !strings.Contains(err.Error(), "failed to load modules") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMakeReal_NoVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// No version specified — modules.Load should still work (resolves latest)
	// or fail gracefully
	_, err := runMakeCmd(t, "nonexistent/fakepkg")
	if err == nil {
		t.Fatal("expected error for nonexistent module without version")
	}
}

// TODO: resolve dynamic library symlink issue
// func TestMakeReal_OutputDir(t *testing.T) {
// 	if testing.Short() {
// 		t.Skip("skipping integration test in short mode")
// 	}
//
// 	dest := filepath.Join(t.TempDir(), "zlib-out")
// 	_, err := runMakeCmd(t, "-o", dest, "madler/zlib@v1.3.1")
// 	if err != nil {
// 		t.Fatalf("llar make -o dir failed: %v", err)
// 	}
//
// 	// Verify lib and include directories exist
// 	if _, err := os.Stat(filepath.Join(dest, "lib")); err != nil {
// 		t.Errorf("missing lib/: %v", err)
// 	}
// 	if _, err := os.Stat(filepath.Join(dest, "include")); err != nil {
// 		t.Errorf("missing include/: %v", err)
// 	}
// }

// ---------------------------------------------------------------------------
// Local pattern tests (no network required)
// ---------------------------------------------------------------------------

type noopVCSRepo struct{}

func (m *noopVCSRepo) Tags(ctx context.Context) ([]string, error)                 { return nil, nil }
func (m *noopVCSRepo) Latest(ctx context.Context) (string, error)                 { return "", nil }
func (m *noopVCSRepo) At(ref, localDir string) fs.FS                              { return nil }
func (m *noopVCSRepo) Sync(ctx context.Context, ref, path, localDir string) error { return nil }

func withMockRemoteStore(t *testing.T, store repo.Store) {
	t.Helper()
	orig := newRemoteStore
	newRemoteStore = func() (repo.Store, error) { return store, nil }
	t.Cleanup(func() { newRemoteStore = orig })
}

// testdataFormulasDir returns the absolute path to testdata/formulas,
// immune to cwd changes caused by builder.Build.
func testdataFormulasDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to get caller info")
	}
	return filepath.Join(filepath.Dir(filename), "testdata", "formulas")
}

func setupLocalFormulas(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.CopyFS(tmp, os.DirFS(testdataFormulasDir(t))); err != nil {
		t.Fatalf("failed to copy testdata: %v", err)
	}
	return tmp
}

// computeMatrixStr returns the same matrix string that runMake computes.
func computeMatrixStr() string {
	matrix := formula.Matrix{
		Require: map[string][]string{
			"os":   {runtime.GOOS},
			"arch": {runtime.GOARCH},
		},
	}
	return matrix.Combinations()[0]
}

// prepopulateCache writes a build cache entry so builder.Build returns
// from cache without network access. Also creates the install dir with
// a dummy lib/liba.a file for outputResult verification.
func prepopulateCache(t *testing.T, workspaceDir, modPath, version, matrixStr, metadata string) {
	t.Helper()

	escaped := filepath.FromSlash(modPath)

	// Write .cache.json
	cacheDir := filepath.Join(workspaceDir, escaped)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}
	type buildEntry struct {
		Metadata  string    `json:"metadata"`
		BuildTime time.Time `json:"build_time"`
	}
	type buildCache struct {
		Cache map[string]*buildEntry `json:"cache"`
	}
	cache := buildCache{Cache: map[string]*buildEntry{
		version + "-" + matrixStr: {
			Metadata:  metadata,
			BuildTime: time.Now(),
		},
	}}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, ".cache.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Create install dir with a dummy file for outputResult
	installDir := filepath.Join(workspaceDir, fmt.Sprintf("%s@%s-%s", escaped, version, matrixStr))
	if err := os.MkdirAll(filepath.Join(installDir, "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "lib", "liba.a"), []byte("testlib"), 0644); err != nil {
		t.Fatal(err)
	}
}

// isolatedWorkspaceDir redirects HOME to a temp dir so the default workspace
// dir is isolated. Returns the workspace dir path.
func isolatedWorkspaceDir(t *testing.T) string {
	t.Helper()
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	return filepath.Join(cacheDir, ".llar", "workspaces")
}

func TestMake_RemoteStoreError(t *testing.T) {
	orig := newRemoteStore
	newRemoteStore = func() (repo.Store, error) {
		return nil, fmt.Errorf("mock store creation failed")
	}
	defer func() { newRemoteStore = orig }()

	_, err := runMakeCmd(t, "owner/repo@v1.0.0")
	if err == nil {
		t.Fatal("expected error when newRemoteStore fails")
	}
	if got := err.Error(); got != "mock store creation failed" {
		t.Errorf("error = %q, want %q", got, "mock store creation failed")
	}
}

func TestMakeLocal_BuildSuccess(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(filepath.Join(formulaDir, "test", "liba"))
	defer os.Chdir(origDir)

	// Isolate workspace and pre-populate cache
	workspaceDir := isolatedWorkspaceDir(t)
	matrixStr := computeMatrixStr()
	prepopulateCache(t, workspaceDir, "test/liba", "1.0.0", matrixStr, "-lA")

	out, err := runMakeCmd(t, "-v", "./@1.0.0")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Verify stdout contains exactly the metadata
	got := strings.TrimSpace(out)
	if got != "-lA" {
		t.Errorf("stdout = %q, want %q", got, "-lA")
	}

	// Verify cache file still exists in workspace
	cacheFile := filepath.Join(workspaceDir, "test", "liba", ".cache.json")
	if _, err := os.Stat(cacheFile); err != nil {
		t.Errorf("cache file should exist at %s: %v", cacheFile, err)
	}

	// Verify install dir was used
	installDir := filepath.Join(workspaceDir, fmt.Sprintf("test/liba@1.0.0-%s", matrixStr))
	libFile := filepath.Join(installDir, "lib", "liba.a")
	data, err := os.ReadFile(libFile)
	if err != nil {
		t.Fatalf("install dir missing lib/liba.a: %v", err)
	}
	if string(data) != "testlib" {
		t.Errorf("lib/liba.a content = %q, want %q", data, "testlib")
	}
}

func TestMakeLocal_VerboseWritesBuildOutputToStderr(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	store := repo.New(formulaDir, &noopVCSRepo{})
	ctx := context.Background()
	mods, err := modules.Load(ctx, module.Version{Path: "test/liba", Version: "1.0.0"}, modules.Options{
		FormulaStore: store,
	})
	if err != nil {
		t.Fatalf("modules.Load() failed: %v", err)
	}

	makeVerbose = true

	stdout, stderr, restore := captureProcessStreams(t)
	restoreBuildOutput, err := redirectBuildOutput(mods)
	if err != nil {
		t.Fatalf("redirectBuildOutput() failed: %v", err)
	}

	var out formula.BuildResult
	mods[0].OnBuild(nil, nil, &out)

	restoreBuildOutput()
	restore()

	if out.Metadata() != "-lA" {
		t.Fatalf("metadata = %q, want %q", out.Metadata(), "-lA")
	}

	if got := strings.TrimSpace(stdout.String()); got != "" {
		t.Fatalf("stdout = %q, want no build output", got)
	}
	if !strings.Contains(stderr.String(), "verbose build output") {
		t.Fatalf("stderr = %q, want verbose build output", stderr.String())
	}
}

func TestRedirectBuildOutput_SilentRestoresStreamsBeforeClosingDevNull(t *testing.T) {
	makeVerbose = false
	defer func() { makeVerbose = true }()

	savedStdout := os.Stdout
	savedStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdout: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stderr: %v", err)
	}
	defer func() {
		os.Stdout = savedStdout
		os.Stderr = savedStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	}()
	os.Stdout = stdoutW
	os.Stderr = stderrW

	savedCloseDevNull := closeDevNull
	closeDevNull = func(f *os.File) error {
		if os.Stdout != stdoutW {
			t.Fatalf("stdout was not restored before closing devnull")
		}
		if os.Stderr != stderrW {
			t.Fatalf("stderr was not restored before closing devnull")
		}
		return f.Close()
	}
	defer func() { closeDevNull = savedCloseDevNull }()

	restoreBuildOutput, err := redirectBuildOutput(nil)
	if err != nil {
		t.Fatalf("redirectBuildOutput() failed: %v", err)
	}
	restoreBuildOutput()
}

func TestBuildModule_SilentSuccess(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	store := repo.NewOverlayStore(
		repo.New(formulaDir, &noopVCSRepo{}),
		map[string]string{"test/liba": filepath.Join(formulaDir, "test", "liba")},
	)

	workspaceDir := isolatedWorkspaceDir(t)
	matrixStr := computeMatrixStr()
	prepopulateCache(t, workspaceDir, "test/liba", "1.0.0", matrixStr, "-lA")

	// Silent mode: buildModule redirects os.Stdout to /dev/null,
	// then restores before printing metadata
	makeVerbose = false
	makeOutput = ""
	defer func() { makeVerbose = true }()

	// Capture stdout to verify metadata output
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := buildModule(context.Background(), store, "test/liba", "1.0.0", matrixStr, false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)
	got := strings.TrimSpace(buf.String())
	if got != "-lA" {
		t.Errorf("stdout = %q, want %q", got, "-lA")
	}
}

func TestMakeLocal_BuildSuccessOutputZip(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(filepath.Join(formulaDir, "test", "liba"))
	defer os.Chdir(origDir)

	// With -o flag, buildModule creates a fresh temp workspace (no cache).
	// Build fails at git clone. Verify:
	// 1. Error is a build error (module resolution succeeded)
	// 2. Output zip was NOT created (build failed before outputResult)
	dest := filepath.Join(t.TempDir(), "out.zip")
	_, err := runMakeCmd(t, "-v", "-o", dest, "./@1.0.0")
	if err == nil {
		t.Fatal("expected build error for mock module with -o flag")
	}
	if !strings.Contains(err.Error(), "failed to build test/liba@1.0.0") {
		t.Errorf("expected build error, got: %v", err)
	}
	// Zip should not exist since build failed
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("output zip %s should not exist after build failure", dest)
	}
}

func TestMakeLocal_DotPattern(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(filepath.Join(formulaDir, "test", "liba"))
	defer os.Chdir(origDir)

	// ./@1.0.0: module resolution succeeds via overlay, build fails (no real git repo)
	_, err := runMakeCmd(t, "-v", "./@1.0.0")
	if err == nil {
		t.Fatal("expected build error for mock module")
	}
	// Must fail at build stage (git clone), not at module resolution
	if !strings.Contains(err.Error(), "failed to build test/liba@1.0.0") {
		t.Errorf("expected 'failed to build test/liba@1.0.0', got: %v", err)
	}
}

func TestMakeLocal_DotPatternNoVersion(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(filepath.Join(formulaDir, "test", "liba"))
	defer os.Chdir(origDir)

	// "." without version: modules.Load tries latestVersion via real vcs.NewRepo,
	// git ls-remote fails for nonexistent github.com/test/liba
	_, err := runMakeCmd(t, "-v", ".")
	if err == nil {
		t.Fatal("expected error for module without version and no real git repo")
	}
	if !strings.Contains(err.Error(), "failed to load modules") {
		t.Errorf("expected 'failed to load modules' error, got: %v", err)
	}
}

func TestMakeLocal_ExplicitPath(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(formulaDir)
	defer os.Chdir(origDir)

	// ./test/liba@1.0.0: resolution succeeds, build fails (no real git repo)
	_, err := runMakeCmd(t, "-v", "./test/liba@1.0.0")
	if err == nil {
		t.Fatal("expected build error for mock module")
	}
	if !strings.Contains(err.Error(), "failed to build test/liba@1.0.0") {
		t.Errorf("expected 'failed to build test/liba@1.0.0', got: %v", err)
	}
}

func TestMakeLocal_AbsolutePath(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(formulaDir)
	defer os.Chdir(origDir)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	absModDir := filepath.Join(cwd, "test", "liba")

	// absolute local path + pinned version: resolution succeeds, build fails (no real git repo)
	_, err = runMakeCmd(t, "-v", absModDir+"@1.0.0")
	if err == nil {
		t.Fatal("expected build error for mock module")
	}
	if !strings.Contains(err.Error(), "failed to build test/liba@1.0.0") {
		t.Errorf("expected 'failed to build test/liba@1.0.0', got: %v", err)
	}
}

func TestMakeLocal_NotFound(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(formulaDir)
	defer os.Chdir(origDir)

	_, err := runMakeCmd(t, "-v", "./nonexistent/repo@1.0.0")
	if err == nil {
		t.Fatal("expected error for nonexistent local module")
	}
	// Must fail at versions.json parsing (file not found)
	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("expected 'failed to parse' error, got: %v", err)
	}
}

func TestMakeLocal_InvalidDotSyntax(t *testing.T) {
	_, err := runMakeCmd(t, "-v", ".@v1.0.0")
	if err == nil {
		t.Fatal("expected error for .@version syntax")
	}
	want := `invalid local pattern ".@v1.0.0": use "./@version" instead of ".@version"`
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}
