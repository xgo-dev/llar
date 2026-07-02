package main

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/goplus/llar/formula"
	artifact "github.com/goplus/llar/internal/artifact"
	artifactuploader "github.com/goplus/llar/internal/artifact/uploader"
)

func TestAssertArtifactRejectsChecksumURLMismatch(t *testing.T) {
	cfg := configData{ghcrOwner: "MeteorsLiu"}
	target := target{Module: "madler/zlib", Version: "v1.3.1"}
	got := artifact.Artifact{
		Source: artifact.Source{
			Type: "ghcr",
			URL:  "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}

	err := assertArtifact(context.Background(), cfg, target, formula.Matrix{}, got)
	if err == nil {
		t.Fatal("assertArtifact succeeded, want checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("assertArtifact error = %v, want checksum mismatch", err)
	}
}

func TestCountingUploaderRejectsArchiveChecksumMismatch(t *testing.T) {
	uploader := &countingUploader{
		inner: fakeUploader{
			result: artifactuploader.Result{
				URL:      "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Size:     3,
				Checksum: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		},
	}

	_, err := uploader.Upload(context.Background(), bytes.NewReader([]byte("abc")), artifactuploader.Options{})
	if err == nil {
		t.Fatal("Upload succeeded, want checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Upload error = %v, want checksum mismatch", err)
	}
}

func TestCountingUploaderForwardsSeed(t *testing.T) {
	inner := &fakeUploader{}
	uploader := &countingUploader{inner: inner}
	opts := artifactuploader.Options{Name: "madler/zlib", Tag: "v1.3.1"}

	if err := uploader.Seed(context.Background(), opts); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if !reflect.DeepEqual(inner.seedOptions, opts) {
		t.Fatalf("seed options = %+v, want %+v", inner.seedOptions, opts)
	}
}

type fakeUploader struct {
	result      artifactuploader.Result
	seedOptions artifactuploader.Options
}

func (u fakeUploader) Type() string {
	return "ghcr"
}

func (u *fakeUploader) Seed(ctx context.Context, opts artifactuploader.Options) error {
	u.seedOptions = opts
	return nil
}

func (u fakeUploader) Upload(context.Context, io.ReadSeeker, artifactuploader.Options) (artifactuploader.Result, error) {
	return u.result, nil
}
