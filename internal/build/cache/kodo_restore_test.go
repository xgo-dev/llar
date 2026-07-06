package cache

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/mod/module"
	qiniuclient "github.com/qiniu/go-sdk/v7/client"
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

func TestKodoGetArtifactMissAndError(t *testing.T) {
	key := Key{
		Module: module.Version{Path: "madler/zlib", Version: "v1.3.2"},
		Matrix: "linux-amd64",
	}
	t.Run("miss", func(t *testing.T) {
		c := NewKodo(KodoConfig{
			Artifacts: &recordingArtifactStore{err: artifact.ErrNotFound},
		}).(*kodoCache)
		if _, ok, err := c.Get(context.Background(), key); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatal("Get hit, want miss")
		}
	})
	t.Run("store error", func(t *testing.T) {
		wantErr := errors.New("artifact store failed")
		c := NewKodo(KodoConfig{
			Artifacts: &recordingArtifactStore{err: wantErr},
		}).(*kodoCache)
		if _, _, err := c.Get(context.Background(), key); !errors.Is(err, wantErr) {
			t.Fatalf("Get error = %v, want %v", err, wantErr)
		}
	})
	t.Run("invalid install path", func(t *testing.T) {
		c := NewKodo(KodoConfig{
			WorkspaceDir: t.TempDir(),
			Artifacts: &recordingArtifactStore{art: artifact.Artifact{
				Type: "tar.gz",
			}},
		}).(*kodoCache)
		if _, _, err := c.Get(context.Background(), Key{Matrix: "linux-amd64"}); err == nil {
			t.Fatal("Get should fail for empty module path")
		}
	})
}

func TestKodoPutLocalErrors(t *testing.T) {
	key := Key{
		Module: module.Version{Path: "madler/zlib", Version: "v1.3.2"},
		Matrix: "linux-amd64",
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "libz.a"), []byte("archive\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("empty bucket", func(t *testing.T) {
		c := NewKodo(KodoConfig{
			PublicDomain: "https://cdn.example.com",
			Artifacts:    &recordingArtifactStore{},
		})
		if _, err := c.Put(context.Background(), key, os.DirFS(src), Entry{}); err == nil {
			t.Fatal("Put should reject empty bucket")
		}
	})

	t.Run("temp file", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		c := NewKodo(KodoConfig{
			Bucket:       "bucket",
			PublicDomain: "https://cdn.example.com",
			Artifacts:    &recordingArtifactStore{},
		})
		if _, err := c.Put(context.Background(), key, os.DirFS(src), Entry{}); err == nil {
			t.Fatal("Put should fail when temp dir is missing")
		}
	})

	t.Run("archive", func(t *testing.T) {
		src := t.TempDir()
		if err := os.Symlink("target", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		c := NewKodo(KodoConfig{
			Bucket:       "bucket",
			PublicDomain: "https://cdn.example.com",
			Artifacts:    &recordingArtifactStore{},
		})
		if _, err := c.Put(context.Background(), key, os.DirFS(src), Entry{}); err == nil {
			t.Fatal("Put should reject unsupported archive entry")
		}
	})

	t.Run("source url", func(t *testing.T) {
		c := NewKodo(KodoConfig{
			Bucket:       "bucket",
			PublicDomain: "ftp://cdn.example.com",
			Artifacts:    &recordingArtifactStore{},
		})
		if _, err := c.Put(context.Background(), key, os.DirFS(src), Entry{}); err == nil {
			t.Fatal("Put should reject non-http public domain")
		}
	})

	t.Run("upload", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := NewKodo(KodoConfig{
			AccessKey:    "ak",
			SecretKey:    "sk",
			Bucket:       "bucket",
			PublicDomain: "https://cdn.example.com",
			Artifacts:    &recordingArtifactStore{},
		})
		if _, err := c.Put(ctx, key, os.DirFS(src), Entry{}); err == nil {
			t.Fatal("Put should fail with canceled context")
		}
	})
}

func TestKodoRestoreLocalErrors(t *testing.T) {
	key := Key{
		Module: module.Version{Path: "madler/zlib", Version: "v1.3.2"},
		Matrix: "linux-amd64",
	}

	t.Run("temp file", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		c := NewKodo(KodoConfig{
			Bucket:       "bucket",
			WorkspaceDir: t.TempDir(),
		}).(*kodoCache)
		if err := c.restore(context.Background(), key, c.objectName(key), "tar.gz", ""); err == nil {
			t.Fatal("restore should fail when temp dir is missing")
		}
	})

	t.Run("download", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := NewKodo(KodoConfig{
			AccessKey:    "ak",
			SecretKey:    "sk",
			Bucket:       "bucket",
			WorkspaceDir: t.TempDir(),
		}).(*kodoCache)
		if err := c.restore(ctx, key, c.objectName(key), "tar.gz", ""); err == nil {
			t.Fatal("restore should fail with canceled context")
		}
	})
}

