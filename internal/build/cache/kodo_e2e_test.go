package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/mod/module"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestKodoObjectName(t *testing.T) {
	c := NewKodo(KodoConfig{Prefix: "/cache/"}).(*kodoCache)
	key := Key{
		Module: module.Version{Path: "madler/zlib", Version: "v1.3.2"},
		Matrix: "amd64-linux",
	}
	if got, want := c.objectName(key), "cache/madler/zlib/v1.3.2/amd64-linux.tar.gz"; got != want {
		t.Fatalf("object name = %q, want %q", got, want)
	}
	got, err := kodoSourceURL("llar.liuxi.ng", c.objectName(key))
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://llar.liuxi.ng/cache/madler/zlib/v1.3.2/amd64-linux.tar.gz"; got != want {
		t.Fatalf("source url = %q, want %q", got, want)
	}
	if _, err := parseKodoSourceURL("file:///cache/madler/zlib/v1.3.2/amd64-linux.tar.gz"); err == nil {
		t.Fatal("non-http source url should be rejected")
	}
}

func TestKodoE2E_PutGet(t *testing.T) {
	accessKey := os.Getenv("QINIU_ACCESS_KEY")
	secretKey := os.Getenv("QINIU_SECRET_KEY")
	bucket := os.Getenv("QINIU_BUCKET")
	publicDomain := envOrDefault("QINIU_PUBLIC_DOMAIN", "llar.liuxi.ng")
	if accessKey == "" || secretKey == "" || bucket == "" {
		t.Skip("QINIU_ACCESS_KEY, QINIU_SECRET_KEY, and QINIU_BUCKET are required")
	}

	prefix := strings.Trim(os.Getenv("QINIU_PREFIX"), "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += fmt.Sprintf("llar-kodo-e2e/%d", time.Now().UnixNano())
	store := newKodoE2EArtifactStore(t)

	const zlibVersion = "v1.3.1"
	matrix := hostMatrix()
	workspaceDir, installDir, metadata := buildZlibWithLLAR(t, zlibVersion, matrix)
	c := NewKodo(KodoConfig{
		AccessKey:    accessKey,
		SecretKey:    secretKey,
		Bucket:       bucket,
		PublicDomain: publicDomain,
		Prefix:       prefix,
		WorkspaceDir: workspaceDir,
		Artifacts:    store,
	}).(*kodoCache)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	key := Key{
		Module: module.Version{Path: "madler/zlib", Version: zlibVersion},
		Matrix: matrix,
	}
	objectName := c.objectName(key)
	if want := prefix + "/madler/zlib/" + zlibVersion + "/" + matrix + ".tar.gz"; objectName != want {
		t.Fatalf("object name = %q, want %q", objectName, want)
	}
	defer func() {
		if err := c.objects.Bucket(c.bucket).Object(objectName).Delete().Call(ctx); err != nil && !isKodoObjectNotFound(err) {
			t.Errorf("delete %s: %v", objectName, err)
		}
	}()

	if _, ok, err := c.Get(ctx, key); err != nil {
		t.Fatalf("Get before Put failed: %v", err)
	} else if ok {
		t.Fatalf("Get before Put hit %s", objectName)
	}

	want := Entry{
		Metadata: metadata,
		Deps:     []module.Version{{Path: "example/dep", Version: "v1.0.0"}},
	}
	got, err := c.Put(ctx, key, os.DirFS(installDir), want)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if got.Metadata != want.Metadata || !slices.Equal(got.Deps, want.Deps) {
		t.Fatalf("Put entry = %+v, want %+v", got, want)
	}

	stored, ok, err := store.Get(ctx, artifactKey(key))
	if err != nil {
		t.Fatalf("artifact Get after Put failed: %v", err)
	}
	if !ok {
		t.Fatal("artifact Get after Put missed")
	}
	if stored.Source.Type != "kodo" {
		t.Fatalf("artifact source type = %q, want kodo", stored.Source.Type)
	}
	wantURL, err := kodoSourceURL(publicDomain, objectName)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Source.URL != wantURL {
		t.Fatalf("artifact source url = %q, want %q", stored.Source.URL, wantURL)
	}
	if stored.Type != "tar.gz" || stored.Metadata != metadata || len(stored.Checksum) != 64 {
		t.Fatalf("artifact = %+v, want tar.gz metadata %q and sha256 checksum", stored, metadata)
	}
	if err := assertPublicURLChecksum(ctx, stored.Source.URL, stored.Checksum); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(installDir, "lib", "libz.a"), []byte("conflicting zlib archive\n"), 0o644); err != nil {
		t.Fatalf("rewrite zlib archive before conflicting Put: %v", err)
	}
	conflict := Entry{
		Metadata: "-lz-conflict",
		Deps:     []module.Version{{Path: "example/other", Version: "v2.0.0"}},
	}
	got, err = c.Put(ctx, key, os.DirFS(installDir), conflict)
	if err != nil {
		t.Fatalf("conflicting Put failed: %v", err)
	}
	if got.Metadata != want.Metadata || !slices.Equal(got.Deps, want.Deps) {
		t.Fatalf("conflicting Put entry = %+v, want existing %+v", got, want)
	}
	afterConflict, ok, err := store.Get(ctx, artifactKey(key))
	if err != nil {
		t.Fatalf("artifact Get after conflicting Put failed: %v", err)
	}
	if !ok {
		t.Fatal("artifact Get after conflicting Put missed")
	}
	if afterConflict != stored {
		t.Fatalf("artifact after conflicting Put = %+v, want existing %+v", afterConflict, stored)
	}
	if err := assertPublicURLChecksum(ctx, stored.Source.URL, stored.Checksum); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(installDir); err != nil {
		t.Fatalf("remove install dir before Get: %v", err)
	}
	got, ok, err = c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after Put failed: %v", err)
	}
	if !ok {
		t.Fatal("Get after Put missed")
	}
	if got.Metadata != want.Metadata || !slices.Equal(got.Deps, want.Deps) {
		t.Fatalf("Get entry = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(installDir, "include", "zlib.h")); err != nil {
		t.Fatalf("restored zlib include not found in %s: %v", installDir, err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "lib")); err != nil {
		t.Fatalf("restored zlib lib dir not found in %s: %v", installDir, err)
	}
}

