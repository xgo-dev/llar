// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package repo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/goplus/llar/internal/vcs"
)

// mockRepo is a mock implementation of vcs.Repo for testing
type mockRepo struct {
	syncFn func(ctx context.Context, ref, path, localDir string) error
}

func (m *mockRepo) Tags(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (m *mockRepo) Latest(ctx context.Context) (string, error) {
	return "", nil
}

func (m *mockRepo) At(ref, localDir string) fs.FS {
	return nil
}

func (m *mockRepo) Sync(ctx context.Context, ref, path, localDir string) error {
	if m.syncFn != nil {
		return m.syncFn(ctx, ref, path, localDir)
	}
	return nil
}

func TestNew(t *testing.T) {
	tmpDir := t.TempDir()
	repo := &mockRepo{}

	store := New(tmpDir, repo)
	if store == nil {
		t.Fatal("New returned nil")
	}
	rs := store.(*remoteStore)
	if rs.dir != tmpDir {
		t.Errorf("dir = %q, want %q", rs.dir, tmpDir)
	}
}

func TestDefaultDir(t *testing.T) {
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir() failed: %v", err)
	}
	if dir == "" {
		t.Error("DefaultDir() returned empty string")
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Errorf("DefaultDir() returned non-existent path: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("DefaultDir() returned non-directory path: %s", dir)
	}
}

func TestStore_ModuleFS(t *testing.T) {
	tmpDir := t.TempDir()

	// Create module directory with a test file
	modDir := filepath.Join(tmpDir, "DaveGamble", "cJSON")
	if err := os.MkdirAll(modDir, 0755); err != nil {
		t.Fatalf("failed to create module dir: %v", err)
	}
	testFile := filepath.Join(modDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	syncCalled := false
	repo := &mockRepo{
		syncFn: func(ctx context.Context, ref, path, localDir string) error {
			syncCalled = true
			if ref != "" {
				t.Errorf("syncFn ref = %q, want empty string", ref)
			}
			if path != "DaveGamble/cJSON" {
				t.Errorf("syncFn path = %q, want %q", path, "DaveGamble/cJSON")
			}
			if localDir != tmpDir {
				t.Errorf("syncFn localDir = %q, want %q", localDir, tmpDir)
			}
			return nil
		},
	}

	store := New(tmpDir, repo)
	fsys, err := store.ModuleFS(context.Background(), "DaveGamble/cJSON")
	if err != nil {
		t.Fatalf("ModuleFS() failed: %v", err)
	}

	if !syncCalled {
		t.Error("syncFn was not called")
	}

	// Verify fs.FS works
	f, err := fsys.Open("test.txt")
	if err != nil {
		t.Fatalf("failed to open file from fs.FS: %v", err)
	}
	f.Close()
}

func TestStore_ModuleFS_SyncError(t *testing.T) {
	tmpDir := t.TempDir()
	expectedErr := errors.New("sync failed")

	repo := &mockRepo{
		syncFn: func(ctx context.Context, ref, path, localDir string) error {
			return expectedErr
		},
	}

	store := New(tmpDir, repo)
	_, err := store.ModuleFS(context.Background(), "test/module")
	if err != expectedErr {
		t.Errorf("ModuleFS() error = %v, want %v", err, expectedErr)
	}
}

func TestStore_ModuleFS_InvalidModulePath(t *testing.T) {
	tests := []string{"", "../../../etc", "owner//repo"}

	for _, modPath := range tests {
		t.Run(modPath, func(t *testing.T) {
			tmpDir := t.TempDir()
			syncCalled := false
			repo := &mockRepo{
				syncFn: func(ctx context.Context, ref, path, localDir string) error {
					syncCalled = true
					return nil
				},
			}

			store := New(tmpDir, repo)
			_, err := store.ModuleFS(context.Background(), modPath)
			if err == nil {
				t.Fatalf("ModuleFS() expected error for invalid module path %q", modPath)
			}
			if syncCalled {
				t.Errorf("syncFn should not be called for invalid module path %q", modPath)
			}
		})
	}
}

func TestStore_ModuleFS_SerializesSync(t *testing.T) {
	tmpDir := t.TempDir()

	var mu sync.Mutex
	inSync := 0
	maxInSync := 0
	syncCalls := 0
	repo := &mockRepo{
		syncFn: func(ctx context.Context, ref, path, localDir string) error {
			mu.Lock()
			inSync++
			syncCalls++
			maxInSync = max(maxInSync, inSync)
			mu.Unlock()

			time.Sleep(50 * time.Millisecond)

			mu.Lock()
			inSync--
			mu.Unlock()
			return nil
		},
	}

	store := New(tmpDir, repo)

	const calls = 8
	ready := make(chan struct{}, calls)
	start := make(chan struct{})
	errs := make(chan error, calls)
	for i := range calls {
		go func(i int) {
			ready <- struct{}{}
			<-start
			_, err := store.ModuleFS(context.Background(), fmt.Sprintf("owner/mod%d", i))
			errs <- err
		}(i)
	}
	for range calls {
		<-ready
	}
	close(start)

	for range calls {
		if err := <-errs; err != nil {
			t.Fatalf("ModuleFS: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if syncCalls != calls {
		t.Fatalf("sync calls = %d, want %d", syncCalls, calls)
	}
	if maxInSync != 1 {
		t.Fatalf("max concurrent sync calls = %d, want 1", maxInSync)
	}
}

func TestStore_moduleDirOf(t *testing.T) {
	tmpDir := t.TempDir()
	store := New(tmpDir, &mockRepo{}).(*remoteStore)

	tests := []struct {
		modPath string
		wantDir string
	}{
		{"DaveGamble/cJSON", filepath.Join(tmpDir, "DaveGamble", "cJSON")},
		{"madler/zlib", filepath.Join(tmpDir, "madler", "zlib")},
	}

	for _, tt := range tests {
		t.Run(tt.modPath, func(t *testing.T) {
			got, err := store.moduleDirOf(tt.modPath)
			if err != nil {
				t.Fatalf("moduleDirOf() failed: %v", err)
			}
			if got != tt.wantDir {
				t.Errorf("moduleDirOf() = %q, want %q", got, tt.wantDir)
			}

			// Verify directory was created
			info, err := os.Stat(got)
			if err != nil {
				t.Errorf("moduleDirOf() directory not created: %v", err)
			}
			if !info.IsDir() {
				t.Errorf("moduleDirOf() path is not a directory")
			}
		})
	}
}

func TestStore_moduleDirOf_MkdirAllError(t *testing.T) {
	tmpDir := t.TempDir()
	store := New(tmpDir, &mockRepo{}).(*remoteStore)

	parent := filepath.Join(tmpDir, "owner")
	if err := os.WriteFile(parent, []byte("not-a-dir"), 0600); err != nil {
		t.Fatalf("failed to create parent file: %v", err)
	}

	_, err := store.moduleDirOf("owner/repo")
	if err == nil {
		t.Fatal("moduleDirOf() expected error when parent path is a file")
	}
}

func TestDefaultDir_UserCacheDirError(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", "")
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "aix":
		t.Setenv("XDG_CACHE_HOME", "relative/path")
	case "windows":
		t.Setenv("LocalAppData", "")
	default:
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}

	_, err := DefaultDir()
	if err == nil {
		t.Fatal("DefaultDir() expected error from os.UserCacheDir")
	}
}

func TestDefaultDir_MkdirAllError(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "Library"), []byte("not-a-dir"), 0600); err != nil {
			t.Fatalf("failed to create blocking file: %v", err)
		}
		t.Setenv("HOME", home)
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "aix":
		cacheFile := filepath.Join(t.TempDir(), "cache-file")
		if err := os.WriteFile(cacheFile, []byte("not-a-dir"), 0600); err != nil {
			t.Fatalf("failed to create cache file: %v", err)
		}
		t.Setenv("XDG_CACHE_HOME", cacheFile)
	case "windows":
		cacheFile := filepath.Join(t.TempDir(), "cache-file")
		if err := os.WriteFile(cacheFile, []byte("not-a-dir"), 0600); err != nil {
			t.Fatalf("failed to create cache file: %v", err)
		}
		t.Setenv("LocalAppData", cacheFile)
	default:
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}

	_, err := DefaultDir()
	if err == nil {
		t.Fatal("DefaultDir() expected MkdirAll error")
	}
}