func TestKodoGetDoesNotParseSourceURL(t *testing.T) {
	c := NewKodo(KodoConfig{
		Artifacts: staticArtifactStore{
			art: artifact.Artifact{
				Source:   artifact.Source{Type: "kodo", URL: "not a kodo object name"},
				Type:     "zip",
				Metadata: "-lz",
			},
		},
	}).(*kodoCache)
	key := Key{
		Module: module.Version{Path: "madler/zlib", Version: "v1.3.2"},
		Matrix: "amd64-linux",
	}

	got, ok, err := c.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Get missed")
	}
	if got.Metadata != "-lz" {
		t.Fatalf("metadata = %q, want -lz", got.Metadata)
	}
}

func TestExtractArtifactUsesArtifactType(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "include", "zlib.h"), []byte("zlib header\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("tar.gz", func(t *testing.T) {
		artifactFile := filepath.Join(t.TempDir(), "artifact.tar.gz")
		file, err := os.Create(artifactFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeTarGzip(file, os.DirFS(src)); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

		dst := t.TempDir()
		if err := extractArtifact(artifactFile, "tar.gz", dst); err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, filepath.Join(dst, "include", "zlib.h"), "zlib header\n")
	})

	t.Run("zip", func(t *testing.T) {
		artifactFile := filepath.Join(t.TempDir(), "artifact.zip")
		file, err := os.Create(artifactFile)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(file)
		w, err := zw.Create("include/zlib.h")
		if err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("zlib header\n")); err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

		dst := t.TempDir()
		if err := extractArtifact(artifactFile, "zip", dst); err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, filepath.Join(dst, "include", "zlib.h"), "zlib header\n")
	})
}

func TestKodoHelpersRejectInvalidInputs(t *testing.T) {
	if _, err := kodoSourceURL("ftp://cdn.example.com", "object.tar.gz"); err == nil {
		t.Fatal("kodoSourceURL should reject ftp domain")
	}
	if _, err := kodoSourceURL("http://[::1", "object.tar.gz"); err == nil {
		t.Fatal("kodoSourceURL should reject invalid domain")
	}

	if !isKodoObjectNotFound(&qiniuclient.ErrorInfo{Code: 612}) {
		t.Fatal("612 should be object not found")
	}
	if isKodoObjectNotFound(&qiniuclient.ErrorInfo{Code: 614}) {
		t.Fatal("614 should not be object not found")
	}
	if !isKodoObjectExists(&qiniuclient.ErrorInfo{Code: 614}) {
		t.Fatal("614 should be object exists")
	}
	if isKodoObjectExists(errors.New("plain error")) {
		t.Fatal("plain error should not be object exists")
	}
}

func TestKodoFileSHA256(t *testing.T) {
	name := filepath.Join(t.TempDir(), "artifact.tar.gz")
	body := []byte("artifact bytes\n")
	if err := os.WriteFile(name, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	got, err := fileSHA256(name)
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("fileSHA256 = %s, want %s", got, hex.EncodeToString(sum[:]))
	}

	if _, err := fileSHA256(filepath.Join(t.TempDir(), "missing.tar.gz")); err == nil {
		t.Fatal("fileSHA256 should fail for missing file")
	}
}

