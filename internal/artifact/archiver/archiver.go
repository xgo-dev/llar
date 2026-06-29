package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const metadataPath = ".llar/metadata.json"

// Pack writes srcDir as an LLAR binary artifact at dst.
// The metainfo bytes are written verbatim to .llar/metadata.json.
func Pack(srcDir, dst string, metainfo []byte) error {
	if strings.HasSuffix(dst, ".zip") {
		return packZip(srcDir, dst, metainfo)
	}
	if strings.HasSuffix(dst, ".tar.gz") {
		return packTarGz(srcDir, dst, metainfo)
	}
	return fmt.Errorf("unsupported artifact output %q: use .zip or .tar.gz", dst)
}

func packZip(srcDir, dst string, metainfo []byte) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	if err := walkFiles(srcDir, func(path, name string, info fs.FileInfo) error {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = name
		header.Method = zip.Deflate

		writer, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		return copyFile(writer, path)
	}); err != nil {
		_ = w.Close()
		return err
	}
	if err := writeZipFile(w, metadataPath, metainfo); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func writeZipFile(w *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	header.SetMode(0o644)
	writer, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func packTarGz(srcDir, dst string, metainfo []byte) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := walkFiles(srcDir, func(path, name string, info fs.FileInfo) error {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		return copyFile(tw, path)
	}); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}
	if err := writeTarFile(tw, metadataPath, metainfo); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

func writeTarFile(w *tar.Writer, name string, data []byte) error {
	if err := w.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func walkFiles(srcDir string, fn func(path, name string, info fs.FileInfo) error) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if name == metadataPath {
			return nil
		}
		return fn(path, name, info)
	})
}

func copyFile(dst io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(dst, file)
	return err
}
