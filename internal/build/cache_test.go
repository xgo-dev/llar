package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheKey(t *testing.T) {
	tests := []struct {
		version, matrix, want string
	}{
		{"1.0.0", "amd64-linux", "1.0.0-amd64-linux"},
		{"2.0.0", "arm64-darwin", "2.0.0-arm64-darwin"},
		{"1.0.0", "amd64-linux|openssl", "1.0.0-amd64-linux|openssl"},
	}
	for _, tt := range tests {
		if got := cacheKey(tt.version, tt.matrix); got != tt.want {
			t.Errorf("cacheKey(%q, %q) = %q, want %q", tt.version, tt.matrix, got, tt.want)
		}
	}
}

func TestBuildCache_GetSet(t *testing.T) {
	c := &buildCache{}

	// get from empty cache
	if _, ok := c.get("1.0.0", "amd64-linux"); ok {
		t.Fatal("get from empty cache should return false")
	}

	// set and get
	entry := &buildEntry{
		BuildTime: time.Now(),
	}
	c.set("1.0.0", "amd64-linux", entry)

	got, ok := c.get("1.0.0", "amd64-linux")
	if !ok {
		t.Fatal("get after set should return true")
	}
	if got != entry {
		t.Error("get returned different entry")
	}

	// different matrix miss
	if _, ok := c.get("1.0.0", "arm64-linux"); ok {
		t.Error("different matrix should miss")
	}

	// different version miss
	if _, ok := c.get("2.0.0", "amd64-linux"); ok {
		t.Error("different version should miss")
	}
}

func TestBuildCache_MultipleEntries(t *testing.T) {
	c := &buildCache{}

	e1 := &buildEntry{BuildTime: time.Now()}
	e2 := &buildEntry{BuildTime: time.Now().Add(time.Hour)}
	e3 := &buildEntry{BuildTime: time.Now().Add(2 * time.Hour)}

	c.set("1.0.0", "amd64-linux", e1)
	c.set("1.0.0", "arm64-linux", e2)
	c.set("2.0.0", "amd64-linux", e3)

	if got, _ := c.get("1.0.0", "amd64-linux"); got != e1 {
		t.Error("wrong entry for 1.0.0-amd64-linux")
	}
	if got, _ := c.get("1.0.0", "arm64-linux"); got != e2 {
		t.Error("wrong entry for 1.0.0-arm64-linux")
	}
	if got, _ := c.get("2.0.0", "amd64-linux"); got != e3 {
		t.Error("wrong entry for 2.0.0-amd64-linux")
	}
}

func TestBuildCache_Overwrite(t *testing.T) {
	c := &buildCache{}

	old := &buildEntry{BuildTime: time.Now()}
	c.set("1.0.0", "amd64-linux", old)

	updated := &buildEntry{BuildTime: time.Now().Add(time.Hour)}
	c.set("1.0.0", "amd64-linux", updated)

	got, _ := c.get("1.0.0", "amd64-linux")
	if got != updated {
		t.Error("overwrite did not replace entry")
	}
	if len(c.Cache) != 1 {
		t.Errorf("cache size = %d, want 1", len(c.Cache))
	}
}

func TestBuilder_InstallDir(t *testing.T) {
	b := &Builder{workspaceDir: "/tmp/ws"}

	dir, err := b.installDir("madler/zlib", "1.0.0", "amd64-linux")
	if err != nil {
		t.Fatalf("installDir() failed: %v", err)
	}
	want := filepath.Join("/tmp/ws", "madler/zlib@1.0.0-amd64-linux")
	if dir != want {
		t.Errorf("installDir() = %q, want %q", dir, want)
	}
}

func TestBuilder_CacheDir(t *testing.T) {
	b := &Builder{workspaceDir: "/tmp/ws"}

	dir, err := b.cacheDir("madler/zlib")
	if err != nil {
		t.Fatalf("cacheDir() failed: %v", err)
	}
	want := filepath.Join("/tmp/ws", "madler/zlib")
	if dir != want {
		t.Errorf("cacheDir() = %q, want %q", dir, want)
	}
}

