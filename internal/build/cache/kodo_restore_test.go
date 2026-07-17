package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	t.Run("workspace required", func(t *testing.T) {
		c := NewKodo(KodoConfig{
			Artifacts: &recordingArtifactStore{art: artifact.Artifact{Type: "tar.gz"}},
		}).(*kodoCache)
		if _, _, err := c.Get(context.Background(), key); err == nil {
			t.Fatal("Get should require a workspace for an artifact hit")
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
		if _, err := c.restore(context.Background(), key, c.objectName(key), "tar.gz", ""); err == nil {
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
		if _, err := c.restore(ctx, key, c.objectName(key), "tar.gz", ""); err == nil {
			t.Fatal("restore should fail with canceled context")
		}
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
