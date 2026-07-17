package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

const metadataPath = ".llar/metadata.json"

// Pack writes srcDir as an LLAR binary artifact at dst.
// The metainfo bytes are written verbatim to .llar/metadata.json.
func Pack(srcDir, dst string, metainfo json.RawMessage) error {
	return PackFS(os.DirFS(srcDir), dst, metainfo)
}

// PackFS writes src as an LLAR binary artifact at dst.
// The metainfo bytes are written verbatim to .llar/metadata.json.
// TODO: Copy symlink targets as regular files instead of rejecting link entries.
func PackFS(src fs.FS, dst string, metainfo json.RawMessage) error {
	if !json.Valid(metainfo) {
		return fmt.Errorf("invalid artifact metainfo JSON")
	}
	if strings.HasSuffix(dst, ".zip") {
		return writeZipArtifact(src, dst, metainfo)
	}
	if strings.HasSuffix(dst, ".tar.gz") {
		return writeTarGzArtifact(src, dst, metainfo)
	}
	return fmt.Errorf("unsupported artifact output %q: use .zip or .tar.gz", dst)
}

func writeZipArtifact(src fs.FS, dst string, metainfo json.RawMessage) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	if err := packZip(w, src); err != nil {
		return err
	}
	return writeZipMetadata(w, metainfo)
}

func packZip(w *zip.Writer, src fs.FS) error {
	add := func(name string, info fs.FileInfo) error {
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
		file, err := src.Open(name)
		if err != nil {
			return err
		}
		if _, err := io.Copy(writer, file); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}

	return fs.WalkDir(src, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		if name == metadataPath {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return add(name, info)
	})
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

func writeTarGzArtifact(src fs.FS, dst string, metainfo json.RawMessage) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	if err := packTar(tw, src); err != nil {
		return err
	}
	return writeTarMetadata(tw, metainfo)
}

func packTar(tw *tar.Writer, src fs.FS) error {
	add := func(name string, info fs.FileInfo) error {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		file, err := src.Open(name)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, file); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}

	return fs.WalkDir(src, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		if name == metadataPath {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return add(name, info)
	})
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
