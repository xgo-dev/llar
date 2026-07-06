package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/mod/module"
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
	store := artifact.NewKodoArtifact(artifact.KodoArtifactConfig{
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		Prefix:    prefix,
	})

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
	}
	got, err := c.Put(ctx, key, os.DirFS(installDir), want)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if got.Metadata != want.Metadata {
		t.Fatalf("Put entry = %+v, want %+v", got, want)
	}

	stored, err := store.Get(ctx, artifact.Key{
		Module:    key.Module.Path,
		Version:   key.Module.Version,
		MatrixStr: key.Matrix,
	})
	if err != nil {
		t.Fatalf("artifact Get after Put failed: %v", err)
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
	if err := c.restore(ctx, key, objectName, stored.Type, strings.Repeat("f", 64)); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("restore with wrong checksum error = %v, want checksum error", err)
	}

	if err := os.WriteFile(filepath.Join(installDir, "lib", "libz.a"), []byte("conflicting zlib archive\n"), 0o644); err != nil {
		t.Fatalf("rewrite zlib archive before conflicting Put: %v", err)
	}
	conflict := Entry{
		Metadata: metadata,
	}
	got, err = c.Put(ctx, key, os.DirFS(installDir), conflict)
	if err != nil {
		t.Fatalf("conflicting Put failed: %v", err)
	}
	if got.Metadata != want.Metadata {
		t.Fatalf("conflicting Put entry = %+v, want existing %+v", got, want)
	}
	afterConflict, err := store.Get(ctx, artifact.Key{
		Module:    key.Module.Path,
		Version:   key.Module.Version,
		MatrixStr: key.Matrix,
	})
	if err != nil {
		t.Fatalf("artifact Get after conflicting Put failed: %v", err)
	}
	if afterConflict != stored {
		t.Fatalf("artifact after conflicting Put = %+v, want existing %+v", afterConflict, stored)
	}
	if err := assertPublicURLChecksum(ctx, stored.Source.URL, stored.Checksum); err != nil {
		t.Fatal(err)
	}

	putErrKey := Key{
		Module: key.Module,
		Matrix: matrix + "-put-error",
	}
	putErrObjectName := c.objectName(putErrKey)
	defer func() {
		if err := c.objects.Bucket(c.bucket).Object(putErrObjectName).Delete().Call(ctx); err != nil && !isKodoObjectNotFound(err) {
			t.Errorf("delete %s: %v", putErrObjectName, err)
		}
	}()
	putErr := errors.New("artifact put failed")
	realStore := c.artifacts
	c.artifacts = &recordingArtifactStore{putErr: putErr}
	if _, err := c.Put(ctx, putErrKey, os.DirFS(installDir), want); !errors.Is(err, putErr) {
		t.Fatalf("Put with artifact put error = %v, want %v", err, putErr)
	}
	c.artifacts = realStore

	if err := os.RemoveAll(installDir); err != nil {
		t.Fatalf("remove install dir before Get: %v", err)
	}
	got, ok, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after Put failed: %v", err)
	}
	if !ok {
		t.Fatal("Get after Put missed")
	}
	if got.Metadata != want.Metadata {
		t.Fatalf("Get entry = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(installDir, "include", "zlib.h")); err != nil {
		t.Fatalf("restored zlib include not found in %s: %v", installDir, err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "lib")); err != nil {
		t.Fatalf("restored zlib lib dir not found in %s: %v", installDir, err)
	}

	workspaceFile := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(workspaceFile, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWorkspaceDir := c.workspaceDir
	c.workspaceDir = workspaceFile
	if err := c.restore(ctx, key, objectName, stored.Type, ""); err == nil {
		t.Fatal("restore should fail when workspace dir is a file")
	}
	c.workspaceDir = oldWorkspaceDir

	if err := c.restore(ctx, key, objectName, "rar", ""); err == nil || !strings.Contains(err.Error(), "unsupported artifact type") {
		t.Fatalf("restore with unsupported type error = %v, want unsupported artifact type", err)
	}
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
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			hash := sha256.New()
			_, copyErr := io.Copy(hash, resp.Body)
			closeErr := resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("GET %s: %s", rawURL, resp.Status)
			} else if copyErr != nil {
				lastErr = copyErr
			} else if closeErr != nil {
				lastErr = closeErr
			} else if got := hex.EncodeToString(hash.Sum(nil)); got != checksum {
				lastErr = fmt.Errorf("GET %s checksum = %s, want %s", rawURL, got, checksum)
			} else {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return lastErr
}
