package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	artifact "github.com/goplus/llar/internal/artfact"
	"github.com/goplus/llar/internal/upload"
	"github.com/goplus/llar/mod/module"
)

func TestArtifactCachePutUploadsAndStoresArtifact(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outputDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "lib", "libfoo.a"), []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeArtifactStore{}
	uploader := &fakeArtifactUploader{
		result: upload.Result{
			URL:      "https://ghcr.io/v2/meteorsliu/test/lib/blobs/sha256:abc",
			Checksum: "abc",
		},
	}
	cache := NewArtifactCache(ArtifactCacheOptions{
		Store:    store,
		Uploader: uploader,
		Attrs: map[string]string{
			"os":   "linux",
			"arch": "amd64",
		},
	})
	key := CacheKey{
		Module: module.Version{Path: "test/lib", Version: "1.0.0"},
		Matrix: "amd64-linux",
	}

	got, err := cache.Put(context.Background(), key, outputDir, CacheEntry{
		Metadata: "-lfoo",
		Deps:     []module.Version{{Path: "test/dep", Version: "1.2.3"}},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if got.Metadata != "-lfoo" {
		t.Fatalf("metadata = %q, want -lfoo", got.Metadata)
	}
	wantOptions := upload.Options{
		Name: "test/lib",
		Tag:  "1.0.0",
		Type: "tar.gz",
		Attrs: map[string]string{
			"org.llar.matrix": "amd64-linux",
			"os":              "linux",
			"arch":            "amd64",
		},
	}
	if !reflect.DeepEqual(uploader.options, wantOptions) {
		t.Fatalf("upload options = %+v, want %+v", uploader.options, wantOptions)
	}
	wantArtifact := artifact.Artifact{
		Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/test/lib/blobs/sha256:abc"},
		Type:     "tar.gz",
		Metadata: "-lfoo",
		Checksum: "abc",
	}
	if store.value != wantArtifact {
		t.Fatalf("stored artifact = %+v, want %+v", store.value, wantArtifact)
	}
	files := readArtifactTarGz(t, uploader.payload)
	if string(files["lib/libfoo.a"]) != "archive" {
		t.Fatalf("artifact lib/libfoo.a = %q, want archive", files["lib/libfoo.a"])
	}
	var metadata artifactMetadata
	if err := json.Unmarshal(files[".llar/metadata.json"], &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata.Metadata != "-lfoo" {
		t.Fatalf("artifact metadata = %q, want -lfoo", metadata.Metadata)
	}
	if !reflect.DeepEqual(metadata.Deps, []string{"test/dep@1.2.3"}) {
		t.Fatalf("artifact deps = %+v, want test/dep@1.2.3", metadata.Deps)
	}
}

func TestParseGHCRBlobURL(t *testing.T) {
	repo, digest, err := parseGHCRBlobURL("https://ghcr.io/v2/owner/pkg/blobs/sha256:abc")
	if err != nil {
		t.Fatalf("parseGHCRBlobURL: %v", err)
	}
	if repo != "owner/pkg" || digest != "abc" {
		t.Fatalf("repo,digest = %q,%q; want owner/pkg,abc", repo, digest)
	}
}

type fakeArtifactStore struct {
	value artifact.Artifact
}

func (s *fakeArtifactStore) Get(context.Context, artifact.Key) (artifact.Artifact, bool, error) {
	return artifact.Artifact{}, false, nil
}

func (s *fakeArtifactStore) Put(ctx context.Context, key artifact.Key, value artifact.Artifact) (artifact.Artifact, error) {
	s.value = value
	return value, nil
}

func (s *fakeArtifactStore) Delete(context.Context, artifact.Key) error {
	return nil
}

type fakeArtifactUploader struct {
	result  upload.Result
	options upload.Options
	payload []byte
}

func (u *fakeArtifactUploader) Type() string {
	return "ghcr"
}

func (u *fakeArtifactUploader) Upload(ctx context.Context, r io.ReadSeeker, opts upload.Options) (upload.Result, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return upload.Result{}, err
	}
	u.options = opts
	u.payload = payload
	return u.result, nil
}

func readArtifactTarGz(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		files[header.Name] = data
	}
}
