package downloader

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/goplus/llar/internal/artifact"
)

type GHCRConfig struct {
	Username string
	Token    string
}

func NewGHCR(cfg GHCRConfig) *GHCR {
	return &GHCR{cfg: cfg}
}

type GHCR struct {
	cfg GHCRConfig
}

func (d *GHCR) Type() string {
	return "ghcr"
}

func (d *GHCR) Download(ctx context.Context, source artifact.Source, checksum string) ([]byte, error) {
	if source.Type != "" && source.Type != d.Type() {
		return nil, fmt.Errorf("source type = %q, want %q", source.Type, d.Type())
	}
	repo, digest, err := parseGHCRBlobURL(source.URL)
	if err != nil {
		return nil, err
	}
	if checksum != "" && digest != checksum {
		return nil, fmt.Errorf("artifact source digest = %q, want checksum %q", digest, checksum)
	}
	ref, err := name.NewDigest("ghcr.io/"+repo+"@sha256:"+digest, name.WeakValidation)
	if err != nil {
		return nil, fmt.Errorf("GHCR blob ref: %w", err)
	}
	layer, err := remote.Layer(ref, ghcrRemoteOptions(ctx, d.cfg.Username, d.cfg.Token)...)
	if err != nil {
		return nil, fmt.Errorf("read GHCR blob %s: %w", ref.String(), err)
	}
	rc, err := layer.Compressed()
	if err != nil {
		return nil, fmt.Errorf("open GHCR blob %s: %w", ref.String(), err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read GHCR blob %s: %w", ref.String(), err)
	}
	return body, nil
}

func parseGHCRBlobURL(sourceURL string) (repo, digest string, err error) {
	const prefix = "https://ghcr.io/v2/"
	if !strings.HasPrefix(sourceURL, prefix) {
		return "", "", fmt.Errorf("source url = %q, want GHCR URL", sourceURL)
	}
	rest := strings.TrimPrefix(sourceURL, prefix)
	repo, digest, ok := strings.Cut(rest, "/blobs/sha256:")
	if !ok || repo == "" || digest == "" {
		return "", "", fmt.Errorf("source url = %q, want GHCR blob URL", sourceURL)
	}
	return repo, digest, nil
}

func ghcrRemoteOptions(ctx context.Context, username, token string) []remote.Option {
	opts := []remote.Option{remote.WithContext(ctx)}
	if token != "" {
		opts = append(opts, remote.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: username,
			Password: token,
		})))
	}
	return opts
}
