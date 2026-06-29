package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const metadataPath = ".llar/metadata.json"

// Pack writes srcDir as an LLAR binary artifact at dst.
// The metainfo bytes are written verbatim to .llar/metadata.json.
func Pack(srcDir, dst string, metainfo json.RawMessage) error {
	if !json.Valid(metainfo) {
		return fmt.Errorf("invalid artifact metainfo JSON")
	}
	if strings.HasSuffix(dst, ".zip") {
		return packZip(srcDir, dst, metainfo)
	}
	if strings.HasSuffix(dst, ".tar.gz") {
		return packTarGz(srcDir, dst, metainfo)
	}
	return fmt.Errorf("unsupported artifact output %q: use .zip or .tar.gz", dst)
}

func packZip(srcDir, dst string, metainfo json.RawMessage) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	if err := writeZipPayload(w, srcDir); err != nil {
		return err
	}
	return writeZipMetadata(w, metainfo)
}

func writeZipPayload(w *zip.Writer, srcDir string) error {
	add := func(path, name string, info os.FileInfo) error {
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
		return add(path, name, info)
	}
	return filepath.Walk(srcDir, walk)
}

func writeZipMetadata(w *zip.Writer, metainfo json.RawMessage) error {
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

func packTarGz(srcDir, dst string, metainfo json.RawMessage) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	if err := writeTarPayload(tw, srcDir); err != nil {
		return err
	}
	return writeTarMetadata(tw, metainfo)
}

func writeTarPayload(tw *tar.Writer, srcDir string) error {
	add := func(path, name string, info os.FileInfo) error {
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
		return add(path, name, info)
	}
	return filepath.Walk(srcDir, walk)
}

func writeTarMetadata(tw *tar.Writer, metainfo json.RawMessage) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: metadataPath,
		Mode: 0o644,
		Size: int64(len(metainfo)),
	}); err != nil {
		return err
	}
	_, err := tw.Write(metainfo)
	return err
}
