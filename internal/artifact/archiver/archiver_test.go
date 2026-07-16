package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPackRejectsNonArchiveOutput(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "out")

	err := Pack(src, dst, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Pack error = nil, want unsupported output error")
	}
	if !strings.Contains(err.Error(), "unsupported artifact output") {
		t.Fatalf("Pack error = %v, want unsupported artifact output", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("unsupported output created dst: %v", err)
	}
}

func TestPackRejectsInvalidMetainfoJSON(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "out.zip")

	err := Pack(src, dst, json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("Pack error = nil, want invalid JSON error")
	}
	if !strings.Contains(err.Error(), "invalid artifact metainfo JSON") {
		t.Fatalf("Pack error = %v, want invalid artifact metainfo JSON", err)
	}
}

func TestPackZipAddsMetadataAndPayload(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "out.zip")
	metainfo := json.RawMessage(`{"metadata":"-lfoo"}`)

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
	metainfo := json.RawMessage(`{"metadata":"-lfoo"}`)

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

func TestPackFSAddsMetadataAndPayload(t *testing.T) {
	src := fstest.MapFS{
		"lib/libfoo.a":        &fstest.MapFile{Data: []byte("archive"), Mode: 0o644},
		"include/foo.h":       &fstest.MapFile{Data: []byte("#pragma once"), Mode: 0o644},
		".llar/metadata.json": &fstest.MapFile{Data: []byte("old"), Mode: 0o644},
	}
	metainfo := json.RawMessage(`{"metadata":"-lfoo"}`)

	t.Run("zip", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out.zip")
		if err := PackFS(src, dst, metainfo); err != nil {
			t.Fatalf("PackFS: %v", err)
		}
		files := readZip(t, dst)
		if string(files["lib/libfoo.a"]) != "archive" {
			t.Fatalf("zip lib/libfoo.a = %q, want %q", files["lib/libfoo.a"], "archive")
		}
		if string(files[".llar/metadata.json"]) != string(metainfo) {
			t.Fatalf("zip metadata = %q, want %q", files[".llar/metadata.json"], metainfo)
		}
	})

	t.Run("tar.gz", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out.tar.gz")
		if err := PackFS(src, dst, metainfo); err != nil {
			t.Fatalf("PackFS: %v", err)
		}
		files := readTarGz(t, dst)
		if string(files["lib/libfoo.a"]) != "archive" {
			t.Fatalf("tar.gz lib/libfoo.a = %q, want %q", files["lib/libfoo.a"], "archive")
		}
		if string(files[".llar/metadata.json"]) != string(metainfo) {
			t.Fatalf("tar.gz metadata = %q, want %q", files[".llar/metadata.json"], metainfo)
		}
	})
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
	metainfo := json.RawMessage(`{"metadata":"new"}`)

	if err := Pack(src, dst, metainfo); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	files := readZip(t, dst)
	if string(files[".llar/metadata.json"]) != string(metainfo) {
		t.Fatalf("zip metadata = %q, want %q", files[".llar/metadata.json"], metainfo)
	}
	assertFileContent(t, filepath.Join(src, ".llar", "metadata.json"), "old")
}

func TestPackTarGzIncludesNestedFiles(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "b", "c", "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "nested.tar.gz")

	if err := Pack(src, dst, json.RawMessage(`{}`)); err != nil {
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

	if err := Pack(src, dst, json.RawMessage(`{}`)); err == nil {
		t.Fatal("Pack error = nil, want create error")
	}
}

func TestPackTarGzReturnsCreateError(t *testing.T) {
	src := setupSourceDir(t)
	dst := filepath.Join(t.TempDir(), "missing", "out.tar.gz")

	if err := Pack(src, dst, json.RawMessage(`{}`)); err == nil {
		t.Fatal("Pack error = nil, want create error")
	}
}

func TestPackReturnsWalkError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "missing")
	dst := filepath.Join(t.TempDir(), "out.tar.gz")

	if err := Pack(src, dst, json.RawMessage(`{}`)); err == nil {
		t.Fatal("Pack error = nil, want walk error")
	}
}

func TestPackReturnsOpenError(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "missing-target"), filepath.Join(src, "broken-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out.zip")

	if err := Pack(src, dst, json.RawMessage(`{}`)); err == nil {
		t.Fatal("Pack error = nil, want open error")
	}
}

func TestPackTarGzReturnsOpenError(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "missing-target"), filepath.Join(src, "broken-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out.tar.gz")

	if err := Pack(src, dst, json.RawMessage(`{}`)); err == nil {
		t.Fatal("Pack error = nil, want open error")
	}
}

