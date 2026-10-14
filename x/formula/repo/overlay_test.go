package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOverlayStore_ModuleFS_Local(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a local module directory with a test file
	localDir := filepath.Join(tmpDir, "local", "testmod")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "test.txt"), []byte("local"), 0644); err != nil {
		t.Fatal(err)
	}

	syncCalled := false
	remote := New(tmpDir, &mockRepo{
		syncFn: func(ctx context.Context, ref, path, localDir string) error {
			syncCalled = true
			return nil
		},
	})

	store := NewOverlayStore(remote, map[string]string{
		"test/mod": localDir,
	})

	fsys, err := store.ModuleFS(context.Background(), "test/mod")
	if err != nil {
		t.Fatalf("ModuleFS() failed: %v", err)
	}
	if syncCalled {
		t.Error("remote sync should not be called for local module")
	}

	// Verify we get the local file
	f, err := fsys.Open("test.txt")
	if err != nil {
		t.Fatalf("failed to open file from local FS: %v", err)
	}
	f.Close()
}

func TestOverlayStore_ModuleFS_Remote(t *testing.T) {
	tmpDir := t.TempDir()

	// Create remote module dir
	modDir := filepath.Join(tmpDir, "remote", "mod")
	if err := os.MkdirAll(modDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "remote.txt"), []byte("remote"), 0644); err != nil {
		t.Fatal(err)
	}

	syncCalled := false
	remote := New(tmpDir, &mockRepo{
		syncFn: func(ctx context.Context, ref, path, localDir string) error {
			syncCalled = true
			return nil
		},
	})

	// Only "local/mod" is in locals, not "remote/mod"
	store := NewOverlayStore(remote, map[string]string{
		"local/mod": "/some/path",
	})

	_, err := store.ModuleFS(context.Background(), "remote/mod")
	if err != nil {
		t.Fatalf("ModuleFS() failed: %v", err)
	}
	if !syncCalled {
		t.Error("remote sync should be called for non-local module")
	}
}

func TestOverlayStore_LockModule(t *testing.T) {
	tmpDir := t.TempDir()
	remote := New(tmpDir, &mockRepo{})

	localDir := filepath.Join(tmpDir, "local")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}

	store := NewOverlayStore(remote, map[string]string{
		"test/mod": localDir,
	})

	// LockModule for local module should still use shared module-path lock.
	unlock, err := store.LockModule("test/mod")
	if err != nil {
		t.Fatalf("LockModule() failed: %v", err)
	}
	defer unlock()

	// Lock file should be module-path scoped under shared store root.
	sharedLock := filepath.Join(tmpDir, "test", "mod", ".lock")
	if _, err := os.Stat(sharedLock); err != nil {
		t.Fatalf("shared lock file not created: %v", err)
	}
	// Local lock file should not be used.
	if _, err := os.Stat(filepath.Join(localDir, ".lock")); err == nil {
		t.Fatal("local .lock should not be used")
	}
}

func TestOverlayStore_LockModule_ExclusiveAcrossLocalAndRemote(t *testing.T) {
	tmpDir := t.TempDir()
	remote := New(tmpDir, &mockRepo{})

	localDir := filepath.Join(tmpDir, "local")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewOverlayStore(remote, map[string]string{
		"test/mod": localDir,
	})

	unlock, err := store.LockModule("test/mod")
	if err != nil {
		t.Fatalf("overlay LockModule() failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		unlock2, err := remote.LockModule("test/mod")
		if err != nil {
			t.Errorf("remote LockModule() failed: %v", err)
			return
		}
		unlock2()
	}()

	select {
	case <-done:
		t.Fatal("remote lock acquired before overlay lock released")
	case <-time.After(50 * time.Millisecond):
		// expected: blocked by the same module-path lock
	}

	unlock()

	select {
	case <-done:
		// expected
	case <-time.After(5 * time.Second):
		t.Fatal("remote lock not acquired after overlay lock released")
	}
}
