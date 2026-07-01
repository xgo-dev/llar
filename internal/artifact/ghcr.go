package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type GHCRConfig struct {
	Owner    string
	Username string
	Token    string
}

func NewGHCR(cfg GHCRConfig) *GHCR {
	return &GHCR{
		cfg:        cfg,
		writeIndex: writeRemoteIndex,
	}
}

type GHCR struct {
	cfg        GHCRConfig
	writeIndex indexWriter
}

type indexWriter func(ctx context.Context, ref string, index v1.ImageIndex, username, token string) error

func (u *GHCR) Type() string {
	return "ghcr"
}

func (u *GHCR) Upload(ctx context.Context, r io.ReadSeeker, opts Options) (Result, error) {
	ref, err := parseGHCRName(opts.Name, opts.Tag, u.cfg.Owner)
	if err != nil {
		return Result{}, err
	}
	archiveType := opts.Type
	if archiveType == "" {
		archiveType = "tar.gz"
	}
	layerType, err := layerMediaType(archiveType)
	if err != nil {
		return Result{}, err
	}

	offset, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return Result{}, err
	}
	payload, result, err := readPayload(r)
	if err != nil {
		return Result{}, err
	}
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return Result{}, err
	}

	index, err := buildIndex(payload, layerType, opts.Attrs)
	if err != nil {
		return Result{}, err
	}
	writeIndex := u.writeIndex
	if writeIndex == nil {
		writeIndex = writeRemoteIndex
	}
	if err := writeIndex(ctx, ref.String(), index, u.cfg.Username, u.cfg.Token); err != nil {
		return Result{}, err
	}

	result.URL = "https://ghcr.io/v2/" + ref.repo + "/blobs/sha256:" + result.Checksum
	return result, nil
}

func (u *GHCR) Download(ctx context.Context, source Source, checksum string) ([]byte, error) {
	if source.Type != "" && source.Type != u.Type() {
		return nil, fmt.Errorf("source type = %q, want %q", source.Type, u.Type())
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
	layer, err := remote.Layer(ref, u.remoteOptions(ctx)...)
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

func checksumResult(r io.ReadSeeker) (Result, error) {
	offset, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return Result{}, err
	}
	_, result, err := readPayload(r)
	_, seekErr := r.Seek(offset, io.SeekStart)
	if err != nil {
		return Result{}, err
	}
	if seekErr != nil {
		return Result{}, seekErr
	}
	return result, nil
}

func readPayload(r io.Reader) ([]byte, Result, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return nil, Result{}, err
	}
	sum := sha256.Sum256(payload)
	return payload, Result{
		Size:     int64(len(payload)),
		Checksum: hex.EncodeToString(sum[:]),
	}, nil
}

type ghcrRef struct {
	repo string
	tag  string
}

func (r ghcrRef) String() string {
	return "ghcr.io/" + r.repo + ":" + r.tag
}

func parseGHCRName(rawName, tag, owner string) (ghcrRef, error) {
	rawName = strings.TrimSpace(strings.TrimPrefix(rawName, "ghcr.io/"))
	if rawName == "" {
		return ghcrRef{}, errors.New("ghcr name is required")
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ghcrRef{}, errors.New("ghcr tag is required")
	}
	repo := strings.Trim(rawName, "/")
	if owner != "" {
		owner = strings.Trim(owner, "/")
		if repo != owner && !strings.HasPrefix(repo, owner+"/") {
			repo = owner + "/" + repo
		}
	}
	repo = strings.ToLower(repo)
	if repo == "" || tag == "" {
		return ghcrRef{}, fmt.Errorf("invalid ghcr name %q", rawName)
	}
	return ghcrRef{repo: repo, tag: tag}, nil
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

func layerMediaType(archiveType string) (types.MediaType, error) {
	switch archiveType {
	case "tar.gz":
		return types.OCILayer, nil
	case "tar.zst":
		return types.OCILayerZStd, nil
	default:
		return "", fmt.Errorf("unsupported ghcr archive type %q", archiveType)
	}
}

func buildIndex(payload []byte, layerType types.MediaType, attrs map[string]string) (v1.ImageIndex, error) {
	layer := static.NewLayer(payload, layerType)
	img, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:     layer,
		MediaType: layerType,
	})
	if err != nil {
		return nil, err
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	return mutate.IndexMediaType(mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: img,
		Descriptor: v1.Descriptor{
			Annotations: map[string]string{
				"org.llar.matrix": attrs["org.llar.matrix"],
			},
			Platform: platformFromAttrs(attrs),
		},
	}), types.OCIImageIndex), nil
}

func platformFromAttrs(attrs map[string]string) *v1.Platform {
	if attrs["os"] == "" && attrs["arch"] == "" {
		return nil
	}
	return &v1.Platform{
		OS:           attrs["os"],
		Architecture: attrs["arch"],
	}
}

func writeRemoteIndex(ctx context.Context, ref string, index v1.ImageIndex, username, token string) error {
	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		return err
	}
	opts := ghcrRemoteOptions(ctx, username, token)
	return remote.WriteIndex(tag, index, opts...)
}

func (u *GHCR) remoteOptions(ctx context.Context) []remote.Option {
	return ghcrRemoteOptions(ctx, u.cfg.Username, u.cfg.Token)
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
