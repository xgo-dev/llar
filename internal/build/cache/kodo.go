package cache

import (
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
	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/metadata"
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
	if c.workspaceDir == "" {
		return Entry{}, false, errors.New("kodo cache workspace dir is required")
	}
	objectName := c.objectName(key)
	data, err := c.restore(ctx, key, objectName, art.Type, art.Checksum)
	if err != nil {
		return Entry{}, false, err
	}
	installDir, err := c.installDir(key)
	if err != nil {
		return Entry{}, false, err
	}
	info, err := metadata.Decode(data, installDir)
	if err != nil {
		return Entry{}, false, err
	}
	return Entry{Metadata: info.Metadata, Deps: info.Deps}, true, nil
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
	if err := file.Close(); err != nil {
		return Entry{}, err
	}

	installDir, err := c.installDir(key)
	if err != nil {
		return Entry{}, err
	}
	metadataJSON, err := metadata.Encode(metadata.Info{
		Metadata: entry.Metadata,
		Deps:     entry.Deps,
	}, installDir)
	if err != nil {
		return Entry{}, err
	}
	if err := archiver.PackFS(output, file.Name(), metadataJSON); err != nil {
		return Entry{}, err
	}
	checksum, err := fileSHA256(file.Name())
	if err != nil {
		return Entry{}, err
	}
	sourceURL, err := kodoSourceURL(c.publicDomain, objectName)
	if err != nil {
		return Entry{}, err
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
		Checksum: checksum,
	}); err != nil {
		return Entry{}, err
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

func (c *kodoCache) restore(ctx context.Context, key Key, objectName, artifactType, checksum string) ([]byte, error) {
	installDir, err := c.installDir(key)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "llar-kodo-restore-*."+path.Base(artifactType))
	if err != nil {
		return nil, err
	}
	fileName := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(fileName)
		return nil, err
	}
	defer os.Remove(fileName)

	_, err = c.downloader.DownloadToFile(ctx, objectName, fileName, &qiniudownloader.ObjectOptions{
		GenerateOptions: qiniudownloader.GenerateOptions{
			BucketName: c.bucket,
		},
	})
	if err != nil {
		return nil, err
	}
	if checksum != "" {
		got, err := fileSHA256(fileName)
		if err != nil {
			return nil, err
		}
		if got != checksum {
			return nil, fmt.Errorf("kodo artifact checksum = %s, want %s", got, checksum)
		}
	}
	if err := os.RemoveAll(installDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return nil, err
	}
	return archiver.Unpack(fileName, installDir)
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
