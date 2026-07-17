package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnpack(t *testing.T) {
	src := setupSourceDir(t)
	metainfo := []byte(`{"metadata":"-lfoo"}`)

	for _, ext := range []string{".tar.gz", ".zip"} {
		t.Run(ext, func(t *testing.T) {
			artifact := filepath.Join(t.TempDir(), "artifact"+ext)
			if err := Pack(src, artifact, metainfo); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(t.TempDir(), "install")
			got, err := Unpack(artifact, dst)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(metainfo) {
				t.Fatalf("metainfo = %q, want %q", got, metainfo)
			}
			assertFileContent(t, filepath.Join(dst, "lib", "libfoo.a"), "archive")
			if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(metadataPath))); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("metadata file should not be unpacked: %v", err)
			}
		})
	}
}

func TestUnpackRejectsInvalidArtifact(t *testing.T) {
	t.Run("unsupported format", func(t *testing.T) {
		if _, err := Unpack("artifact.rar", t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := Unpack(filepath.Join(t.TempDir(), "missing.tar.gz"), t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Unpack error = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("missing metadata", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.zip")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		entry, err := writer.Create("lib.a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("archive")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(name, t.TempDir()); err == nil || !strings.Contains(err.Error(), "metadata is missing") {
			t.Fatalf("Unpack error = %v, want missing metadata", err)
		}
	})

	t.Run("unsafe tar path", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.tar.gz")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(file)
		writer := tar.NewWriter(gz)
		if err := writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(name, t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("unsupported zip mode", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "artifact.zip")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		header := &zip.FileHeader{Name: "link"}
		header.SetMode(os.ModeSymlink | 0o777)
		if _, err := writer.CreateHeader(header); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(name, t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})
}
