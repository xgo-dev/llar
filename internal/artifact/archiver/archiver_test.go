package archiver

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPackDirectoryAddsMetadataAndPayload(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "out")
	metainfo := []byte(`{"metadata":"-lfoo"}`)

	if err := Pack(src, dst, metainfo); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "lib", "libfoo.a"), "archive")
	assertFileContent(t, filepath.Join(dst, ".llar", "metadata.json"), string(metainfo))
	if _, err := os.Stat(filepath.Join(src, ".llar", "metadata.json")); !os.IsNotExist(err) {
		t.Fatalf("Pack modified source metadata file: %v", err)
	}
}

func TestPackZipAddsMetadataAndPayload(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "out.zip")
	metainfo := []byte(`{"metadata":"-lfoo"}`)

	if err := Pack(src, dst, metainfo); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	files := readZip(t, dst)
	if string(files["lib/libfoo.a"]) != "archive" {
		t.Fatalf("zip lib/libfoo.a = %q, want %q", files["lib/libfoo.a"], "archive")
	}
	if string(files[".llar/metadata.json"]) != string(metainfo) {
		t.Fatalf("zip metadata = %q, want %q", files[".llar/metadata.json"], metainfo)
	}
}

func TestPackTarGzAddsMetadataAndPayload(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "out.tar.gz")
	metainfo := []byte(`{"metadata":"-lfoo"}`)

	if err := Pack(src, dst, metainfo); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	files := readTarGz(t, dst)
	if string(files["lib/libfoo.a"]) != "archive" {
		t.Fatalf("tar.gz lib/libfoo.a = %q, want %q", files["lib/libfoo.a"], "archive")
	}
	if string(files[".llar/metadata.json"]) != string(metainfo) {
		t.Fatalf("tar.gz metadata = %q, want %q", files[".llar/metadata.json"], metainfo)
	}
}

func TestPackOverwritesSourceMetadataInOutputOnly(t *testing.T) {
	src := setupSourceDir(t)
	if err := os.MkdirAll(filepath.Join(src, ".llar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".llar", "metadata.json"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.zip")
	metainfo := []byte("new")

	if err := Pack(src, dst, metainfo); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	files := readZip(t, dst)
	if string(files[".llar/metadata.json"]) != string(metainfo) {
		t.Fatalf("zip metadata = %q, want %q", files[".llar/metadata.json"], metainfo)
	}
	assertFileContent(t, filepath.Join(src, ".llar", "metadata.json"), "old")
}

func TestPackCopiesNestedDirectories(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "b", "c", "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "nested.tar.gz")

	if err := Pack(src, dst, []byte("{}")); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	files := readTarGz(t, dst)
	if string(files["a/b/c/deep.txt"]) != "deep" {
		t.Fatalf("tar.gz a/b/c/deep.txt = %q, want %q", files["a/b/c/deep.txt"], "deep")
	}
}

func TestPackReturnsCreateError(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "missing", "out.zip")

	if err := Pack(src, dst, []byte("{}")); err == nil {
		t.Fatal("Pack error = nil, want create error")
	}
}

func TestPackDirectoryReturnsCopyError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "missing")
	dst := filepath.Join(t.TempDir(), "out")

	if err := Pack(src, dst, []byte("{}")); err == nil {
		t.Fatal("Pack error = nil, want copy error")
	}
}

func TestPackTarGzReturnsCreateError(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "missing", "out.tar.gz")

	if err := Pack(src, dst, []byte("{}")); err == nil {
		t.Fatal("Pack error = nil, want create error")
	}
}

func TestPackReturnsWalkError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "missing")
	dst := filepath.Join(t.TempDir(), "out.tar.gz")

	if err := Pack(src, dst, []byte("{}")); err == nil {
		t.Fatal("Pack error = nil, want walk error")
	}
}

func TestPackReturnsOpenError(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "missing-target"), filepath.Join(src, "broken-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out.zip")

	if err := Pack(src, dst, []byte("{}")); err == nil {
		t.Fatal("Pack error = nil, want open error")
	}
}

func TestPackTarGzReturnsOpenError(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "missing-target"), filepath.Join(src, "broken-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out.tar.gz")

	if err := Pack(src, dst, []byte("{}")); err == nil {
		t.Fatal("Pack error = nil, want open error")
	}
}

func TestWriteMetadataReturnsMkdirError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(root, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeMetadata(root, []byte("{}")); err == nil {
		t.Fatal("writeMetadata error = nil, want mkdir error")
	}
}

func TestWriteTarFileReturnsWriteHeaderError(t *testing.T) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if err := writeTarFile(w, "metadata.json", []byte("{}")); err == nil {
		t.Fatal("writeTarFile error = nil, want write header error")
	}
}

func setupSourceDir(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "lib", "libfoo.a"), []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "include", "foo.h"), []byte("#pragma once"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func readZip(t *testing.T, path string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	files := map[string][]byte{}
	for _, file := range r.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		files[file.Name] = data
	}
	return files
}

func readTarGz(t *testing.T, path string) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open tar.gz: %v", err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		files[header.Name] = data
	}
}
