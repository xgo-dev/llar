package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

var errTestUpload = errors.New("test upload error")

func TestNewGHCRReturnsGHCRUploader(t *testing.T) {
	uploader := NewGHCR(GHCRConfig{Owner: "MeteorsLiu", Username: "MeteorsLiu", Token: "token"})
	if uploader.Type() != "ghcr" {
		t.Fatalf("Type = %q, want ghcr", uploader.Type())
	}
	got, ok := uploader.(ghcrUploader)
	if !ok {
		t.Fatalf("NewGHCR returned %T, want ghcrUploader", uploader)
	}
	if got.cfg.Owner != "MeteorsLiu" || got.cfg.Username != "MeteorsLiu" || got.cfg.Token != "token" {
		t.Fatalf("config = %+v", got.cfg)
	}
	if got.writeIndex == nil {
		t.Fatal("writeIndex is nil")
	}
}

func TestChecksumResultReadsFromCurrentOffsetAndRestoresReader(t *testing.T) {
	r := bytes.NewReader([]byte("prefixartifact-bytes"))
	if _, err := r.Seek(int64(len("prefix")), io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	got, err := checksumResult(r)
	if err != nil {
		t.Fatalf("checksumResult: %v", err)
	}

	payload := []byte("artifact-bytes")
	sum := sha256.Sum256(payload)
	if got.Size != int64(len(payload)) {
		t.Fatalf("Size = %d, want %d", got.Size, len(payload))
	}
	if got.Checksum != hex.EncodeToString(sum[:]) {
		t.Fatalf("Checksum = %q", got.Checksum)
	}
	offset, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek current: %v", err)
	}
	if offset != int64(len("prefix")) {
		t.Fatalf("offset = %d, want %d", offset, len("prefix"))
	}
}

func TestChecksumResultReportsReaderErrors(t *testing.T) {
	tests := []struct {
		name string
		r    io.ReadSeeker
	}{
		{name: "initial seek", r: seekErrorReader{}},
		{name: "read", r: readErrorSeeker{}},
		{name: "restore seek", r: &secondSeekErrorReader{Reader: bytes.NewReader([]byte("archive"))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := checksumResult(tt.r); !errors.Is(err, errTestUpload) {
				t.Fatalf("checksumResult error = %v, want %v", err, errTestUpload)
			}
		})
	}
}

