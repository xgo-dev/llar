package artifact

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qiniuclient "github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storagev2/apis/stat_object"
	"github.com/qiniu/go-sdk/v7/storagev2/credentials"
	httpclient "github.com/qiniu/go-sdk/v7/storagev2/http_client"
	"github.com/qiniu/go-sdk/v7/storagev2/objects"
	"github.com/qiniu/go-sdk/v7/storagev2/region"
	"github.com/qiniu/go-sdk/v7/storagev2/uploader"
	"github.com/qiniu/go-sdk/v7/storagev2/uptoken"
)

func TestKodoArtifactObjectName(t *testing.T) {
	store := NewKodoArtifact(KodoArtifactConfig{Prefix: "/cache/"}).(*kodoArtifact)
	key := Key{Module: "madler/zlib", Version: "v1.3.2", MatrixStr: "amd64-linux"}
	if got, want := store.objectName(key), "cache/madler/zlib/v1.3.2/amd64-linux.tar.gz"; got != want {
		t.Fatalf("object name = %q, want %q", got, want)
	}
}

func TestKodoArtifactMetadataRoundTrip(t *testing.T) {
	want := Artifact{
		Source:   Source{Type: "kodo", URL: "https://example.com/cache/madler/zlib/v1.3.2/amd64-linux.tar.gz"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "abc",
	}
	raw, err := encodeKodoArtifact(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, metadata := range []map[string]string{
		{kodoArtifactMetadataKey: raw},
		{"x-qn-meta-" + kodoArtifactMetadataKey: raw},
	} {
		got, ok := kodoArtifactFromMetadata(metadata)
		if !ok {
			t.Fatalf("decode missed metadata %+v", metadata)
		}
		if got != want {
			t.Fatalf("decode = %+v, want %+v", got, want)
		}
	}
}

func TestKodoArtifactMetadataInvalid(t *testing.T) {
	for _, metadata := range []map[string]string{
		{},
		{kodoArtifactMetadataKey: "not-base64"},
		{kodoArtifactMetadataKey: base64.RawURLEncoding.EncodeToString([]byte("{"))},
	} {
		if got, ok := kodoArtifactFromMetadata(metadata); ok {
			t.Fatalf("decode %+v = %+v, true; want false", metadata, got)
		}
	}
}

func TestKodoArtifactObjectNotFound(t *testing.T) {
	if !kodoArtifactObjectNotFound(&qiniuclient.ErrorInfo{Code: 612}) {
		t.Fatal("612 should be object not found")
	}
	if kodoArtifactObjectNotFound(&qiniuclient.ErrorInfo{Code: 500}) {
		t.Fatal("500 should not be object not found")
	}
	if kodoArtifactObjectNotFound(context.Canceled) {
		t.Fatal("context.Canceled should not be object not found")
	}
}

func TestKodoArtifactGetPutDeleteWithFakeKodo(t *testing.T) {
	key := Key{Module: "madler/zlib", Version: "v1.3.2", MatrixStr: "amd64-linux"}
	want := Artifact{
		Source:   Source{Type: "kodo", URL: "https://example.com/cache/madler/zlib/v1.3.2/amd64-linux.tar.gz"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "abc",
	}
	objectName := "cache/madler/zlib/v1.3.2/amd64-linux.tar.gz"
	server := newFakeKodoObjectServer(t, "bucket", objectName, nil)
	defer server.Close()
	store := newFakeKodoArtifactStore(server.URL, "bucket", "cache")

	got, err := store.Put(context.Background(), key, want)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got != want {
		t.Fatalf("Put = %+v, want %+v", got, want)
	}

	got, ok, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get missed object")
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}

	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestKodoArtifactGetErrors(t *testing.T) {
	key := Key{Module: "madler/zlib", Version: "v1.3.2", MatrixStr: "amd64-linux"}

	t.Run("not found", func(t *testing.T) {
		server := newFakeKodoObjectServer(t, "bucket", "other", nil)
		defer server.Close()
		store := newFakeKodoArtifactStore(server.URL, "bucket", "")

		got, ok, err := store.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if ok {
			t.Fatalf("Get = %+v, true; want miss", got)
		}
	})

	t.Run("missing metadata", func(t *testing.T) {
		objectName := "madler/zlib/v1.3.2/amd64-linux.tar.gz"
		server := newFakeKodoObjectServer(t, "bucket", objectName, nil)
		defer server.Close()
		store := newFakeKodoArtifactStore(server.URL, "bucket", "")

		if got, ok, err := store.Get(context.Background(), key); err == nil {
			t.Fatalf("Get = %+v, %v, nil; want metadata error", got, ok)
		}
	})
}

func TestKodoArtifactPutDeleteErrors(t *testing.T) {
	key := Key{Module: "madler/zlib", Version: "v1.3.2", MatrixStr: "amd64-linux"}
	value := Artifact{Source: Source{Type: "kodo", URL: "https://example.com"}, Type: "tar.gz"}

	t.Run("put missing object", func(t *testing.T) {
		server := newFakeKodoObjectServer(t, "bucket", "other", nil)
		defer server.Close()
		store := newFakeKodoArtifactStore(server.URL, "bucket", "")

		if got, err := store.Put(context.Background(), key, value); err == nil {
			t.Fatalf("Put = %+v, nil; want missing object error", got)
		}
	})

	t.Run("delete missing object", func(t *testing.T) {
		server := newFakeKodoObjectServer(t, "bucket", "other", nil)
		defer server.Close()
		store := newFakeKodoArtifactStore(server.URL, "bucket", "")

		if err := store.Delete(context.Background(), key); err != nil {
			t.Fatalf("Delete missing object: %v", err)
		}
	})

	t.Run("delete server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		}))
		defer server.Close()
		store := newFakeKodoArtifactStore(server.URL, "bucket", "")

		if err := store.Delete(context.Background(), key); err == nil {
			t.Fatal("Delete server error = nil, want error")
		}
	})
}

func TestKodoArtifactE2E(t *testing.T) {
	accessKey := os.Getenv("QINIU_ACCESS_KEY")
	secretKey := os.Getenv("QINIU_SECRET_KEY")
	bucket := os.Getenv("QINIU_BUCKET")
	if accessKey == "" || secretKey == "" || bucket == "" {
		t.Skip("QINIU_ACCESS_KEY, QINIU_SECRET_KEY, and QINIU_BUCKET are required")
	}

	prefix := strings.Trim(os.Getenv("QINIU_PREFIX"), "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += fmt.Sprintf("llar-kodo-artifact-e2e/%d", time.Now().UnixNano())
	store := NewKodoArtifact(KodoArtifactConfig{
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		Prefix:    prefix,
	}).(*kodoArtifact)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	key := Key{Module: "madler/zlib", Version: "v1.3.2", MatrixStr: "amd64-linux"}
	objectName := store.objectName(key)
	want := Artifact{
		Source:   Source{Type: "kodo", URL: "https://example.com/" + objectName},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "sha256-test",
	}
	if err := uploadKodoArtifactObject(ctx, t, accessKey, secretKey, bucket, objectName); err != nil {
		t.Fatalf("upload object: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := store.Delete(cleanupCtx, key); err != nil {
			t.Errorf("delete object: %v", err)
		}
	})

	got, err := store.Put(ctx, key, want)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got != want {
		t.Fatalf("Put = %+v, want %+v", got, want)
	}

	got, ok, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get missed uploaded object")
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
}

func newFakeKodoArtifactStore(rsURL, bucket, prefix string) *kodoArtifact {
	cred := credentials.NewCredentials("testak", "testsk")
	return &kodoArtifact{
		bucket: bucket,
		prefix: strings.Trim(prefix, "/"),
		objects: objects.NewObjectsManager(&objects.ObjectsManagerOptions{
			Options: httpclient.Options{
				Credentials: cred,
				Regions:     &region.Region{Rs: region.Endpoints{Preferred: []string{rsURL}}},
			},
		}),
	}
}

func newFakeKodoObjectServer(t *testing.T, bucket, objectName string, metadata map[string]string) *httptest.Server {
	t.Helper()

	entry := base64.URLEncoding.EncodeToString([]byte(bucket + ":" + objectName))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Reqid", "fake-reqid")
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/stat/"+entry:
			response := stat_object.Response{
				Hash:     "etag",
				MimeType: "application/gzip",
				PutTime:  time.Now().UnixNano() / 100,
				Metadata: metadata,
			}
			data, err := json.Marshal(&response)
			if err != nil {
				t.Fatalf("marshal stat response: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(data)
		case r.Method == http.MethodPost && r.URL.RequestURI() == "/delete/"+entry:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/chgm/"+entry+"/mime/"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/chgm/"+entry+"/mime/"), "/")
			if len(parts) < 3 || len(parts)%2 != 1 {
				t.Fatalf("unexpected chgm path: %s", r.URL.Path)
			}
			if metadata == nil {
				metadata = map[string]string{}
			}
			for i := 1; i < len(parts); i += 2 {
				value, err := base64.URLEncoding.DecodeString(parts[i+1])
				if err != nil {
					t.Fatalf("decode metadata value: %v", err)
				}
				metadata[parts[i]] = string(value)
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/stat/"), strings.HasPrefix(r.URL.Path, "/delete/"), strings.HasPrefix(r.URL.Path, "/chgm/"):
			w.WriteHeader(612)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.RequestURI())
		}
	}))
}

func uploadKodoArtifactObject(ctx context.Context, t *testing.T, accessKey, secretKey, bucket, objectName string) error {
	t.Helper()

	file := filepath.Join(t.TempDir(), "tar.gz")
	if err := os.WriteFile(file, []byte("artifact"), 0o644); err != nil {
		return err
	}
	cred := credentials.NewCredentials(accessKey, secretKey)
	options := httpclient.Options{Credentials: cred}
	manager := uploader.NewUploadManager(&uploader.UploadManagerOptions{Options: options})
	putPolicy, err := uptoken.NewPutPolicyWithKey(bucket, objectName, time.Now().Add(time.Hour))
	if err != nil {
		return err
	}
	putPolicy.SetInsertOnly(1)
	return manager.UploadFile(ctx, file, &uploader.ObjectOptions{
		BucketName:  bucket,
		ObjectName:  &objectName,
		FileName:    path.Base(objectName),
		ContentType: "application/gzip",
		UpToken:     uptoken.NewSigner(putPolicy, cred),
	}, nil)
}