func TestStore_LockModule(t *testing.T) {
	tmpDir := t.TempDir()
	store := New(tmpDir, &mockRepo{})

	unlock, err := store.LockModule("DaveGamble/cJSON")
	if err != nil {
		t.Fatalf("LockModule() failed: %v", err)
	}
	defer unlock()

	// Verify lock file was created
	lockFile := filepath.Join(tmpDir, "DaveGamble", "cJSON", ".lock")
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("lock file not created: %v", err)
	}
}

func TestStore_LockModule_Exclusive(t *testing.T) {
	tmpDir := t.TempDir()
	store := New(tmpDir, &mockRepo{})

	unlock, err := store.LockModule("madler/zlib")
	if err != nil {
		t.Fatalf("LockModule() failed: %v", err)
	}

	// Try to acquire the same lock from another goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		unlock2, err := store.LockModule("madler/zlib")
		if err != nil {
			t.Errorf("second LockModule() failed: %v", err)
			return
		}
		unlock2()
	}()

	// The goroutine should be blocked; give it a moment then release
	select {
	case <-done:
		t.Error("second lock acquired before first was released")
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}

	unlock()
	// Now the goroutine should complete
	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Error("second lock not acquired after first was released")
	}
}

func TestStore_LockModule_InvalidPath(t *testing.T) {
	tmpDir := t.TempDir()
	store := New(tmpDir, &mockRepo{})

	_, err := store.LockModule("")
	if err == nil {
		t.Error("LockModule() expected error for empty path")
	}
}

func TestStore_ModuleFS_RealRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real repo test in short mode")
	}

	tmpDir := t.TempDir()

	// Use real vcs.Repo with llarmvp-formula repository
	repo, err := vcs.NewRepo("github.com/MeteorsLiu/llarmvp-formula")
	if err != nil {
		t.Fatalf("failed to create vcs.Repo: %v", err)
	}

	store := New(tmpDir, repo)

	// Test syncing madler/zlib module (exists in llarmvp-formula)
	ctx := context.Background()
	fsys, err := store.ModuleFS(ctx, "madler/zlib")
	if err != nil {
		t.Fatalf("ModuleFS() failed: %v", err)
	}

	// Verify formula file exists
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("failed to read module directory: %v", err)
	}

	if len(entries) == 0 {
		t.Error("module directory is empty after sync")
	}

	// Look for formula files
	hasFormulaFile := false
	for _, entry := range entries {
		t.Logf("found entry: %s", entry.Name())
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".gox" {
			hasFormulaFile = true
		}
	}

	if !hasFormulaFile {
		// Check subdirectories for formula files
		for _, entry := range entries {
			if entry.IsDir() {
				subEntries, err := fs.ReadDir(fsys, entry.Name())
				if err != nil {
					continue
				}
				for _, subEntry := range subEntries {
					t.Logf("found subentry: %s/%s", entry.Name(), subEntry.Name())
					if filepath.Ext(subEntry.Name()) == ".gox" {
						hasFormulaFile = true
						break
					}
				}
			}
		}
	}

	if !hasFormulaFile {
		t.Error("no formula files (.gox) found in synced module")
	}
}
