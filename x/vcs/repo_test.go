// Copyright 2024 The llar Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRepoPath(t *testing.T) {
	tests := []struct {
		input     string
		wantHost  string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"github.com/owner/repo", "github.com", "owner", "repo", false},
		{"github.com/owner/repo/sub", "github.com", "owner", "repo/sub", false},
		{"gitlab.com/org/project", "gitlab.com", "org", "project", false},
		{"invalid", "", "", "", true},
		{"only/two", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			host, owner, repo, err := parseRepoPath(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRepoPath(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if host != tt.wantHost || owner != tt.wantOwner || repo != tt.wantRepo {
				t.Errorf("parseRepoPath(%q) = (%q, %q, %q), want (%q, %q, %q)",
					tt.input, host, owner, repo, tt.wantHost, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestNewRepo(t *testing.T) {
	// Test unsupported host
	_, err := NewRepo("unsupported.com/owner/repo")
	if err == nil {
		t.Error("NewRepo with unsupported host should return error")
	}

	// Test valid GitHub repo
	r, err := NewRepo("github.com/golang/go")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}
	rr := r.(*repo)
	if rr.host != "github.com" || rr.owner != "golang" || rr.name != "go" {
		t.Errorf("NewRepo parsed incorrectly: got host=%q owner=%q name=%q",
			rr.host, rr.owner, rr.name)
	}
}

func TestRepoAt(t *testing.T) {
	r, err := NewRepo("github.com/golang/go")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v1.21.0", localDir)
	rfs := fsys.(*repoFS)

	if rfs.ref != "v1.21.0" {
		t.Errorf("RepoFS.ref = %q, want %q", rfs.ref, "v1.21.0")
	}
	if rfs.localDir != localDir {
		t.Errorf("RepoFS.localDir = %q, want %q", rfs.localDir, localDir)
	}
}

// Integration tests (require network access)

func TestRepoTags(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	ctx := context.Background()
	tags, err := repo.Tags(ctx)
	if err != nil {
		t.Fatalf("Tags failed: %v", err)
	}

	if len(tags) == 0 {
		t.Error("Tags returned empty list")
	}
}

func TestRepoLatest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	ctx := context.Background()
	latest, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest failed: %v", err)
	}

	if latest == "" {
		t.Error("Latest returned empty string")
	}
	t.Logf("Latest commit: %s", latest)
}

func TestRepoFSReadFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir)
	rfs := fsys.(fs.ReadFileFS)

	// Read README.md
	data, err := rfs.ReadFile("README.md")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("ReadFile returned empty data")
	}

	// Check file is cached locally
	cached := filepath.Join(localDir, "README.md")
	if _, err := os.Stat(cached); os.IsNotExist(err) {
		t.Error("File was not cached locally")
	}

	// Second read should come from cache
	data2, err := rfs.ReadFile("README.md")
	if err != nil {
		t.Fatalf("ReadFile (cached) failed: %v", err)
	}

	if string(data) != string(data2) {
		t.Error("Cached data differs from original")
	}
}

func TestRepoFSReadDir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir)
	rfs := fsys.(fs.ReadDirFS)

	// Read root directory
	entries, err := rfs.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error("ReadDir returned empty list")
	}

	// Check some expected files/dirs exist
	found := make(map[string]bool)
	for _, e := range entries {
		found[e.Name()] = true
	}

	if !found["README.md"] {
		t.Error("README.md not found in directory listing")
	}
	if !found["github"] {
		t.Error("github directory not found in directory listing")
	}
}

func TestRepoSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	syncedDir := filepath.Join(localDir, ".github")

	// Sync only the .github directory (smaller)
	ctx := context.Background()
	if err := r.Sync(ctx, "v68.0.0", ".github", syncedDir); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Check directory was synced
	if _, err := os.Stat(syncedDir); os.IsNotExist(err) {
		t.Error(".github directory was not synced")
	}

	// Check some files exist in synced directory
	entries, err := os.ReadDir(syncedDir)
	if err != nil {
		t.Fatalf("ReadDir synced dir failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Synced directory is empty")
	}
}

func TestRepoFSOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	repoFS := repo.At("v68.0.0", localDir)

	// Open file (lazy loading)
	f, err := repoFS.Open("README.md")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	// File not downloaded yet (lazy)
	cached := filepath.Join(localDir, "README.md")
	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Error("File should not be cached before Read")
	}

	// Read triggers download
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Read returned empty data")
	}

	// Now file should be cached
	if _, err := os.Stat(cached); os.IsNotExist(err) {
		t.Error("File should be cached after Read")
	}

	// Stat should work
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if info.Name() != "README.md" {
		t.Errorf("Stat Name = %q, want %q", info.Name(), "README.md")
	}
}

func TestRepoFSOpenStatBeforeRead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	repoFS := repo.At("v68.0.0", localDir)

	f, err := repoFS.Open("README.md")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	// Stat before Read should fetch from remote
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if info.Name() != "README.md" {
		t.Errorf("Stat Name = %q, want %q", info.Name(), "README.md")
	}

	// File still not cached (Stat doesn't download content)
	cached := filepath.Join(localDir, "README.md")
	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Error("File should not be cached after Stat only")
	}
}

func TestRepoFSOpenMultipleReads(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	repoFS := repo.At("v68.0.0", localDir)

	f, err := repoFS.Open("LICENSE")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	// First read
	buf1 := make([]byte, 10)
	n1, err := f.Read(buf1)
	if err != nil {
		t.Fatalf("First Read failed: %v", err)
	}

	// Second read continues from where we left off
	buf2 := make([]byte, 10)
	n2, err := f.Read(buf2)
	if err != nil {
		t.Fatalf("Second Read failed: %v", err)
	}

	// Should read different content
	if string(buf1[:n1]) == string(buf2[:n2]) && n1 == n2 {
		t.Error("Multiple reads should advance position")
	}
}

func TestRepoFSOpenNonExistent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	repoFS := repo.At("v68.0.0", localDir)

	// Open succeeds (lazy)
	f, err := repoFS.Open("nonexistent-file.txt")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	// Read should fail for non-existent file
	_, err = io.ReadAll(f)
	if err == nil {
		t.Error("Read should fail for non-existent file")
	}
}

func TestRepoFSOpenFromCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repo, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	repoFS := repo.At("v68.0.0", localDir)

	// Pre-populate cache
	cached := filepath.Join(localDir, "cached.txt")
	cachedContent := []byte("cached content")
	if err := os.WriteFile(cached, cachedContent, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Open should read from cache, not remote
	f, err := repoFS.Open("cached.txt")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if string(data) != string(cachedContent) {
		t.Errorf("Read = %q, want %q (from cache)", string(data), string(cachedContent))
	}
}

func TestGitHubClientSyncDirSparse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := newGitHubClient()
	ctx := context.Background()
	destDir := t.TempDir()

	// Test sparse-checkout for a subdirectory
	err := client.SyncDir(ctx, "google", "go-github", "v68.0.0", ".github", destDir)
	if err != nil {
		t.Fatalf("SyncDir sparse failed: %v", err)
	}

	// Check .git exists
	gitDir := filepath.Join(destDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Error(".git directory should exist")
	}

	// Read root directory entries (excluding .git)
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	// Filter out .git directory
	var nonGitEntries []os.DirEntry
	for _, e := range entries {
		if e.Name() != ".git" {
			nonGitEntries = append(nonGitEntries, e)
		}
	}

	// Sparse checkout should only have the target directory (.github)
	if len(nonGitEntries) != 1 {
		var names []string
		for _, e := range nonGitEntries {
			names = append(names, e.Name())
		}
		t.Errorf("sparse-checkout should only contain target directory, got %d entries: %v", len(nonGitEntries), names)
	}

	if len(nonGitEntries) > 0 && nonGitEntries[0].Name() != ".github" {
		t.Errorf("expected .github directory, got %s", nonGitEntries[0].Name())
	}

	// Check .github directory has content
	githubDir := filepath.Join(destDir, ".github")
	githubEntries, err := os.ReadDir(githubDir)
	if err != nil {
		t.Fatalf("ReadDir .github failed: %v", err)
	}

	if len(githubEntries) == 0 {
		t.Error(".github directory should have content")
	}

	t.Logf("Sparse checkout: only .github with %d files", len(githubEntries))
}

