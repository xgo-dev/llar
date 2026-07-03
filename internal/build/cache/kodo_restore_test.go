package cache

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/mod/module"
)

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
