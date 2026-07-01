package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/goplus/llar/formula"
	artifact "github.com/goplus/llar/internal/artifact"
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
			result: artifact.Result{
				URL:      "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Size:     3,
				Checksum: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		},
	}

	_, err := uploader.Upload(context.Background(), bytes.NewReader([]byte("abc")), artifact.Options{})
	if err == nil {
		t.Fatal("Upload succeeded, want checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Upload error = %v, want checksum mismatch", err)
	}
}

type fakeUploader struct {
	result artifact.Result
}

func (u fakeUploader) Type() string {
	return "ghcr"
}

func (u fakeUploader) Upload(context.Context, io.ReadSeeker, artifact.Options) (artifact.Result, error) {
	return u.result, nil
}
