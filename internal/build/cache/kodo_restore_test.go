package cache

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

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
