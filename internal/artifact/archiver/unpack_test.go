package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
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

func TestUnpackTarGzEntries(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		artifact := writeTestTarGz(t,
			tarTestEntry{header: tar.Header{Name: "include/", Typeflag: tar.TypeDir, Mode: 0o755}},
			tarTestEntry{header: tar.Header{Name: "include/foo.h", Mode: 0o644}, body: []byte("header\n")},
			tarTestEntry{header: tar.Header{Name: metadataPath, Mode: 0o644}, body: []byte(`{}`)},
		)
		dst := t.TempDir()
		if _, err := Unpack(artifact, dst); err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, filepath.Join(dst, "include", "foo.h"), "header\n")
	})

	t.Run("invalid gzip", func(t *testing.T) {
		artifact := filepath.Join(t.TempDir(), "artifact.tar.gz")
		if err := os.WriteFile(artifact, []byte("not gzip"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(artifact, t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("invalid tar", func(t *testing.T) {
		artifact := filepath.Join(t.TempDir(), "artifact.tar.gz")
		file, err := os.Create(artifact)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(file)
		if _, err := gz.Write([]byte("not tar")); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(artifact, t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("missing metadata", func(t *testing.T) {
		artifact := writeTestTarGz(t, tarTestEntry{
			header: tar.Header{Name: "lib.a", Mode: 0o644},
			body:   []byte("archive"),
		})
		if _, err := Unpack(artifact, t.TempDir()); err == nil || !strings.Contains(err.Error(), "metadata is missing") {
			t.Fatalf("Unpack error = %v, want missing metadata", err)
		}
	})

	for _, test := range []struct {
		name  string
		entry tarTestEntry
	}{
		{
			name:  "metadata symlink",
			entry: tarTestEntry{header: tar.Header{Name: metadataPath, Typeflag: tar.TypeSymlink, Linkname: "target"}},
		},
		{
			name:  "payload symlink",
			entry: tarTestEntry{header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := writeTestTarGz(t, test.entry)
			if _, err := Unpack(artifact, t.TempDir()); err == nil {
				t.Fatal("Unpack error = nil")
			}
		})
	}

	t.Run("directory conflicts with file", func(t *testing.T) {
		artifact := writeTestTarGz(t,
			tarTestEntry{header: tar.Header{Name: "include/", Typeflag: tar.TypeDir, Mode: 0o755}},
			tarTestEntry{header: tar.Header{Name: metadataPath, Mode: 0o644}, body: []byte(`{}`)},
		)
		dst := t.TempDir()
		if err := os.WriteFile(filepath.Join(dst, "include"), []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(artifact, dst); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})
}

func TestUnpackZipEntries(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		artifact := writeTestZip(t,
			zipTestEntry{name: "include/", mode: fs.ModeDir | 0o755},
			zipTestEntry{name: "include/foo.h", mode: 0o644, body: []byte("header\n")},
			zipTestEntry{name: metadataPath, mode: 0o644, body: []byte(`{}`)},
		)
		dst := t.TempDir()
		if _, err := Unpack(artifact, dst); err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, filepath.Join(dst, "include", "foo.h"), "header\n")
	})

	t.Run("invalid zip", func(t *testing.T) {
		artifact := filepath.Join(t.TempDir(), "artifact.zip")
		if err := os.WriteFile(artifact, []byte("not zip"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(artifact, t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("unsafe path", func(t *testing.T) {
		artifact := writeTestZip(t, zipTestEntry{name: "../escape", mode: 0o644, body: []byte("x")})
		if _, err := Unpack(artifact, t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("metadata symlink", func(t *testing.T) {
		artifact := writeTestZip(t, zipTestEntry{name: metadataPath, mode: fs.ModeSymlink | 0o777})
		if _, err := Unpack(artifact, t.TempDir()); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("directory conflicts with file", func(t *testing.T) {
		artifact := writeTestZip(t,
			zipTestEntry{name: "include/", mode: fs.ModeDir | 0o755},
			zipTestEntry{name: metadataPath, mode: 0o644, body: []byte(`{}`)},
		)
		dst := t.TempDir()
		if err := os.WriteFile(filepath.Join(dst, "include"), []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(artifact, dst); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})

	t.Run("file conflicts with directory", func(t *testing.T) {
		artifact := writeTestZip(t,
			zipTestEntry{name: "lib.a", mode: 0o644, body: []byte("archive")},
			zipTestEntry{name: metadataPath, mode: 0o644, body: []byte(`{}`)},
		)
		dst := t.TempDir()
		if err := os.Mkdir(filepath.Join(dst, "lib.a"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(artifact, dst); err == nil {
			t.Fatal("Unpack error = nil")
		}
	})
}

func TestWriteUnpackedFileErrors(t *testing.T) {
	t.Run("create parent", func(t *testing.T) {
		root := t.TempDir()
		parent := filepath.Join(root, "parent")
		if err := os.WriteFile(parent, []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeUnpackedFile(filepath.Join(parent, "child"), 0o644, strings.NewReader("data")); err == nil {
			t.Fatal("writeUnpackedFile error = nil")
		}
	})

	t.Run("open target", func(t *testing.T) {
		if err := writeUnpackedFile(t.TempDir(), 0o644, strings.NewReader("data")); err == nil {
			t.Fatal("writeUnpackedFile error = nil")
		}
	})

	t.Run("copy", func(t *testing.T) {
		testErr := errors.New("read failed")
		err := writeUnpackedFile(filepath.Join(t.TempDir(), "file"), 0o644, errorReader{err: testErr})
		if !errors.Is(err, testErr) {
			t.Fatalf("writeUnpackedFile error = %v, want %v", err, testErr)
		}
	})
}

type tarTestEntry struct {
	header tar.Header
	body   []byte
}

func writeTestTarGz(t *testing.T, entries ...tarTestEntry) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "artifact.tar.gz")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	writer := tar.NewWriter(gz)
	for _, entry := range entries {
		entry.header.Size = int64(len(entry.body))
		if err := writer.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
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
	return name
}

type zipTestEntry struct {
	name string
	mode fs.FileMode
	body []byte
}

func writeTestZip(t *testing.T, entries ...zipTestEntry) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "artifact.zip")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name}
		header.SetMode(entry.mode)
		output, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

var _ io.Reader = errorReader{}