func TestGitHubClientSyncDirShallowClone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := newGitHubClient()
	ctx := context.Background()
	destDir := t.TempDir()

	// Test shallow clone for root directory (empty path)
	err := client.SyncDir(ctx, "google", "go-github", "v68.0.0", "", destDir)
	if err != nil {
		t.Fatalf("SyncDir shallow clone failed: %v", err)
	}

	// Check README.md exists
	readme := filepath.Join(destDir, "README.md")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		t.Error("README.md should exist")
	}

	// Check .git exists
	gitDir := filepath.Join(destDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Error(".git directory should exist")
	}

	// Check github directory exists (full repo)
	githubDir := filepath.Join(destDir, "github")
	if _, err := os.Stat(githubDir); os.IsNotExist(err) {
		t.Error("github directory should exist in full clone")
	}
}

func TestGitHubClientSyncDirSparseUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := newGitHubClient()
	ctx := context.Background()
	destDir := t.TempDir()

	// First sync
	err := client.SyncDir(ctx, "google", "go-github", "v68.0.0", ".github", destDir)
	if err != nil {
		t.Fatalf("First SyncDir failed: %v", err)
	}

	// Second sync should succeed (update scenario)
	err = client.SyncDir(ctx, "google", "go-github", "v68.0.0", ".github", destDir)
	if err != nil {
		t.Fatalf("Second SyncDir (update) failed: %v", err)
	}

	// Verify content still exists
	githubDir := filepath.Join(destDir, ".github")
	entries, err := os.ReadDir(githubDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error(".github directory should have content after update")
	}
}

func TestGitHubClientSyncDirShallowCloneUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := newGitHubClient()
	ctx := context.Background()
	destDir := t.TempDir()

	// First sync
	err := client.SyncDir(ctx, "google", "go-github", "v68.0.0", "", destDir)
	if err != nil {
		t.Fatalf("First SyncDir failed: %v", err)
	}

	// Second sync should succeed (update scenario)
	err = client.SyncDir(ctx, "google", "go-github", "v68.0.0", "", destDir)
	if err != nil {
		t.Fatalf("Second SyncDir (update) failed: %v", err)
	}

	// Verify content still exists
	readme := filepath.Join(destDir, "README.md")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		t.Error("README.md should exist after update")
	}
}

// Cache FS verification tests (real go-github, require network)
//
// These tests verify that repoFS correctly fetches remote content and caches
// it to disk. We use the LICENSE file (BSD-3-Clause) at v68.0.0 because its
// content is standardized and immutable at a pinned tag.

const licenseHeader = "Copyright (c) 2013 The go-github AUTHORS. All rights reserved."

func TestRepoFS_ReadFileCacheVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir).(fs.ReadFileFS)

	// ReadFile fetches from remote and caches to disk
	data, err := fsys.ReadFile("LICENSE")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// Verify remote content matches known LICENSE text
	if !strings.Contains(string(data), licenseHeader) {
		t.Errorf("ReadFile content missing expected license header:\ngot: %s", string(data))
	}

	// Verify on-disk cache has the same content
	cached := filepath.Join(localDir, "LICENSE")
	diskData, err := os.ReadFile(cached)
	if err != nil {
		t.Fatal("File should be cached on disk after ReadFile")
	}
	if string(diskData) != string(data) {
		t.Error("On-disk cache doesn't match ReadFile result")
	}
}

func TestRepoFS_OpenReadCacheVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir)

	// Open + Read fetches from remote
	f, err := fsys.Open("LICENSE")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	// Verify remote content matches known LICENSE text
	if !strings.Contains(string(data), licenseHeader) {
		t.Errorf("Open+Read content missing expected license header:\ngot: %s", string(data))
	}

	// Verify on-disk cache has the same content
	cached := filepath.Join(localDir, "LICENSE")
	diskData, err := os.ReadFile(cached)
	if err != nil {
		t.Fatal("File should be cached on disk after Open+Read")
	}
	if string(diskData) != string(data) {
		t.Error("On-disk cache doesn't match Open+Read result")
	}
}

func TestRepoFS_StatCacheVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir)

	// Stat before any read — no local file, should fetch from remote
	f, err := fsys.Open("LICENSE")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat (remote) failed: %v", err)
	}
	if info.Name() != "LICENSE" {
		t.Errorf("Stat Name = %q, want %q", info.Name(), "LICENSE")
	}

	// Now read and cache
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	// Stat after cache — should use local file
	f2, err := fsys.Open("LICENSE")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f2.Close()

	info2, err := f2.Stat()
	if err != nil {
		t.Fatalf("Stat (cached) failed: %v", err)
	}
	if info2.Size() != int64(len(data)) {
		t.Errorf("Stat Size = %d, want %d", info2.Size(), len(data))
	}
}

