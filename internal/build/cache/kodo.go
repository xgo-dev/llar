package cache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/mod/module"
	qiniuclient "github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storagev2/credentials"
	qiniudownloader "github.com/qiniu/go-sdk/v7/storagev2/downloader"
	httpclient "github.com/qiniu/go-sdk/v7/storagev2/http_client"
	"github.com/qiniu/go-sdk/v7/storagev2/objects"
	"github.com/qiniu/go-sdk/v7/storagev2/uploader"
	"github.com/qiniu/go-sdk/v7/storagev2/uptoken"
)

type KodoConfig struct {
	AccessKey    string
	SecretKey    string
	Bucket       string
	PublicDomain string
	Prefix       string
	WorkspaceDir string
	Artifacts    artifact.Store
}

type kodoCache struct {
	bucket       string
	publicDomain string
	prefix       string
	workspaceDir string
	artifacts    artifact.Store
	credentials  *credentials.Credentials
	objects      *objects.ObjectsManager
	uploader     *uploader.UploadManager
	downloader   *qiniudownloader.DownloadManager
}

func NewKodo(cfg KodoConfig) Cache {
	cred := credentials.NewCredentials(cfg.AccessKey, cfg.SecretKey)
	options := httpclient.Options{Credentials: cred}
	return &kodoCache{
		bucket:       cfg.Bucket,
		publicDomain: normalizePublicDomain(cfg.PublicDomain),
		prefix:       strings.Trim(cfg.Prefix, "/"),
		workspaceDir: cfg.WorkspaceDir,
		artifacts:    cfg.Artifacts,
		credentials:  cred,
		objects: objects.NewObjectsManager(&objects.ObjectsManagerOptions{
			Options: options,
		}),
		uploader: uploader.NewUploadManager(&uploader.UploadManagerOptions{
			Options: options,
		}),
		downloader: qiniudownloader.NewDownloadManager(&qiniudownloader.DownloadManagerOptions{
			Options: options,
		}),
	}
}

func (c *kodoCache) Get(ctx context.Context, key Key) (Entry, bool, error) {
	if c.artifacts == nil {
		return Entry{}, false, nil
	}
	art, err := c.artifacts.Get(ctx, artifact.Key{
		Module:    key.Module.Path,
		Version:   key.Module.Version,
		MatrixStr: key.Matrix,
	})
	if errors.Is(err, artifact.ErrNotFound) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	if art.Source.Type != "kodo" {
		return Entry{}, false, fmt.Errorf("artifact source type = %q, want kodo", art.Source.Type)
	}
	objectName, err := parseKodoSourceURL(art.Source.URL)
	if err != nil {
		return Entry{}, false, err
	}
	if c.workspaceDir != "" {
		if err := c.restore(ctx, key, objectName, art.Checksum); err != nil {
			return Entry{}, false, err
		}
	}
	return Entry{Metadata: art.Metadata}, true, nil
}

func (c *kodoCache) Put(ctx context.Context, key Key, output fs.FS, entry Entry) (Entry, error) {
	objectName := c.objectName(key)
	putPolicy, err := uptoken.NewPutPolicyWithKey(c.bucket, objectName, time.Now().Add(time.Hour))
	if err != nil {
		return Entry{}, err
	}
	putPolicy.SetInsertOnly(1)

	file, err := os.CreateTemp("", "llar-kodo-*.tar.gz")
	if err != nil {
		return Entry{}, err
	}
	defer os.Remove(file.Name())

	hash := sha256.New()
	if err := writeTarGzip(io.MultiWriter(file, hash), output); err != nil {
		_ = file.Close()
		return Entry{}, err
	}
	if err := file.Close(); err != nil {
		return Entry{}, err
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	var sourceURL string
	if c.artifacts != nil {
		var err error
		sourceURL, err = kodoSourceURL(c.publicDomain, objectName)
		if err != nil {
			return Entry{}, err
		}
	}

	err = c.uploader.UploadFile(ctx, file.Name(), &uploader.ObjectOptions{
		BucketName:  c.bucket,
		ObjectName:  &objectName,
		FileName:    path.Base(objectName),
		ContentType: "application/gzip",
		UpToken:     uptoken.NewSigner(putPolicy, c.credentials),
	}, nil)
	if err != nil {
		if isKodoObjectExists(err) {
			if got, ok, getErr := c.Get(ctx, key); getErr != nil {
				return Entry{}, getErr
			} else if ok {
				return got, nil
			}
		}
		return Entry{}, err
	}
	if c.artifacts != nil {
		if _, err := c.artifacts.Put(ctx, artifact.Key{
			Module:    key.Module.Path,
			Version:   key.Module.Version,
			MatrixStr: key.Matrix,
		}, artifact.Artifact{
			Source: artifact.Source{
				Type: "kodo",
				URL:  sourceURL,
			},
			Type:     "tar.gz",
			Metadata: entry.Metadata,
			Checksum: checksum,
		}); err != nil {
			return Entry{}, err
		}
	}
	return entry, nil
}

func (c *kodoCache) objectName(key Key) string {
	parts := make([]string, 0, 4)
	if c.prefix != "" {
		parts = append(parts, c.prefix)
	}
	parts = append(parts, strings.Trim(key.Module.Path, "/"), strings.Trim(key.Module.Version, "/"), key.Matrix+".tar.gz")
	return strings.Join(parts, "/")
}

func (c *kodoCache) installDir(key Key) (string, error) {
	escaped, err := module.EscapePath(key.Module.Path)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.workspaceDir, fmt.Sprintf("%s@%s-%s", escaped, key.Module.Version, key.Matrix)), nil
}

