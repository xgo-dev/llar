package artifact

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	qiniuclient "github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storagev2/credentials"
	httpclient "github.com/qiniu/go-sdk/v7/storagev2/http_client"
	"github.com/qiniu/go-sdk/v7/storagev2/objects"
)

const (
	kodoArtifactContentType = "application/gzip"
	kodoArtifactMetadataKey = "llar-artifact"
)

type KodoArtifactConfig struct {
	AccessKey string
	SecretKey string
	Bucket    string
	Prefix    string
}

type kodoArtifact struct {
	bucket  string
	prefix  string
	objects *objects.ObjectsManager
}

func NewKodoArtifact(cfg KodoArtifactConfig) Store {
	cred := credentials.NewCredentials(cfg.AccessKey, cfg.SecretKey)
	return &kodoArtifact{
		bucket: cfg.Bucket,
		prefix: strings.Trim(cfg.Prefix, "/"),
		objects: objects.NewObjectsManager(&objects.ObjectsManagerOptions{
			Options: httpclient.Options{Credentials: cred},
		}),
	}
}

func (s *kodoArtifact) Get(ctx context.Context, key Key) (Artifact, error) {
	objectName := s.objectName(key)
	object, err := s.objects.Bucket(s.bucket).Object(objectName).Stat().Call(ctx)
	if err != nil {
		if kodoArtifactObjectNotFound(err) {
			return Artifact{}, ErrNotFound
		}
		return Artifact{}, err
	}
	got, ok := kodoArtifactFromMetadata(object.Metadata)
	if !ok {
		return Artifact{}, ErrNotFound
	}
	return got, nil
}

func (s *kodoArtifact) Put(ctx context.Context, key Key, art Artifact) (Artifact, error) {
	raw, err := encodeKodoArtifact(art)
	if err != nil {
		return art, err
	}
	err = s.objects.Bucket(s.bucket).Object(s.objectName(key)).SetMetadata(kodoArtifactContentType).
		Metadata(map[string]string{kodoArtifactMetadataKey: raw}).
		Call(ctx)
	if err != nil {
		return art, err
	}
	return art, nil
}

func (s *kodoArtifact) Delete(ctx context.Context, key Key) error {
	err := s.objects.Bucket(s.bucket).Object(s.objectName(key)).Delete().Call(ctx)
	if err != nil && !kodoArtifactObjectNotFound(err) {
		return err
	}
	return nil
}

func (s *kodoArtifact) objectName(key Key) string {
	parts := make([]string, 0, 4)
	if s.prefix != "" {
		parts = append(parts, s.prefix)
	}
	parts = append(parts, strings.Trim(key.Module, "/"), strings.Trim(key.Version, "/"), key.MatrixStr+".tar.gz")
	return strings.Join(parts, "/")
}

func encodeKodoArtifact(art Artifact) (string, error) {
	data, err := json.Marshal(art)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func kodoArtifactFromMetadata(metadata map[string]string) (Artifact, bool) {
	raw := kodoArtifactMetadataValue(metadata, kodoArtifactMetadataKey)
	if raw == "" {
		return Artifact{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Artifact{}, false
	}
	var art Artifact
	if err := json.Unmarshal(data, &art); err != nil {
		return Artifact{}, false
	}
	return art, true
}

func kodoArtifactMetadataValue(metadata map[string]string, key string) string {
	value := metadata[key]
	if value == "" {
		value = metadata["x-qn-meta-"+key]
	}
	return value
}

func kodoArtifactObjectNotFound(err error) bool {
	var info *qiniuclient.ErrorInfo
	return errors.As(err, &info) && info.Code == 612
}