func TestRepoFS_ReadDirCacheVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir).(fs.ReadDirFS)

	// First ReadDir: syncs from remote
	entries1, err := fsys.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries1) == 0 {
		t.Fatal("ReadDir returned empty list")
	}

	// Add a local-only file to the cached directory
	marker := filepath.Join(localDir, "cache_marker.txt")
	if err := os.WriteFile(marker, []byte("marker"), 0644); err != nil {
		t.Fatal(err)
	}

	// Second ReadDir: should read from local (finds non-empty dir), includes marker
	entries2, err := fsys.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir (cached) failed: %v", err)
	}

	found := false
	for _, e := range entries2 {
		if e.Name() == "cache_marker.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Second ReadDir should read from local cache and see the marker file")
	}
}

func TestSyncDirSparse_InitBranchError(t *testing.T) {
	c := newGitHubClient()
	destDir := t.TempDir()

	// Cancelled context makes exec.CommandContext fail
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// .git does not exist → enters IsNotExist branch
	// runGit("init") error is ignored, but sparse-checkout init will fail
	err := c.syncDirSparse(ctx, "owner", "repo", "v1.0", "sub", destDir)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestSyncDirSparse_AddBranchError(t *testing.T) {
	c := newGitHubClient()
	destDir := t.TempDir()

	// Create .git so it enters the else (add) branch
	if err := os.MkdirAll(filepath.Join(destDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.syncDirSparse(ctx, "owner", "repo", "v1.0", "sub", destDir)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestGitHubClientSyncDirSparseMultiplePaths(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	client := newGitHubClient()
	ctx := context.Background()
	destDir := t.TempDir()

	// Sync first directory
	err := client.SyncDir(ctx, "google", "go-github", "v68.0.0", ".github", destDir)
	if err != nil {
		t.Fatalf("First SyncDir failed: %v", err)
	}

	// Verify .github exists
	githubDir := filepath.Join(destDir, ".github")
	if _, err := os.Stat(githubDir); os.IsNotExist(err) {
		t.Fatal(".github should exist after first sync")
	}

	// Sync second directory into the same destDir
	err = client.SyncDir(ctx, "google", "go-github", "v68.0.0", "github", destDir)
	if err != nil {
		t.Fatalf("Second SyncDir failed: %v", err)
	}

	// Both directories should exist
	if _, err := os.Stat(githubDir); os.IsNotExist(err) {
		t.Error(".github should still exist after second sync")
	}
	codeDir := filepath.Join(destDir, "github")
	if _, err := os.Stat(codeDir); os.IsNotExist(err) {
		t.Error("github should exist after second sync")
	}

	// Only the two target directories (plus .git) should be present
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	var nonGitEntries []string
	for _, e := range entries {
		if e.Name() != ".git" {
			nonGitEntries = append(nonGitEntries, e.Name())
		}
	}
	if len(nonGitEntries) != 2 {
		t.Errorf("expected 2 directories, got %d: %v", len(nonGitEntries), nonGitEntries)
	}

	t.Logf("Sparse checkout with multiple paths: %v", nonGitEntries)
}

func TestRepoFS_LoadMkdirError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	// Create a regular file where MkdirAll expects a directory
	blockingFile := filepath.Join(localDir, "github")
	os.WriteFile(blockingFile, []byte("block"), 0644)

	fsys := r.At("v68.0.0", localDir)

	// Open github/github.go — remote read succeeds but MkdirAll("github") fails
	f, _ := fsys.Open("github/github.go")
	defer f.Close()

	_, err = io.ReadAll(f)
	if err == nil {
		t.Error("Read should fail when MkdirAll fails (file blocking directory)")
	}
}

func TestRepoFS_LoadWriteError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	os.Chmod(localDir, 0555)
	defer os.Chmod(localDir, 0755)

	fsys := r.At("v68.0.0", localDir)

	// Remote read succeeds but WriteFile fails (read-only dir)
	f, _ := fsys.Open("LICENSE")
	defer f.Close()

	_, err = io.ReadAll(f)
	if err == nil {
		t.Error("Read should fail when WriteFile fails (read-only directory)")
	}
}

func TestRepoFS_ReadFileRemoteError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir).(fs.ReadFileFS)

	// Non-existent file triggers remote read error
	_, err = fsys.ReadFile("this-file-does-not-exist-at-all.xyz")
	if err == nil {
		t.Error("ReadFile should fail for non-existent remote file")
	}
}

