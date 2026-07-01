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
		return writeZipArtifact(srcDir, dst, metainfo)
	}
	if strings.HasSuffix(dst, ".tar.gz") {
		return writeTarGzArtifact(srcDir, dst, metainfo)
	}
	return fmt.Errorf("unsupported artifact output %q: use .zip or .tar.gz", dst)
}

func Unpack(src, dstDir string) error {
	if strings.HasSuffix(src, ".zip") {
		return unpackZip(src, dstDir)
	}
	if strings.HasSuffix(src, ".tar.gz") {
		return unpackTarGz(src, dstDir)
	}
	return fmt.Errorf("unsupported artifact input %q: use .zip or .tar.gz", src)
}

func writeZipArtifact(srcDir, dst string, metainfo json.RawMessage) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	if err := packZip(w, srcDir); err != nil {
		return err
	}
	return writeZipMetadata(w, metainfo)
}

func unpackZip(src, dstDir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	for _, file := range r.File {
		target, err := unpackPath(dstDir, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, file.Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		err = writeUnpackedFile(target, file.Mode(), rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func packZip(w *zip.Writer, srcDir string) error {
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

func writeTarGzArtifact(srcDir, dst string, metainfo json.RawMessage) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	if err := packTar(tw, srcDir); err != nil {
		return err
	}
	return writeTarMetadata(tw, metainfo)
}

func unpackTarGz(src, dstDir string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := unpackPath(dstDir, header.Name)
		if err != nil {
			return err
		}
		info := header.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, info.Mode()); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeUnpackedFile(target, info.Mode(), tr); err != nil {
			return err
		}
	}
}

func unpackPath(dstDir, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
		return "", fmt.Errorf("invalid artifact path %q", name)
	}
	target := filepath.Join(dstDir, clean)
	rel, err := filepath.Rel(dstDir, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("artifact path %q escapes output directory", name)
	}
	return target, nil
}

func writeUnpackedFile(path string, mode os.FileMode, r io.Reader) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, r)
	return err
}

func packTar(tw *tar.Writer, srcDir string) error {
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