func (c *kodoCache) restore(ctx context.Context, key Key, objectName, checksum string) error {
	installDir, err := c.installDir(key)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "llar-kodo-restore-*.tar.gz")
	if err != nil {
		return err
	}
	fileName := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(fileName)
		return err
	}
	defer os.Remove(fileName)

	_, err = c.downloader.DownloadToFile(ctx, objectName, fileName, &qiniudownloader.ObjectOptions{
		GenerateOptions: qiniudownloader.GenerateOptions{
			BucketName: c.bucket,
		},
	})
	if err != nil {
		return err
	}
	if checksum != "" {
		got, err := fileSHA256(fileName)
		if err != nil {
			return err
		}
		if got != checksum {
			return fmt.Errorf("kodo artifact checksum = %s, want %s", got, checksum)
		}
	}
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return err
	}
	file, err = os.Open(fileName)
	if err != nil {
		return err
	}
	defer file.Close()
	return extractTarGzip(file, installDir)
}

func normalizePublicDomain(domain string) string {
	domain = strings.TrimRight(strings.TrimSpace(domain), "/")
	if domain == "" || strings.Contains(domain, "://") {
		return domain
	}
	return "http://" + domain
}

func kodoSourceURL(domain, objectName string) (string, error) {
	u, err := url.Parse(normalizePublicDomain(domain))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("kodo public domain must be http(s), got %q", domain)
	}
	u.Path = "/" + objectName
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func parseKodoSourceURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("invalid kodo source url %q", raw)
	}
	objectName, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil {
		return "", err
	}
	if objectName == "" {
		return "", fmt.Errorf("invalid kodo source url %q", raw)
	}
	return objectName, nil
}

func isKodoObjectNotFound(err error) bool {
	var info *qiniuclient.ErrorInfo
	return errors.As(err, &info) && info.Code == 612
}

func isKodoObjectExists(err error) bool {
	var info *qiniuclient.ErrorInfo
	return errors.As(err, &info) && info.Code == 614
}

func fileSHA256(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeTarGzip(w io.Writer, src fs.FS) error {
	gzw := gzip.NewWriter(w)
	tw := tar.NewWriter(gzw)

	if err := fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("archive %s: unsupported file mode %s", name, info.Mode())
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		header.ModTime = time.Unix(0, 0)
		header.AccessTime = time.Unix(0, 0)
		header.ChangeTime = time.Unix(0, 0)
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		file, err := src.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		_ = tw.Close()
		_ = gzw.Close()
		return err
	}

	if err := tw.Close(); err != nil {
		_ = gzw.Close()
		return err
	}
	return gzw.Close()
}

func extractTarGzip(r io.Reader, dst string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := cleanTarName(header.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, header.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tr)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("extract %s: unsupported tar type %d", header.Name, header.Typeflag)
		}
	}
}

func cleanTarName(name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe tar path %q", name)
	}
	return name, nil
}
