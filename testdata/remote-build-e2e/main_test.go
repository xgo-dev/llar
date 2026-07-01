package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/artifact"
	remotebuild "github.com/goplus/llar/internal/remote/build"
	"github.com/goplus/llar/internal/upload"
)

func TestAssertTargetArtifactRejectsChecksumURLMismatch(t *testing.T) {
	cfg := configData{ghcrOwner: "MeteorsLiu"}
	target := remotebuild.Target{Module: "madler/zlib", Version: "v1.3.1"}
	got := remotebuild.Result{
		TargetArtifact: remotebuild.TargetArtifact{
			Target: target.Module + "@" + target.Version,
			Artifact: artifact.Artifact{
				Source: artifact.Source{
					Type: "ghcr",
					URL:  "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				Type:     "tar.gz",
				Metadata: "-lz",
				Checksum: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		},
	}

	err := assertTargetArtifact(cfg, target, got)
	if err == nil {
		t.Fatal("assertTargetArtifact succeeded, want checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("assertTargetArtifact error = %v, want checksum mismatch", err)
	}
}

func TestCountingUploaderRejectsArchiveChecksumMismatch(t *testing.T) {
	uploader := &countingUploader{
		inner: fakeUploader{
			result: upload.Result{
				URL:      "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Size:     3,
				Checksum: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		},
	}

	_, err := uploader.Upload(context.Background(), bytes.NewReader([]byte("abc")), upload.Options{})
	if err == nil {
		t.Fatal("Upload succeeded, want checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Upload error = %v, want checksum mismatch", err)
	}
}

type fakeUploader struct {
	result upload.Result
}

func (u fakeUploader) Type() string {
	return "ghcr"
}

func (u fakeUploader) Upload(context.Context, io.ReadSeeker, upload.Options) (upload.Result, error) {
	return u.result, nil
}
