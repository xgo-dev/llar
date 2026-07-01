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

	"github.com/goplus/llar/internal/artifact"
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
		result: artifact.Result{
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
	wantOptions := artifact.Options{
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
	if store.putCalls != 1 {
		t.Fatalf("store Put calls = %d, want 1", store.putCalls)
	}
	if store.getOrUpdateCalls != 1 {
		t.Fatalf("store GetOrUpdate calls = %d, want 1", store.getOrUpdateCalls)
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

func TestArtifactCachePutReturnsExistingArtifactWithoutUploading(t *testing.T) {
	outputDir := t.TempDir()
	store := &fakeArtifactStore{
		putReturn: artifact.Artifact{
			Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/test/lib/blobs/sha256:existing"},
			Type:     "tar.gz",
			Metadata: "-lexisting",
			Checksum: "existing",
		},
	}
	uploader := &fakeArtifactUploader{
		result: artifact.Result{URL: "unused", Checksum: "unused"},
	}
	cache := NewArtifactCache(ArtifactCacheOptions{
		Store:    store,
		Uploader: uploader,
	})
	key := CacheKey{
		Module: module.Version{Path: "test/lib", Version: "1.0.0"},
		Matrix: "amd64-linux",
	}

	got, err := cache.Put(context.Background(), key, outputDir, CacheEntry{Metadata: "-lfoo"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got.Metadata != "-lexisting" {
		t.Fatalf("metadata = %q, want -lexisting", got.Metadata)
	}
	if uploader.payload != nil {
		t.Fatalf("uploader payload = %q, want nil", uploader.payload)
	}
	if store.getOrUpdateCalls != 0 {
		t.Fatalf("store GetOrUpdate calls = %d, want 0", store.getOrUpdateCalls)
	}
}

func TestArtifactCachePutOmitsAttrsForNonGHCRUploader(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "libfoo.a"), []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeArtifactStore{}
	uploader := &fakeArtifactUploader{
		typ:    "file",
		result: artifact.Result{URL: "file:///tmp/libfoo.tar.gz", Checksum: "abc"},
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

	if _, err := cache.Put(context.Background(), key, outputDir, CacheEntry{Metadata: "-lfoo"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if uploader.options.Attrs != nil {
		t.Fatalf("upload attrs = %+v, want nil", uploader.options.Attrs)
	}
	if store.value.Source.Type != "file" {
		t.Fatalf("source type = %q, want file", store.value.Source.Type)
	}
}

func TestArtifactCacheGetDownloadsAndUnpacksArtifact(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "lib", "libfoo.a"), []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "artifact.tar.gz")
	if err := writeTestTarGz(src, archivePath); err != nil {
		t.Fatalf("write test artifact: %v", err)
	}
	body, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeArtifactStore{
		getOK: true,
		getValue: artifact.Artifact{
			Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/test/lib/blobs/sha256:abc"},
			Type:     "tar.gz",
			Metadata: "-lfoo",
			Checksum: "abc",
		},
	}
	downloader := &fakeArtifactDownloader{
		typ:  "ghcr",
		body: body,
	}
	cache := NewArtifactCache(ArtifactCacheOptions{
		Store:      store,
		Downloader: downloader,
	})
	outputDir := t.TempDir()
	key := CacheKey{
		Module: module.Version{Path: "test/lib", Version: "1.0.0"},
		Matrix: "amd64-linux",
	}

	got, ok, err := cache.Get(context.Background(), key, outputDir)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get ok = false, want true")
	}
	if got.Metadata != "-lfoo" {
		t.Fatalf("metadata = %q, want -lfoo", got.Metadata)
	}
	if downloader.source != store.getValue.Source {
		t.Fatalf("download source = %+v, want %+v", downloader.source, store.getValue.Source)
	}
	if downloader.checksum != "abc" {
		t.Fatalf("download checksum = %q, want abc", downloader.checksum)
	}
	if data, err := os.ReadFile(filepath.Join(outputDir, "lib", "libfoo.a")); err != nil {
		t.Fatal(err)
	} else if string(data) != "archive" {
		t.Fatalf("unpacked libfoo.a = %q, want archive", data)
	}
}

type fakeArtifactStore struct {
	getOK            bool
	getValue         artifact.Artifact
	putReturn        artifact.Artifact
	value            artifact.Artifact
	putCalls         int
	getOrUpdateCalls int
}

func (s *fakeArtifactStore) Get(context.Context, artifact.Key) (artifact.Artifact, bool, error) {
	return s.getValue, s.getOK, nil
}

func (s *fakeArtifactStore) Put(ctx context.Context, key artifact.Key, value artifact.Artifact) (artifact.Artifact, error) {
	s.putCalls++
	if s.putReturn != (artifact.Artifact{}) {
		return s.putReturn, nil
	}
	return value, nil
}

func (s *fakeArtifactStore) GetOrUpdate(ctx context.Context, key artifact.Key, update func() (artifact.Artifact, error)) (artifact.Artifact, error) {
	s.getOrUpdateCalls++
	value, err := update()
	if err != nil {
		return artifact.Artifact{}, err
	}
	s.value = value
	return value, nil
}

func (s *fakeArtifactStore) Delete(context.Context, artifact.Key) error {
	return nil
}

type fakeArtifactUploader struct {
	typ     string
	result  artifact.Result
	options artifact.Options
	payload []byte
}

func (u *fakeArtifactUploader) Type() string {
	if u.typ != "" {
		return u.typ
	}
	return "ghcr"
}

func (u *fakeArtifactUploader) Upload(ctx context.Context, r io.ReadSeeker, opts artifact.Options) (artifact.Result, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return artifact.Result{}, err
	}
	u.options = opts
	u.payload = payload
	return u.result, nil
}

type fakeArtifactDownloader struct {
	typ      string
	source   artifact.Source
	checksum string
	body     []byte
}

func (d *fakeArtifactDownloader) Type() string {
	return d.typ
}

func (d *fakeArtifactDownloader) Download(ctx context.Context, source artifact.Source, checksum string) ([]byte, error) {
	d.source = source
	d.checksum = checksum
	return d.body, nil
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

func writeTestTarGz(srcDir, dst string) error {
	file, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
}