func newKodoE2EArtifactStore(t *testing.T) artifact.Store {
	t.Helper()

	var dial gorm.Dialector
	if dsn := os.Getenv("POSTGRES_DSN"); dsn != "" {
		dial = postgres.Open(dsn)
	} else {
		dial = sqlite.Open(filepath.Join(t.TempDir(), "artifacts.db"))
	}
	db, err := gorm.Open(dial, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("db.Close: %v", err)
		}
	})
	if err := db.Exec("DROP TABLE IF EXISTS artifacts").Error; err != nil {
		t.Fatalf("drop artifacts: %v", err)
	}

	store, err := artifact.NewGormStore(db)
	if err != nil {
		t.Fatalf("NewGormStore: %v", err)
	}
	return store
}

func buildZlibWithLLAR(t *testing.T, version, matrix string) (string, string, string) {
	t.Helper()

	llar, err := exec.LookPath("llar")
	if err != nil {
		t.Skip("llar not found in PATH")
	}
	for _, tool := range []string{"cmake", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found in PATH", tool)
		}
	}

	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	cacheHome := filepath.Join(dir, "cache")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create home: %v", err)
	}
	if err := os.MkdirAll(cacheHome, 0o755); err != nil {
		t.Fatalf("create cache home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}

	formulaRoot := kodoE2EFormulaRoot(t)
	cmd := exec.Command(llar, "make", "./madler/zlib@"+version)
	cmd.Dir = formulaRoot
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("llar make ./madler/zlib@%s failed: %v\n%s", version, err, out)
	}

	metadata := strings.TrimSpace(string(out))
	if metadata != "-lz" {
		t.Fatalf("llar make metadata = %q, want -lz", metadata)
	}

	workspaceDir := filepath.Join(userCacheDir, ".llar", "workspaces")
	installDir := filepath.Join(workspaceDir, fmt.Sprintf("madler/zlib@%s-%s", version, matrix))
	if _, err := os.Stat(filepath.Join(installDir, "include", "zlib.h")); err != nil {
		t.Fatalf("zlib include not found in %s: %v", installDir, err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "lib")); err != nil {
		t.Fatalf("zlib lib dir not found in %s: %v", installDir, err)
	}
	return workspaceDir, installDir, metadata
}

func kodoE2EFormulaRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	dir := filepath.Join(root, "testdata", "kodo-e2e", "formulas")
	if _, err := os.Stat(filepath.Join(dir, "madler", "zlib", "versions.json")); err != nil {
		t.Fatalf("kodo e2e formula root: %v", err)
	}
	return dir
}

func hostMatrix() string {
	return runtime.GOARCH + "-" + runtime.GOOS
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func assertPublicURLChecksum(ctx context.Context, rawURL, checksum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, resp.Body); err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != checksum {
		return fmt.Errorf("GET %s checksum = %s, want %s", rawURL, got, checksum)
	}
	return nil
}
