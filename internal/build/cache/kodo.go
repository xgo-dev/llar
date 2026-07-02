package cache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	qiniuclient "github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storagev2/credentials"
	httpclient "github.com/qiniu/go-sdk/v7/storagev2/http_client"
	"github.com/qiniu/go-sdk/v7/storagev2/objects"
	"github.com/qiniu/go-sdk/v7/storagev2/uploader"
	"github.com/qiniu/go-sdk/v7/storagev2/uptoken"
)

const kodoEntryMetadataKey = "llar-entry"

type KodoConfig struct {
	AccessKey string
	SecretKey string
	Bucket    string
	Prefix    string
}

type kodoCache struct {
	bucket      string
	prefix      string
	credentials *credentials.Credentials
	objects     *objects.ObjectsManager
	uploader    *uploader.UploadManager
}

func NewKodo(cfg KodoConfig) Cache {
	cred := credentials.NewCredentials(cfg.AccessKey, cfg.SecretKey)
	options := httpclient.Options{Credentials: cred}
	return &kodoCache{
		bucket:      cfg.Bucket,
		prefix:      strings.Trim(cfg.Prefix, "/"),
		credentials: cred,
		objects: objects.NewObjectsManager(&objects.ObjectsManagerOptions{
			Options: options,
		}),
		uploader: uploader.NewUploadManager(&uploader.UploadManagerOptions{
			Options: options,
		}),
	}
}

func (c *kodoCache) Get(ctx context.Context, key Key) (Entry, bool, error) {
	objectName := c.objectName(key)
	object, err := c.objects.Bucket(c.bucket).Object(objectName).Stat().Call(ctx)
	if err != nil {
		if isKodoObjectNotFound(err) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	entry, ok := kodoEntryFromMetadata(object.Metadata)
	return entry, ok, nil
}

func (c *kodoCache) Put(ctx context.Context, key Key, output fs.FS, entry Entry) (Entry, error) {
	entryMetadata, err := encodeKodoEntry(entry)
	if err != nil {
		return Entry{}, err
	}

	objectName := c.objectName(key)
	putPolicy, err := uptoken.NewPutPolicyWithKey(c.bucket, objectName, time.Now().Add(time.Hour))
	if err != nil {
		return Entry{}, err
	}

	reader, writer := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := writeTarGzip(writer, output)
		_ = writer.CloseWithError(err)
		errc <- err
	}()

	err = c.uploader.UploadReader(ctx, reader, &uploader.ObjectOptions{
		BucketName:  c.bucket,
		ObjectName:  &objectName,
		FileName:    path.Base(objectName),
		ContentType: "application/gzip",
		UpToken:     uptoken.NewSigner(putPolicy, c.credentials),
		Metadata: map[string]string{
			kodoEntryMetadataKey: entryMetadata,
		},
	}, nil)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-errc
		return Entry{}, err
	}
	if err := <-errc; err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (c *kodoCache) objectName(key Key) string {
	parts := make([]string, 0, 3)
	if c.prefix != "" {
		parts = append(parts, c.prefix)
	}
	parts = append(parts, strings.Trim(key.Module.Path, "/"), key.Matrix+".tar.gz")
	return strings.Join(parts, "/")
}

func encodeKodoEntry(entry Entry) (string, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func kodoEntryFromMetadata(metadata map[string]string) (Entry, bool) {
	raw := metadata[kodoEntryMetadataKey]
	if raw == "" {
		raw = metadata["x-qn-meta-"+kodoEntryMetadataKey]
	}
	if raw == "" {
		return Entry{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Entry{}, false
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, false
	}
	return entry, true
}

func isKodoObjectNotFound(err error) bool {
	var info *qiniuclient.ErrorInfo
	return errors.As(err, &info) && info.Code == 612
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
