package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

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