func TestGHCRUploaderWritesOCIIndexWithArtifactLayer(t *testing.T) {
	payload := []byte("archive-bytes")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	writer := &recordingIndexWriter{}
	r := bytes.NewReader(append([]byte("skip"), payload...))
	if _, err := r.Seek(int64(len("skip")), io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	uploader := ghcrUploader{
		cfg:        GHCRConfig{Owner: "example", Token: "publish-token"},
		writeIndex: writer.write,
	}
	got, err := uploader.Upload(context.Background(), r, Options{
		Name: "ghcr.io/example/madler/zlib:v1.3.1",
		Type: "tar.gz",
		Attrs: map[string]string{
			"org.llar.matrix": "amd64-linux",
			"os":              "linux",
			"arch":            "amd64",
		},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if writer.ref != "ghcr.io/example/madler/zlib:v1.3.1" {
		t.Fatalf("ref = %q", writer.ref)
	}
	if writer.index == nil {
		t.Fatalf("index was not written")
	}
	manifest, err := writer.index.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	if manifest.MediaType != types.OCIImageIndex {
		t.Fatalf("index media type = %q", manifest.MediaType)
	}
	if len(manifest.Manifests) != 1 {
		t.Fatalf("manifests = %+v", manifest.Manifests)
	}
	entry := manifest.Manifests[0]
	if entry.Annotations["org.llar.matrix"] != "amd64-linux" {
		t.Fatalf("annotations = %+v", entry.Annotations)
	}
	if entry.Platform == nil || entry.Platform.OS != "linux" || entry.Platform.Architecture != "amd64" {
		t.Fatalf("platform = %+v", entry.Platform)
	}

	img, err := writer.index.Image(entry.Digest)
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	imgManifest, err := img.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if imgManifest.MediaType != types.OCIManifestSchema1 {
		t.Fatalf("image manifest media type = %q", imgManifest.MediaType)
	}
	if len(imgManifest.Layers) != 1 {
		t.Fatalf("layers = %+v", imgManifest.Layers)
	}
	layer := imgManifest.Layers[0]
	if layer.MediaType != types.OCILayer {
		t.Fatalf("layer media type = %q", layer.MediaType)
	}
	if layer.Digest.String() != "sha256:"+digest {
		t.Fatalf("layer digest = %q", layer.Digest)
	}

	if got.URL != "https://ghcr.io/v2/example/madler/zlib/blobs/sha256:"+digest {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Size != int64(len(payload)) {
		t.Fatalf("Size = %d", got.Size)
	}
	if got.Checksum != digest {
		t.Fatalf("Checksum = %q", got.Checksum)
	}
	offset, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek current: %v", err)
	}
	if offset != int64(len("skip")) {
		t.Fatalf("offset = %d, want %d", offset, len("skip"))
	}
}

func TestGHCRUploaderWritesZstdLayerByDefaultingOwner(t *testing.T) {
	writer := &recordingIndexWriter{}
	uploader := ghcrUploader{
		cfg:        GHCRConfig{Owner: "MeteorsLiu", Username: "MeteorsLiu", Token: "publish-token"},
		writeIndex: writer.write,
	}

	_, err := uploader.Upload(context.Background(), bytes.NewReader([]byte("archive")), Options{
		Name: "llar:test",
		Type: "tar.zst",
		Attrs: map[string]string{
			"org.llar.matrix": "arm64-darwin",
		},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if writer.ref != "ghcr.io/meteorsliu/llar:test" {
		t.Fatalf("ref = %q", writer.ref)
	}
	manifest, err := writer.index.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	img, err := writer.index.Image(manifest.Manifests[0].Digest)
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	imgManifest, err := img.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if got := imgManifest.Layers[0].MediaType; got != types.OCILayerZStd {
		t.Fatalf("layer media type = %q, want %q", got, types.OCILayerZStd)
	}
}

func TestGHCRUploaderReportsUploadErrors(t *testing.T) {
	tests := []struct {
		name string
		r    io.ReadSeeker
		opts Options
		u    ghcrUploader
	}{
		{
			name: "invalid name",
			r:    bytes.NewReader([]byte("archive")),
			opts: Options{Name: "MeteorsLiu/llar"},
			u:    ghcrUploader{cfg: GHCRConfig{Owner: "MeteorsLiu"}, writeIndex: (&recordingIndexWriter{}).write},
		},
		{
			name: "unsupported archive type",
			r:    bytes.NewReader([]byte("archive")),
			opts: Options{Name: "MeteorsLiu/llar:test", Type: "zip"},
			u:    ghcrUploader{cfg: GHCRConfig{Owner: "MeteorsLiu"}, writeIndex: (&recordingIndexWriter{}).write},
		},
		{
			name: "initial seek",
			r:    seekErrorReader{},
			opts: Options{Name: "MeteorsLiu/llar:test"},
			u:    ghcrUploader{cfg: GHCRConfig{Owner: "MeteorsLiu"}, writeIndex: (&recordingIndexWriter{}).write},
		},
		{
			name: "read",
			r:    readErrorSeeker{},
			opts: Options{Name: "MeteorsLiu/llar:test"},
			u:    ghcrUploader{cfg: GHCRConfig{Owner: "MeteorsLiu"}, writeIndex: (&recordingIndexWriter{}).write},
		},
		{
			name: "restore seek",
			r:    &secondSeekErrorReader{Reader: bytes.NewReader([]byte("archive"))},
			opts: Options{Name: "MeteorsLiu/llar:test"},
			u:    ghcrUploader{cfg: GHCRConfig{Owner: "MeteorsLiu"}, writeIndex: (&recordingIndexWriter{}).write},
		},
		{
			name: "writer",
			r:    bytes.NewReader([]byte("archive")),
			opts: Options{Name: "MeteorsLiu/llar:test"},
			u: ghcrUploader{
				cfg: GHCRConfig{Owner: "MeteorsLiu"},
				writeIndex: func(context.Context, string, v1.ImageIndex, string, string) error {
					return errTestUpload
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.u.Upload(context.Background(), tt.r, tt.opts); err == nil {
				t.Fatal("Upload error = nil, want error")
			}
		})
	}
}

func TestGHCRUploaderAcceptsGitHubStyleOwnerCase(t *testing.T) {
	writer := &recordingIndexWriter{}
	uploader := ghcrUploader{
		cfg:        GHCRConfig{Owner: "MeteorsLiu", Token: "publish-token"},
		writeIndex: writer.write,
	}

	got, err := uploader.Upload(context.Background(), bytes.NewReader([]byte("archive")), Options{
		Name: "MeteorsLiu/llar:test",
		Type: "tar.gz",
		Attrs: map[string]string{
			"org.llar.matrix": "amd64-linux",
		},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if writer.ref != "ghcr.io/meteorsliu/llar:test" {
		t.Fatalf("ref = %q", writer.ref)
	}
	if got.URL != "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:"+got.Checksum {
		t.Fatalf("URL = %q", got.URL)
	}
}

func TestGHCRUploaderPassesConfiguredUsernameToWriter(t *testing.T) {
	writer := &recordingIndexWriter{}
	uploader := ghcrUploader{
		cfg: GHCRConfig{
			Owner:    "MeteorsLiu",
			Username: "MeteorsLiu",
			Token:    "publish-token",
		},
		writeIndex: writer.write,
	}

	if _, err := uploader.Upload(context.Background(), bytes.NewReader([]byte("archive")), Options{
		Name: "MeteorsLiu/llar:test",
		Type: "tar.gz",
	}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if writer.username != "MeteorsLiu" {
		t.Fatalf("username = %q, want MeteorsLiu", writer.username)
	}
	if writer.token != "publish-token" {
		t.Fatalf("token = %q, want publish-token", writer.token)
	}
}

func TestParseGHCRName(t *testing.T) {
	tests := []struct {
		name    string
		rawName string
		owner   string
		want    ghcrRef
	}{
		{
			name:    "trims registry prefix",
			rawName: "ghcr.io/MeteorsLiu/llar:test",
			owner:   "MeteorsLiu",
			want:    ghcrRef{repo: "meteorsliu/llar", tag: "test"},
		},
		{
			name:    "adds owner",
			rawName: "llar:test",
			owner:   "/MeteorsLiu/",
			want:    ghcrRef{repo: "meteorsliu/llar", tag: "test"},
		},
		{
			name:    "keeps nested owner repo",
			rawName: "MeteorsLiu/madler/zlib:v1.3.1",
			owner:   "MeteorsLiu",
			want:    ghcrRef{repo: "meteorsliu/madler/zlib", tag: "v1.3.1"},
		},
		{
			name:    "allows empty owner",
			rawName: "madler/zlib:v1.3.1",
			want:    ghcrRef{repo: "madler/zlib", tag: "v1.3.1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGHCRName(tt.rawName, tt.owner)
			if err != nil {
				t.Fatalf("parseGHCRName: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseGHCRName = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseGHCRNameRejectsInvalidNames(t *testing.T) {
	tests := []struct {
		rawName string
		owner   string
	}{
		{rawName: ""},
		{rawName: "MeteorsLiu/llar"},
		{rawName: "MeteorsLiu/llar:"},
		{rawName: ":test"},
	}
	for _, tt := range tests {
		t.Run(tt.rawName, func(t *testing.T) {
			if _, err := parseGHCRName(tt.rawName, tt.owner); err == nil {
				t.Fatal("parseGHCRName error = nil, want error")
			}
		})
	}
}

func TestLayerMediaTypeRejectsUnsupportedArchive(t *testing.T) {
	if _, err := layerMediaType("zip"); err == nil || !strings.Contains(err.Error(), "unsupported ghcr archive type") {
		t.Fatalf("layerMediaType error = %v, want unsupported archive type", err)
	}
}

func TestWriteRemoteIndexValidatesReference(t *testing.T) {
	err := writeRemoteIndex(context.Background(), "::not-a-tag", empty.Index, "MeteorsLiu", "token")
	if err == nil {
		t.Fatal("writeRemoteIndex error = nil, want invalid tag error")
	}
}

func TestWriteRemoteIndexHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := writeRemoteIndex(ctx, "ghcr.io/meteorsliu/llar:test", empty.Index, "MeteorsLiu", "token")
	if err == nil {
		t.Fatal("writeRemoteIndex error = nil, want canceled context error")
	}
}

func TestPlatformFromAttrsAllowsPartialPlatform(t *testing.T) {
	got := platformFromAttrs(map[string]string{"os": "linux"})
	if got == nil || got.OS != "linux" || got.Architecture != "" {
		t.Fatalf("platform with only os = %+v", got)
	}
	got = platformFromAttrs(map[string]string{"arch": "amd64"})
	if got == nil || got.OS != "" || got.Architecture != "amd64" {
		t.Fatalf("platform with only arch = %+v", got)
	}
	if got := platformFromAttrs(nil); got != nil {
		t.Fatalf("empty platform = %+v, want nil", got)
	}
	got = platformFromAttrs(map[string]string{"os": "linux", "arch": "amd64"})
	if got == nil || got.OS != "linux" || got.Architecture != "amd64" {
		t.Fatalf("platform = %+v", got)
	}
}

type recordingIndexWriter struct {
	ref      string
	index    v1.ImageIndex
	username string
	token    string
}

func (w *recordingIndexWriter) write(ctx context.Context, ref string, index v1.ImageIndex, username, token string) error {
	w.ref = ref
	w.index = index
	w.username = username
	w.token = token
	return nil
}

type seekErrorReader struct{}

func (seekErrorReader) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (seekErrorReader) Seek(int64, int) (int64, error) {
	return 0, errTestUpload
}

type readErrorSeeker struct{}

func (readErrorSeeker) Read([]byte) (int, error) {
	return 0, errTestUpload
}

func (readErrorSeeker) Seek(int64, int) (int64, error) {
	return 0, nil
}

type secondSeekErrorReader struct {
	*bytes.Reader
	seeks int
}

func (r *secondSeekErrorReader) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	if r.seeks == 2 {
		return 0, errTestUpload
	}
	return r.Reader.Seek(offset, whence)
}