func TestBuilder_SaveLoadCache(t *testing.T) {
	tmpDir := t.TempDir()
	b := &Builder{workspaceDir: tmpDir}
	now := time.Now().Truncate(time.Second)

	original := &buildCache{}
	original.set("1.0.0", "amd64-linux", &buildEntry{
		Metadata:  "-lssl",
		BuildTime: now,
	})
	original.set("2.0.0", "arm64-darwin", &buildEntry{
		BuildTime: now.Add(time.Hour),
	})

	if err := b.saveCache("madler/zlib", original); err != nil {
		t.Fatalf("saveCache() failed: %v", err)
	}

	t.Run("hit", func(t *testing.T) {
		cache, err := b.loadCache("madler/zlib")
		if err != nil {
			t.Fatalf("loadCache() failed: %v", err)
		}
		entry, ok := cache.get("1.0.0", "amd64-linux")
		if !ok {
			t.Fatal("expected entry, got miss")
		}
		if entry.Metadata != "-lssl" {
			t.Errorf("metadata = %q, want %q", entry.Metadata, "-lssl")
		}
		if !entry.BuildTime.Equal(now) {
			t.Errorf("build time = %v, want %v", entry.BuildTime, now)
		}
	})

	t.Run("miss", func(t *testing.T) {
		cache, err := b.loadCache("madler/zlib")
		if err != nil {
			t.Fatalf("loadCache() failed: %v", err)
		}
		if _, ok := cache.get("9.9.9", "amd64-linux"); ok {
			t.Error("expected miss for nonexistent key")
		}
	})

	t.Run("no cache file", func(t *testing.T) {
		if _, err := b.loadCache("nonexistent/mod"); err == nil {
			t.Fatal("expected error for missing cache file")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		badDir, _ := b.cacheDir("bad/json")
		os.MkdirAll(badDir, 0o700)
		os.WriteFile(filepath.Join(badDir, cacheFile), []byte("bad"), 0o644)
		if _, err := b.loadCache("bad/json"); err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

// ---------------------------------------------------------------------------
// Cache error path tests
// ---------------------------------------------------------------------------

func TestBuilder_CacheDir_InvalidPath(t *testing.T) {
	b := &Builder{workspaceDir: "/tmp/ws"}

	// Empty path should fail EscapePath (filepath.Localize)
	_, err := b.cacheDir("")
	if err == nil {
		t.Fatal("cacheDir('') should fail")
	}

	// Absolute path should fail
	_, err = b.cacheDir("/abs/path")
	if err == nil {
		t.Fatal("cacheDir('/abs/path') should fail")
	}

	// Parent traversal should fail
	_, err = b.cacheDir("..")
	if err == nil {
		t.Fatal("cacheDir('..') should fail")
	}
}

func TestBuilder_InstallDir_InvalidPath(t *testing.T) {
	b := &Builder{workspaceDir: "/tmp/ws"}

	_, err := b.installDir("", "1.0.0", "amd64-linux")
	if err == nil {
		t.Fatal("installDir('', ...) should fail")
	}

	_, err = b.installDir("/abs/path", "1.0.0", "amd64-linux")
	if err == nil {
		t.Fatal("installDir('/abs/path', ...) should fail")
	}
}

func TestBuilder_LoadCache_InvalidPath(t *testing.T) {
	tmpDir := t.TempDir()
	b := &Builder{workspaceDir: tmpDir}

	_, err := b.loadCache("")
	if err == nil {
		t.Fatal("loadCache('') should fail")
	}
}

func TestBuilder_SaveCache_InvalidPath(t *testing.T) {
	tmpDir := t.TempDir()
	b := &Builder{workspaceDir: tmpDir}

	cache := &buildCache{}
	cache.set("1.0.0", "amd64-linux", &buildEntry{BuildTime: time.Now()})

	err := b.saveCache("", cache)
	if err == nil {
		t.Fatal("saveCache('', ...) should fail")
	}
}

func TestBuilder_InstallDir_DifferentMatrices(t *testing.T) {
	b1 := &Builder{workspaceDir: "/tmp/ws"}
	b2 := &Builder{workspaceDir: "/tmp/ws"}

	dir1, _ := b1.installDir("test/lib", "1.0.0", "amd64-linux")
	dir2, _ := b2.installDir("test/lib", "1.0.0", "arm64-darwin")

	if dir1 == dir2 {
		t.Errorf("same installDir for different matrices: %q", dir1)
	}
	if !strings.Contains(dir1, "amd64-linux") {
		t.Errorf("dir1 %q should contain matrix string", dir1)
	}
	if !strings.Contains(dir2, "arm64-darwin") {
		t.Errorf("dir2 %q should contain matrix string", dir2)
	}
}

func TestBuilder_InstallDir_DifferentVersions(t *testing.T) {
	b := &Builder{workspaceDir: "/tmp/ws"}

	dir1, _ := b.installDir("test/lib", "1.0.0", "amd64-linux")
	dir2, _ := b.installDir("test/lib", "2.0.0", "amd64-linux")

	if dir1 == dir2 {
		t.Errorf("same installDir for different versions: %q", dir1)
	}
}

func TestBuilder_SaveCache_CreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	b := &Builder{workspaceDir: tmpDir}

	cache := &buildCache{}
	cache.set("1.0.0", "amd64-linux", &buildEntry{
		Metadata:  "-ltest",
		BuildTime: time.Now(),
	})

	// The cacheDir doesn't exist yet; saveCache should create it
	if err := b.saveCache("new/module", cache); err != nil {
		t.Fatalf("saveCache() failed: %v", err)
	}

	// Verify directory was created
	cacheDir, _ := b.cacheDir("new/module")
	if _, err := os.Stat(cacheDir); err != nil {
		t.Errorf("cache directory not created: %v", err)
	}

	// Verify file was written
	cachePath := filepath.Join(cacheDir, cacheFile)
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("cache file not written: %v", err)
	}
}

func TestBuildCache_GetFromNilMap(t *testing.T) {
	c := &buildCache{Cache: nil}
	_, ok := c.get("1.0.0", "amd64-linux")
	if ok {
		t.Error("get from nil cache map should return false")
	}
}

func TestBuildCache_SetInitializesMap(t *testing.T) {
	c := &buildCache{}
	if c.Cache != nil {
		t.Fatal("Cache should start as nil")
	}
	c.set("1.0.0", "amd64-linux", &buildEntry{})
	if c.Cache == nil {
		t.Fatal("set should initialize the Cache map")
	}
	if len(c.Cache) != 1 {
		t.Errorf("cache size = %d, want 1", len(c.Cache))
	}
}
