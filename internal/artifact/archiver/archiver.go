package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
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
	defer w.Close()

	addFile := func(path, name string, info os.FileInfo) error {
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
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err
	}
	walk := func(path string, info os.FileInfo, err error) error {
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
		return addFile(path, name, info)
	}
	if err := filepath.Walk(srcDir, walk); err != nil {
		return err
	}

	header := &zip.FileHeader{
		Name:   metadataPath,
		Method: zip.Deflate,
	}
	header.SetMode(0o644)
	writer, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(metainfo)
	return err
}

func packTarGz(srcDir, dst string, metainfo []byte) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	addFile := func(path, name string, info os.FileInfo) error {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	}
	walk := func(path string, info os.FileInfo, err error) error {
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
		return addFile(path, name, info)
	}
	if err := filepath.Walk(srcDir, walk); err != nil {
		return err
	}

	if err := tw.WriteHeader(&tar.Header{
		Name: metadataPath,
		Mode: 0o644,
		Size: int64(len(metainfo)),
	}); err != nil {
		return err
	}
	_, err = tw.Write(metainfo)
	return err
}
