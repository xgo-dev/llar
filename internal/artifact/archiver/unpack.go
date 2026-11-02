package archiver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Unpack extracts an LLAR binary artifact into dst and returns its metainfo.
// Symlink targets are preserved verbatim. Extraction never writes through a
// symlink outside dst, for example a later entry below a link to /lib.
func Unpack(src, dst string) (metadata json.RawMessage, err error) {
	switch {
	case strings.HasSuffix(src, ".tar.gz"):
		return unpackTarGz(src, dst)
	case strings.HasSuffix(src, ".zip"):
		return unpackZip(src, dst)
	default:
		return nil, fmt.Errorf("unsupported artifact input %q: use .zip or .tar.gz", src)
	}
}

func unpackTarGz(src, dst string) (json.RawMessage, error) {
	file, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var metainfo json.RawMessage
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if metainfo == nil {
				return nil, fmt.Errorf("artifact metadata is missing")
			}
			return metainfo, nil
		}
		if err != nil {
			return nil, err
		}
		name, err := cleanArchiveName(header.Name)
		if err != nil {
			return nil, err
		}
		if name == filepath.FromSlash(metadataPath) {
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				return nil, fmt.Errorf("extract %s: unsupported tar type %d", header.Name, header.Typeflag)
			}
			metainfo, err = io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, header.FileInfo().Mode().Perm()); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeUnpackedFile(root, name, header.FileInfo().Mode().Perm(), tr); err != nil {
				return nil, err
			}
		case tar.TypeSymlink:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return nil, err
			}
			if err := root.Symlink(header.Linkname, name); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("extract %s: unsupported tar type %d", header.Name, header.Typeflag)
		}
	}
}

func unpackZip(src, dst string) (json.RawMessage, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var metainfo json.RawMessage
	for _, entry := range zr.File {
		name, err := cleanArchiveName(entry.Name)
		if err != nil {
			return nil, err
		}
		info := entry.FileInfo()
		mode := info.Mode()
		if name == filepath.FromSlash(metadataPath) {
			if !mode.IsRegular() {
				return nil, fmt.Errorf("extract %s: unsupported zip mode %s", entry.Name, mode)
			}
			file, err := entry.Open()
			if err != nil {
				return nil, err
			}
			metainfo, err = io.ReadAll(file)
			closeErr := file.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			continue
		}

		if info.IsDir() {
			if err := root.MkdirAll(name, mode.Perm()); err != nil {
				return nil, err
			}
			continue
		}
		if !mode.IsRegular() && mode&fs.ModeSymlink == 0 {
			return nil, fmt.Errorf("extract %s: unsupported zip mode %s", entry.Name, mode)
		}
		file, err := entry.Open()
		if err != nil {
			return nil, err
		}
		if mode&fs.ModeSymlink != 0 {
			var target []byte
			target, err = io.ReadAll(file)
			if err == nil {
				err = root.MkdirAll(filepath.Dir(name), 0o755)
			}
			if err == nil {
				err = root.Symlink(string(target), name)
			}
		} else {
			err = writeUnpackedFile(root, name, mode.Perm(), file)
		}
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if metainfo == nil {
		return nil, fmt.Errorf("artifact metadata is missing")
	}
	return metainfo, nil
}

func writeUnpackedFile(root *os.Root, name string, mode fs.FileMode, src io.Reader) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, src)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func cleanArchiveName(name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return name, nil
}
