package uploader

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
	"github.com/google/go-github/v68/github"
)

type GHCRConfig struct {
	Owner     string
	Username  string
	Token     string
	SourceURL string
}

func NewGHCR(cfg GHCRConfig) Uploader {
	return newGHCR(cfg, ghcrOptions{})
}

type ghcrOptions struct {
	writeIndex indexWriter
	client     *github.Client
	sleep      func(context.Context) error
}

func newGHCR(cfg GHCRConfig, opts ghcrOptions) *ghcr {
	client := opts.client
	if client == nil {
		client = github.NewClient(nil)
		if cfg.Token != "" {
			client = client.WithAuthToken(cfg.Token)
		}
	}
	writeIndex := opts.writeIndex
	if writeIndex == nil {
		writeIndex = writeRemoteIndex
	}
	return &ghcr{
		cfg:        cfg,
		writeIndex: writeIndex,
		client:     client,
		sleep:      opts.sleep,
	}
}

type ghcr struct {
	cfg        GHCRConfig
	writeIndex indexWriter
	client     *github.Client
	sleep      func(context.Context) error
}

type indexWriter func(ctx context.Context, ref string, index v1.ImageIndex, username, token string) error

func (u *ghcr) Type() string {
	return "ghcr"
}

func (u *ghcr) Upload(ctx context.Context, r io.ReadSeeker, opts Options) (Result, error) {
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

	index, err := buildIndex(payload, layerType, opts.Attrs, u.cfg.SourceURL)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(u.cfg.SourceURL) != "" {
		if strings.TrimSpace(u.cfg.Owner) == "" {
			return Result{}, errors.New("ghcr package create owner is required")
		}
		if strings.TrimSpace(u.cfg.Token) == "" {
			return Result{}, errors.New("ghcr package create token is required")
		}
		sourceRepo, err := parseGitHubSourceURL(u.cfg.SourceURL)
		if err != nil {
			return Result{}, err
		}
		packageName := ghcrPackageName(ref.repo, u.cfg.Owner)
		if packageName == "" {
			return Result{}, fmt.Errorf("ghcr package name is empty for %q", ref.repo)
		}
		exists, err := u.packageExists(ctx, packageName)
		if err != nil {
			return Result{}, err
		}
		if !exists {
			if err := u.createPackage(ctx, sourceRepo, packageName); err != nil {
				return Result{}, fmt.Errorf("create GHCR package: %w", err)
			}
			if err := u.waitPackage(ctx, packageName); err != nil {
				return Result{}, err
			}
		}
	}
	if err := u.writeIndex(ctx, ref.String(), index, u.cfg.Username, u.cfg.Token); err != nil {
		return Result{}, err
	}

	result.URL = "https://ghcr.io/v2/" + ref.repo + "/blobs/sha256:" + result.Checksum
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

func buildIndex(payload []byte, layerType types.MediaType, attrs map[string]string, sourceURL string) (v1.ImageIndex, error) {
	layer := static.NewLayer(payload, layerType)
	img, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:     layer,
		MediaType: layerType,
	})
	if err != nil {
		return nil, err
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	annotations := map[string]string{
		"org.llar.matrix": attrs["org.llar.matrix"],
	}
	if sourceURL = strings.TrimSpace(sourceURL); sourceURL != "" {
		annotations["org.opencontainers.image.source"] = sourceURL
	}
	return mutate.IndexMediaType(mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: img,
		Descriptor: v1.Descriptor{
			Annotations: annotations,
			Platform:    platformFromAttrs(attrs),
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