func TestKodoArchiveRejectsUnsafeOrUnsupportedEntries(t *testing.T) {
	t.Run("write unsupported source mode", func(t *testing.T) {
		src := t.TempDir()
		if err := os.Symlink("target", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		if err := writeTarGzip(io.Discard, os.DirFS(src)); err == nil {
			t.Fatal("writeTarGzip should reject symlink")
		}
	})

	t.Run("write unreadable file", func(t *testing.T) {
		src := t.TempDir()
		name := filepath.Join(src, "libz.a")
		if err := os.WriteFile(name, []byte("archive\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(name, 0o644)
		if err := writeTarGzip(io.Discard, os.DirFS(src)); err == nil {
			t.Fatal("writeTarGzip should fail when source file cannot be opened")
		}
	})

	t.Run("write target error", func(t *testing.T) {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "libz.a"), []byte("archive\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeTarGzip(errorWriter{}, os.DirFS(src)); err == nil {
			t.Fatal("writeTarGzip should fail when target writer fails")
		}
	})

	t.Run("unsupported artifact type", func(t *testing.T) {
		if err := extractArtifact("unused", "rar", t.TempDir()); err == nil {
			t.Fatal("extractArtifact should reject unsupported type")
		}
	})

	t.Run("missing tar gzip artifact", func(t *testing.T) {
		if err := extractArtifact(filepath.Join(t.TempDir(), "missing.tar.gz"), "tar.gz", t.TempDir()); err == nil {
			t.Fatal("extractArtifact should fail for missing tar.gz")
		}
	})

	t.Run("invalid tar gzip", func(t *testing.T) {
		if err := extractTarGzip(bytes.NewReader([]byte("not gzip")), t.TempDir()); err == nil {
			t.Fatal("extractTarGzip should reject invalid gzip")
		}
	})

	t.Run("invalid tar stream", func(t *testing.T) {
		var buf bytes.Buffer
		gzw := gzip.NewWriter(&buf)
		if _, err := gzw.Write([]byte("not tar")); err != nil {
			t.Fatal(err)
		}
		if err := gzw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGzip(bytes.NewReader(buf.Bytes()), t.TempDir()); err == nil {
			t.Fatal("extractTarGzip should reject invalid tar")
		}
	})

	t.Run("unsafe tar path", func(t *testing.T) {
		var buf bytes.Buffer
		gzw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gzw)
		if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: int64(len("x"))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGzip(bytes.NewReader(buf.Bytes()), t.TempDir()); err == nil {
			t.Fatal("extractTarGzip should reject unsafe path")
		}
	})

	t.Run("unsupported tar type", func(t *testing.T) {
		var buf bytes.Buffer
		gzw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gzw)
		if err := tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGzip(bytes.NewReader(buf.Bytes()), t.TempDir()); err == nil {
			t.Fatal("extractTarGzip should reject symlink")
		}
	})

	t.Run("tar dir conflicts with file", func(t *testing.T) {
		dst := t.TempDir()
		if err := os.WriteFile(filepath.Join(dst, "include"), []byte("file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		gzw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gzw)
		if err := tw.WriteHeader(&tar.Header{Name: "include/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGzip(bytes.NewReader(buf.Bytes()), dst); err == nil {
			t.Fatal("extractTarGzip should reject directory over file")
		}
	})

	t.Run("tar file conflicts with directory", func(t *testing.T) {
		dst := t.TempDir()
		if err := os.Mkdir(filepath.Join(dst, "libz.a"), 0o755); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		gzw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gzw)
		if err := tw.WriteHeader(&tar.Header{Name: "libz.a", Mode: 0o644, Size: int64(len("x"))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGzip(bytes.NewReader(buf.Bytes()), dst); err == nil {
			t.Fatal("extractTarGzip should reject file over directory")
		}
	})

	t.Run("invalid zip", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.zip")
		if err := os.WriteFile(name, []byte("not zip"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := extractZip(name, t.TempDir()); err == nil {
			t.Fatal("extractZip should reject invalid zip")
		}
	})

	t.Run("unsafe zip path", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.zip")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(file)
		w, err := zw.Create("../escape")
		if err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractZip(name, t.TempDir()); err == nil {
			t.Fatal("extractZip should reject unsafe path")
		}
	})

	t.Run("zip dir conflicts with file", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.zip")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(file)
		header := &zip.FileHeader{Name: "include/"}
		header.SetMode(os.ModeDir | 0o755)
		if _, err := zw.CreateHeader(header); err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		if err := os.WriteFile(filepath.Join(dst, "include"), []byte("file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := extractZip(name, dst); err == nil {
			t.Fatal("extractZip should reject directory over file")
		}
	})

	t.Run("zip unsupported mode", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.zip")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(file)
		header := &zip.FileHeader{Name: "link"}
		header.SetMode(os.ModeSymlink | 0o777)
		if _, err := zw.CreateHeader(header); err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractZip(name, t.TempDir()); err == nil {
			t.Fatal("extractZip should reject symlink")
		}
	})

	t.Run("zip file conflicts with directory", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.zip")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(file)
		w, err := zw.Create("libz.a")
		if err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			_ = zw.Close()
			_ = file.Close()
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		if err := os.Mkdir(filepath.Join(dst, "libz.a"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := extractZip(name, dst); err == nil {
			t.Fatal("extractZip should reject file over directory")
		}
	})

	for _, name := range []string{"", "/abs", "..", "../escape"} {
		if got, err := cleanArchiveName(name); err == nil {
			t.Fatalf("cleanArchiveName(%q) = %q, want error", name, got)
		}
	}
}

type staticArtifactStore struct {
	art artifact.Artifact
}

func (s staticArtifactStore) Get(context.Context, artifact.Key) (artifact.Artifact, error) {
	return s.art, nil
}

func (s staticArtifactStore) Put(_ context.Context, _ artifact.Key, art artifact.Artifact) (artifact.Artifact, error) {
	return art, nil
}

func (s staticArtifactStore) Delete(context.Context, artifact.Key) error {
	return nil
}

type recordingArtifactStore struct {
	art    artifact.Artifact
	err    error
	putErr error
}

func (s *recordingArtifactStore) Get(context.Context, artifact.Key) (artifact.Artifact, error) {
	if s.err != nil {
		return artifact.Artifact{}, s.err
	}
	return s.art, nil
}

func (s *recordingArtifactStore) Put(_ context.Context, _ artifact.Key, art artifact.Artifact) (artifact.Artifact, error) {
	if s.putErr != nil {
		return artifact.Artifact{}, s.putErr
	}
	s.art = art
	return art, nil
}

func (s *recordingArtifactStore) Delete(context.Context, artifact.Key) error {
	return nil
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func assertFileContent(t *testing.T, name, want string) {
	t.Helper()

	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}