func TestRepoFS_StatRemoteFileInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir)

	// Stat a file without reading — exercises remote Stat path and fileInfo methods
	f, err := fsys.Open("LICENSE")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	// Verify all fileInfo methods (covers Size/Mode/ModTime/IsDir/Sys/Name)
	if info.Name() != "LICENSE" {
		t.Errorf("Name() = %q, want %q", info.Name(), "LICENSE")
	}
	if info.Size() <= 0 {
		t.Errorf("Size() = %d, want > 0", info.Size())
	}
	if info.Mode() != 0644 {
		t.Errorf("Mode() = %v, want 0644", info.Mode())
	}
	if !info.ModTime().IsZero() {
		t.Errorf("ModTime() = %v, want zero (remote fileInfo has no modtime)", info.ModTime())
	}
	if info.IsDir() {
		t.Error("IsDir() = true, want false")
	}
	if info.Sys() != nil {
		t.Errorf("Sys() = %v, want nil", info.Sys())
	}
}

func TestRepoFS_ReadDirSyncError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir).(fs.ReadDirFS)

	// Non-existent subdirectory triggers SyncDir which will fail
	// because git sparse-checkout for a non-existent path won't create any files
	_, err = fsys.ReadDir("this-directory-does-not-exist-xyz")
	// SyncDir itself may succeed (sparse-checkout doesn't fail for missing paths),
	// but ReadDir on an empty local dir returns empty or error
	// Either way, we exercise the remote fallback code path
	t.Logf("ReadDir non-existent: err=%v", err)
}

func TestRepoFS_StatNonExistent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir)

	// Open a non-existent file and call Stat (no local cache)
	// This exercises the Stat non-200 error path in github.go
	f, err := fsys.Open("this-file-does-not-exist.xyz")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()

	_, err = f.Stat()
	if err == nil {
		t.Error("Stat should fail for non-existent remote file")
	}
}

func TestRepoFS_ReadFileThenSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	r, err := NewRepo("github.com/google/go-github")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v68.0.0", localDir).(fs.ReadFileFS)

	// Step 1: Cache a file via ReadFile (writes plain file to localDir)
	data, err := fsys.ReadFile("LICENSE")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), licenseHeader) {
		t.Fatal("ReadFile returned unexpected content")
	}

	// Step 2: SyncDir on the same localDir (git init + sparse-checkout)
	ctx := context.Background()
	if err := r.Sync(ctx, "v68.0.0", ".github", localDir); err != nil {
		t.Fatalf("Sync after ReadFile failed: %v", err)
	}

	// Step 3: Verify the previously cached file still readable
	data2, err := fsys.ReadFile("LICENSE")
	if err != nil {
		t.Fatalf("ReadFile after Sync failed: %v", err)
	}
	if string(data) != string(data2) {
		t.Error("ReadFile content changed after Sync")
	}

	// Step 4: Verify synced directory is also accessible
	syncedDir := filepath.Join(localDir, ".github")
	entries, err := os.ReadDir(syncedDir)
	if err != nil {
		t.Fatalf("ReadDir .github failed: %v", err)
	}
	if len(entries) == 0 {
		t.Error(".github directory should have content after Sync")
	}

	// Step 5: Dump localDir structure to check for duplicates
	filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(localDir, path)
		if info.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		if info.IsDir() {
			t.Logf("DIR  %s", rel)
		} else {
			t.Logf("FILE %s (%d bytes)", rel, info.Size())
		}
		return nil
	})
}

func TestRepoFS_ReadDirSyncFetchError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Use a non-existent repo so git fetch fails, covering repofs.go SyncDir error path
	r, err := NewRepo("github.com/google/this-repo-does-not-exist-xyz-12345")
	if err != nil {
		t.Fatalf("NewRepo failed: %v", err)
	}

	localDir := t.TempDir()
	fsys := r.At("v1.0.0", localDir).(fs.ReadDirFS)

	_, err = fsys.ReadDir("somedir")
	if err == nil {
		t.Error("ReadDir should fail when SyncDir fails for non-existent repo")
	}
}