func TestPackFSReturnsSourceErrors(t *testing.T) {
	testErr := errors.New("test filesystem error")
	tests := []struct {
		name string
		fsys fs.FS
	}{
		{
			name: "walk",
			fsys: &faultFS{err: testErr},
		},
		{
			name: "info",
			fsys: &faultFS{
				fsys:        fstest.MapFS{"payload": &fstest.MapFile{Data: []byte("data")}},
				infoErrName: "payload",
				err:         testErr,
			},
		},
		{
			name: "read",
			fsys: &faultFS{
				fsys:        fstest.MapFS{"payload": &fstest.MapFile{Data: []byte("data")}},
				readErrName: "payload",
				err:         testErr,
			},
		},
	}

	for _, test := range tests {
		for _, ext := range []string{".zip", ".tar.gz"} {
			t.Run(test.name+ext, func(t *testing.T) {
				dst := filepath.Join(t.TempDir(), "out"+ext)
				err := PackFS(test.fsys, dst, json.RawMessage(`{}`))
				if !errors.Is(err, testErr) {
					t.Fatalf("PackFS error = %v, want %v", err, testErr)
				}
			})
		}
	}
}

func TestPackTarRejectsUnsupportedFileMode(t *testing.T) {
	src := &faultFS{
		fsys:     fstest.MapFS{"socket": &fstest.MapFile{}},
		modeName: "socket",
		mode:     fs.ModeSocket,
	}
	tw := tar.NewWriter(io.Discard)

	if err := packTar(tw, src); err == nil {
		t.Fatal("packTar error = nil, want unsupported file mode error")
	}
}

func TestPackReturnsWriterErrors(t *testing.T) {
	src := fstest.MapFS{"payload": &fstest.MapFile{Data: []byte("data")}}
	testErr := errors.New("test compressor error")
	compressor := func(io.Writer) (io.WriteCloser, error) {
		return nil, testErr
	}

	t.Run("zip payload", func(t *testing.T) {
		w := zip.NewWriter(io.Discard)
		w.RegisterCompressor(zip.Deflate, compressor)
		if err := packZip(w, src); !errors.Is(err, testErr) {
			t.Fatalf("packZip error = %v, want %v", err, testErr)
		}
	})

	t.Run("zip metadata", func(t *testing.T) {
		w := zip.NewWriter(io.Discard)
		w.RegisterCompressor(zip.Deflate, compressor)
		if err := writeZipMetadata(w, json.RawMessage(`{}`)); !errors.Is(err, testErr) {
			t.Fatalf("writeZipMetadata error = %v, want %v", err, testErr)
		}
	})

	t.Run("tar payload", func(t *testing.T) {
		w := tar.NewWriter(io.Discard)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := packTar(w, src); err == nil {
			t.Fatal("packTar error = nil, want closed writer error")
		}
	})

	t.Run("tar metadata", func(t *testing.T) {
		w := tar.NewWriter(io.Discard)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := writeTarMetadata(w, json.RawMessage(`{}`)); err == nil {
			t.Fatal("writeTarMetadata error = nil, want closed writer error")
		}
	})
}

type faultFS struct {
	fsys        fs.FS
	readErrName string
	infoErrName string
	modeName    string
	mode        fs.FileMode
	err         error
}

func (f *faultFS) Open(name string) (fs.File, error) {
	if f.fsys == nil {
		return nil, f.err
	}
	file, err := f.fsys.Open(name)
	if err != nil {
		return nil, err
	}
	if name == f.readErrName {
		return &faultFile{File: file, err: f.err}, nil
	}
	return file, nil
}

func (f *faultFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(f.fsys, name)
	if err != nil {
		return nil, err
	}
	for i, entry := range entries {
		if entry.Name() == f.infoErrName || entry.Name() == f.modeName {
			entries[i] = &faultDirEntry{
				DirEntry: entry,
				infoErr:  entry.Name() == f.infoErrName,
				mode:     f.mode,
				err:      f.err,
			}
		}
	}
	return entries, nil
}

type faultFile struct {
	fs.File
	err error
}

func (f *faultFile) Read([]byte) (int, error) {
	return 0, f.err
}

type faultDirEntry struct {
	fs.DirEntry
	infoErr bool
	mode    fs.FileMode
	err     error
}

func (e *faultDirEntry) Info() (fs.FileInfo, error) {
	if e.infoErr {
		return nil, e.err
	}
	info, err := e.DirEntry.Info()
	if err != nil {
		return nil, err
	}
	return &faultFileInfo{FileInfo: info, mode: e.mode}, nil
}

type faultFileInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (i *faultFileInfo) Mode() fs.FileMode {
	return i.mode
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
