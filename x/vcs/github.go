// Copyright 2024 The llar Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// githubClient implements client interface using raw GitHub URLs (no API).
type githubClient struct {
	httpClient *http.Client
	refs       githubRefs
}

// newGitHubClient creates a new GitHub client.
func newGitHubClient() *githubClient {
	return &githubClient{
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Tags returns all tags from the repository using git ls-remote.
func (g *githubClient) Tags(ctx context.Context, owner, repo string) ([]Tag, error) {
	return g.refs.list(ctx, owner, repo)
}

// Latest returns the latest commit hash on the default branch using git ls-remote.
func (g *githubClient) Latest(ctx context.Context, owner, repo string) (string, error) {
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	cmd := exec.CommandContext(ctx, "git", "ls-remote", repoURL, "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote: %w", err)
	}

	line := strings.TrimSpace(string(output))
	if line == "" {
		return "", fmt.Errorf("no HEAD ref found")
	}

	// Format: <sha>\tHEAD
	parts := strings.Split(line, "\t")
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid ls-remote output: %s", line)
	}

	return parts[0], nil
}

// Stat returns file info for the given path using HEAD request.
func (g *githubClient) Stat(ctx context.Context, owner, repo, ref, path string) (fs.FileInfo, error) {
	// Try as file first
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, ref, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// It's a file
		size := resp.ContentLength
		if size < 0 {
			size = 0
		}
		return &fileInfo{
			name:  filepath.Base(path),
			size:  size,
			mode:  0644,
			isDir: false,
		}, nil
	}

	return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
}

// ReadFile reads the content of a file using raw.githubusercontent.com.
func (g *githubClient) ReadFile(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, ref, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("file not found: %s", path)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d for %s", resp.StatusCode, path)
	}

	return io.ReadAll(resp.Body)
}

// SyncDir downloads a directory to the destination directory using git sparse-checkout.
// This is more efficient than tarball for large repositories when only a subdirectory is needed.
// Falls back to tarball method if sparse-checkout fails.
func (g *githubClient) SyncDir(ctx context.Context, owner, repo, ref, path, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	// Normalize path
	path = filepath.Clean(path)
	if path == "." {
		path = ""
	}

	// Try sparse-checkout first (more efficient for subdirectories)
	if path != "" {
		err := g.syncDirSparse(ctx, owner, repo, ref, path, destDir)
		if err == nil {
			return nil
		}
		// Fall back to tarball if sparse-checkout fails
	}

	// Use shallow clone for root directory or as fallback
	return g.syncDirShallowClone(ctx, owner, repo, ref, destDir)
}

// syncDirSparse uses git sparse-checkout to download only the specified directory.
// This is much more efficient for large repositories.
// If the directory already contains a git repo, it will fetch and update instead of re-cloning.
func (g *githubClient) syncDirSparse(ctx context.Context, owner, repo, ref, path, destDir string) error {
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	// Helper to run git commands in destDir
	runGit := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = destDir
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0", // Disable interactive prompts
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w\n%s", args[0], err, string(output))
		}
		return nil
	}

	// Check if this directory already has a git repo from a previous sync.
	// Use "set" on first call (replaces default patterns), "add" on subsequent
	// calls so previously synced module directories stay in the working tree.
	gitPath := filepath.ToSlash(path)
	_, gitErr := os.Stat(filepath.Join(destDir, ".git"))
	if os.IsNotExist(gitErr) {
		// First time: initialize git repo + sparse-checkout
		runGit("init")
		runGit("remote", "add", "origin", repoURL)
		if err := runGit("sparse-checkout", "init", "--no-cone"); err != nil {
			return err
		}
		if err := runGit("sparse-checkout", "set", gitPath+"/**"); err != nil {
			return err
		}
	} else {
		// Already initialized: accumulate sparse patterns
		if err := runGit("sparse-checkout", "add", gitPath+"/**"); err != nil {
			return err
		}
	}

	// Fetch the specified ref
	if err := runGit("fetch", "--depth=1", "--filter=blob:none", "origin", ref); err != nil {
		return err
	}

	// Checkout the fetched content
	if err := runGit("checkout", "FETCH_HEAD"); err != nil {
		return err
	}

	return nil
}

// syncDirShallowClone uses git init + fetch + checkout to download the repository.
// Used for root directory sync or as fallback when sparse-checkout fails.
// Works with empty, non-empty, or existing git directories.
func (g *githubClient) syncDirShallowClone(ctx context.Context, owner, repo, ref, destDir string) error {
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	runGit := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = destDir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w\n%s", args[0], err, string(output))
		}
		return nil
	}

	// Initialize git repo (ignore error if already initialized)
	runGit("init")

	// Add remote (ignore error if already exists)
	runGit("remote", "add", "origin", repoURL)

	// Fetch the specified ref
	if err := runGit("fetch", "--depth=1", "origin", ref); err != nil {
		return err
	}

	// Checkout the fetched content
	return runGit("checkout", "FETCH_HEAD")
}

// fileInfo implements fs.FileInfo.
type fileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (f *fileInfo) Name() string       { return f.name }
func (f *fileInfo) Size() int64        { return f.size }
func (f *fileInfo) Mode() fs.FileMode  { return f.mode }
func (f *fileInfo) ModTime() time.Time { return f.modTime }
func (f *fileInfo) IsDir() bool        { return f.isDir }
func (f *fileInfo) Sys() any           { return nil }
