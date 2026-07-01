package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/google/go-github/v68/github"
	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact"
	remotebuild "github.com/goplus/llar/internal/remote/build"
	"github.com/goplus/llar/internal/upload"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	defaultPostgresDSN   = "host=localhost port=5432 user=llar password=llar dbname=llar_e2e sslmode=disable"
	defaultTarget        = "madler/zlib@v1.3.1"
	defaultSharedTargets = "DaveGamble/cJSON@v1.7.18,pnggroup/libpng@v1.6.47"
	localFormulaRoot     = "testdata/remote-build-e2e/formulas"
	sharedDependency     = "madler/zlib@v1.3.1"
)

func main() {
	var cfg config
	flag.StringVar(&cfg.postgresDSN, "postgres-dsn", defaultPostgresDSN, "Postgres DSN")
	flag.StringVar(&cfg.ghcrOwner, "ghcr-owner", "", "GHCR owner")
	flag.StringVar(&cfg.ghcrUsername, "ghcr-username", "", "GHCR username")
	flag.StringVar(&cfg.ghcrToken, "ghcr-token", "", "GHCR token")
	flag.StringVar(&cfg.target, "target", defaultTarget, "target module@version")
	flag.StringVar(&cfg.sharedTargets, "shared-targets", defaultSharedTargets, "comma-separated module@version targets sharing a dependency")
	flag.StringVar(&cfg.matrix, "matrix", "", "matrix string")
	flag.DurationVar(&cfg.timeout, "timeout", 10*time.Minute, "E2E timeout")
	flag.Parse()

	if err := cfg.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	remotebuild.AllowLocal = true
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type config struct {
	postgresDSN   string
	ghcrOwner     string
	ghcrUsername  string
	ghcrToken     string
	target        string
	sharedTargets string
	matrix        string
	timeout       time.Duration
}

func (c *config) validate() error {
	if strings.TrimSpace(c.postgresDSN) == "" {
		return fmt.Errorf("missing required -postgres-dsn")
	}
	if strings.TrimSpace(c.ghcrOwner) == "" {
		return fmt.Errorf("missing required -ghcr-owner")
	}
	if strings.TrimSpace(c.ghcrUsername) == "" {
		return fmt.Errorf("missing required -ghcr-username")
	}
	if strings.TrimSpace(c.ghcrToken) == "" {
		return fmt.Errorf("missing required -ghcr-token")
	}
	if _, err := parseTarget(c.target); err != nil {
		return fmt.Errorf("-target: %w", err)
	}
	if targets, err := parseTargets(c.sharedTargets); err != nil {
		return fmt.Errorf("-shared-targets: %w", err)
	} else if len(targets) < 2 {
		return fmt.Errorf("-shared-targets must contain at least two targets")
	}
	if c.timeout <= 0 {
		return fmt.Errorf("-timeout must be positive")
	}
	return nil
}

func run(cfg config) error {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	db, err := gorm.Open(postgres.Open(cfg.postgresDSN), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("postgres DB: %w", err)
	}
	defer sqlDB.Close()

	if err := resetDatabase(ctx, db); err != nil {
		return err
	}
	store, err := artifact.NewGormStore(db)
	if err != nil {
		return fmt.Errorf("NewGormStore: %w", err)
	}
	if count := artifactCount(ctx, db); count != 0 {
		return fmt.Errorf("artifact count after reset = %d, want 0", count)
	}

	target, err := parseTarget(cfg.target)
	if err != nil {
		return err
	}
	if err := validateLocalFormula(target); err != nil {
		return err
	}
	sharedTargets, err := parseTargets(cfg.sharedTargets)
	if err != nil {
		return err
	}
	for _, target := range sharedTargets {
		if err := validateLocalFormula(target); err != nil {
			return err
		}
	}
	cleanupTargets := append([]remotebuild.Target{target}, sharedTargets...)
	if err := cleanupGHCRPackages(ctx, ghcrCleanupConfig{
		Owner: cfg.ghcrOwner,
		Token: cfg.ghcrToken,
	}, cleanupTargets); err != nil {
		return fmt.Errorf("cleanup GHCR packages: %w", err)
	}
	matrix := formula.Matrix{
		Require: map[string][]string{
			"os":   {runtime.GOOS},
			"arch": {runtime.GOARCH},
		},
	}
	if customMatrix := strings.TrimSpace(cfg.matrix); customMatrix != "" {
		matrix = formula.Matrix{
			Require: map[string][]string{
				"matrix": {customMatrix},
			},
		}
	}
	e2e := suite{
		cfg: configData{
			postgresDSN:   cfg.postgresDSN,
			ghcrOwner:     cfg.ghcrOwner,
			ghcrUsername:  cfg.ghcrUsername,
			ghcrToken:     cfg.ghcrToken,
			target:        target,
			matrix:        matrix,
			sharedTargets: sharedTargets,
		},
		store: store,
		db:    db,
	}

	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"cold build uploads and stores artifact", e2e.coldBuild},
		{"repeated build uses stored artifact", e2e.repeatedBuild},
		{"new builds instance uses persisted artifact cache", e2e.persistedCache},
		{"different matrix stores independent artifact", e2e.differentMatrix},
		{"concurrent duplicate build joins singleflight", e2e.concurrentDuplicate},
		{"concurrent targets sharing dependency both complete", e2e.concurrentDifferentTargets},
	} {
		start := time.Now()
		log.Printf("RUN %s", step.name)
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		log.Printf("PASS %s (%s)", step.name, time.Since(start).Round(time.Millisecond))
	}

	wantArtifacts := int64(3 + len(sharedTargets))
	if count := artifactCount(ctx, db); count != wantArtifacts {
		return fmt.Errorf("artifact count after E2E cases = %d, want %d", count, wantArtifacts)
	}
	if err := assertStoredArtifactSources(ctx, db, e2e.cfg); err != nil {
		return err
	}
	log.Printf("PASS remote build E2E (%d artifacts)", wantArtifacts)
	return nil
}

type configData struct {
	postgresDSN   string
	ghcrOwner     string
	ghcrUsername  string
	ghcrToken     string
	target        remotebuild.Target
	matrix        formula.Matrix
	sharedTargets []remotebuild.Target
}

type suite struct {
	cfg          configData
	store        artifact.Store
	db           *gorm.DB
	baseReq      remotebuild.Request
	baseBuilds   *remotebuild.Builds
	baseUploader *countingUploader
	base         []remotebuild.TargetArtifact
}

func (s *suite) coldBuild(ctx context.Context) error {
	s.baseReq = requestForTarget(s.cfg.target, s.cfg.matrix)
	s.baseUploader = newCountingUploader(s.cfg)
	opts := s.buildOptions(s.baseUploader)
	s.baseBuilds = remotebuild.New(opts)

	var info bytes.Buffer
	got, err := s.baseBuilds.Build(ctx, s.baseReq, &info)
	if err != nil {
		return fmt.Errorf("Build: %w\n%s", err, info.String())
	}
	if s.baseUploader.Calls() != 1 {
		return fmt.Errorf("uploader calls = %d, want 1", s.baseUploader.Calls())
	}
	if err := assertTargetArtifact(s.cfg, s.cfg.target, got); err != nil {
		return err
	}
	if err := assertGHCRArtifact(ctx, s.cfg, s.cfg.target, s.baseReq.Matrix, got[0].Artifact); err != nil {
		return err
	}
	if err := assertUploadOptions(s.baseUploader.Options()[0], s.cfg.target, s.baseReq); err != nil {
		return err
	}
	if err := assertStoredArtifact(ctx, s.store, s.cfg.target, s.baseReq.Matrix.Combinations()[0], got[0].Artifact); err != nil {
		return err
	}
	s.base = got
	return nil
}

func (s *suite) repeatedBuild(ctx context.Context) error {
	got, err := s.baseBuilds.Build(ctx, s.baseReq, nil)
	if err != nil {
		return fmt.Errorf("Build: %w", err)
	}
	if !reflect.DeepEqual(got, s.base) {
		return fmt.Errorf("Build = %+v, want %+v", got, s.base)
	}
	if err := assertGHCRArtifact(ctx, s.cfg, s.cfg.target, s.baseReq.Matrix, got[0].Artifact); err != nil {
		return err
	}
	if s.baseUploader.Calls() != 1 {
		return fmt.Errorf("uploader calls after cache hit = %d, want 1", s.baseUploader.Calls())
	}
	return nil
}

func (s *suite) persistedCache(ctx context.Context) error {
	uploader := newCountingUploader(s.cfg)
	opts := s.buildOptions(uploader)
	builds := remotebuild.New(opts)
	got, err := builds.Build(ctx, s.baseReq, nil)
	if err != nil {
		return fmt.Errorf("Build: %w", err)
	}
	if !reflect.DeepEqual(got, s.base) {
		return fmt.Errorf("Build = %+v, want %+v", got, s.base)
	}
	if err := assertGHCRArtifact(ctx, s.cfg, s.cfg.target, s.baseReq.Matrix, got[0].Artifact); err != nil {
		return err
	}
	if uploader.Calls() != 0 {
		return fmt.Errorf("uploader calls = %d, want 0", uploader.Calls())
	}
	return nil
}

func (s *suite) differentMatrix(ctx context.Context) error {
	matrix := s.cfg.matrix
	matrix.Require = maps.Clone(matrix.Require)
	matrix.Require["variant"] = []string{"variant"}
	req := requestForTarget(s.cfg.target, matrix)
	uploader := newCountingUploader(s.cfg)
	opts := s.buildOptions(uploader)
	builds := remotebuild.New(opts)
	got, err := builds.Build(ctx, req, nil)
	if err != nil {
		return fmt.Errorf("Build: %w", err)
	}
	if uploader.Calls() != 1 {
		return fmt.Errorf("uploader calls = %d, want 1", uploader.Calls())
	}
	if err := assertTargetArtifact(s.cfg, s.cfg.target, got); err != nil {
		return err
	}
	if err := assertGHCRArtifact(ctx, s.cfg, s.cfg.target, req.Matrix, got[0].Artifact); err != nil {
		return err
	}
	if err := assertUploadOptions(uploader.Options()[0], s.cfg.target, req); err != nil {
		return err
	}
	return assertStoredArtifact(ctx, s.store, s.cfg.target, req.Matrix.Combinations()[0], got[0].Artifact)
}

func (s *suite) concurrentDuplicate(ctx context.Context) error {
	matrix := s.cfg.matrix
	matrix.Require = maps.Clone(matrix.Require)
	matrix.Require["variant"] = []string{"concurrent"}
	req := requestForTarget(s.cfg.target, matrix)
	uploader := newCountingUploader(s.cfg)
	opts := s.buildOptions(uploader)
	builds := remotebuild.New(opts)

	results := make(chan buildResult, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			got, err := builds.Build(ctx, req, nil)
			results <- buildResult{artifacts: got, err: err}
		}()
	}
	close(start)

	first, err := waitBuildResult(ctx, results)
	if err != nil {
		return err
	}
	second, err := waitBuildResult(ctx, results)
	if err != nil {
		return err
	}
	if first.err != nil {
		return fmt.Errorf("first Build: %w", first.err)
	}
	if second.err != nil {
		return fmt.Errorf("second Build: %w", second.err)
	}
	if !reflect.DeepEqual(first.artifacts, second.artifacts) {
		return fmt.Errorf("joined result = %+v, want %+v", second.artifacts, first.artifacts)
	}
	if uploader.Calls() != 1 {
		return fmt.Errorf("uploader calls = %d, want 1", uploader.Calls())
	}
	if err := assertTargetArtifact(s.cfg, s.cfg.target, first.artifacts); err != nil {
		return err
	}
	if err := assertGHCRArtifact(ctx, s.cfg, s.cfg.target, req.Matrix, first.artifacts[0].Artifact); err != nil {
		return err
	}
	if err := assertUploadOptions(uploader.Options()[0], s.cfg.target, req); err != nil {
		return err
	}
	return assertStoredArtifact(ctx, s.store, s.cfg.target, req.Matrix.Combinations()[0], first.artifacts[0].Artifact)
}

func (s *suite) concurrentDifferentTargets(ctx context.Context) error {
	uploader := newCountingUploader(s.cfg)
	opts := s.buildOptions(uploader)
	builds := remotebuild.New(opts)
	matrix := s.cfg.matrix
	matrix.Require = maps.Clone(matrix.Require)
	matrix.Require["variant"] = []string{"shareddep"}
	matrixStr := matrix.Combinations()[0]

	results := make(chan namedBuildResult, len(s.cfg.sharedTargets))
	start := make(chan struct{})
	for _, target := range s.cfg.sharedTargets {
		req := requestForTarget(target, matrix)
		go func(target remotebuild.Target, req remotebuild.Request) {
			<-start
			got, err := builds.Build(ctx, req, nil)
			results <- namedBuildResult{target: target, req: req, artifacts: got, err: err}
		}(target, req)
	}
	close(start)

	gotByTarget := make(map[string][]remotebuild.TargetArtifact, len(s.cfg.sharedTargets))
	for range s.cfg.sharedTargets {
		result, err := waitNamedBuildResult(ctx, results)
		if err != nil {
			return err
		}
		if result.err != nil {
			return fmt.Errorf("Build %s@%s: %w", result.target.Module, result.target.Version, result.err)
		}
		if err := assertTargetArtifact(s.cfg, result.target, result.artifacts); err != nil {
			return err
		}
		if err := assertGHCRArtifact(ctx, s.cfg, result.target, result.req.Matrix, result.artifacts[0].Artifact); err != nil {
			return err
		}
		if err := assertArtifactDeps(ctx, s.cfg, result.artifacts[0].Artifact, []string{sharedDependency}); err != nil {
			return err
		}
		if err := assertStoredArtifact(ctx, s.store, result.target, result.req.Matrix.Combinations()[0], result.artifacts[0].Artifact); err != nil {
			return err
		}
		gotByTarget[result.artifacts[0].Target] = result.artifacts
	}
	if uploader.Calls() != len(s.cfg.sharedTargets) {
		return fmt.Errorf("uploader calls = %d, want %d", uploader.Calls(), len(s.cfg.sharedTargets))
	}
	for _, opts := range uploader.Options() {
		if opts.Attrs["org.llar.matrix"] != matrixStr {
			return fmt.Errorf("upload attrs = %+v, want matrix %q", opts.Attrs, matrixStr)
		}
	}
	for _, target := range s.cfg.sharedTargets {
		key := target.Module + "@" + target.Version
		if _, ok := gotByTarget[key]; !ok {
			return fmt.Errorf("missing result for %s; got targets %+v", key, gotByTarget)
		}
	}
	return nil
}

func (s *suite) buildOptions(uploader upload.Uploader) remotebuild.Options {
	return remotebuild.Options{
		Store:    s.store,
		Uploader: uploader,
	}
}

func resetDatabase(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec("DROP SCHEMA IF EXISTS public CASCADE").Error; err != nil {
		return fmt.Errorf("drop public schema: %w", err)
	}
	if err := db.WithContext(ctx).Exec("CREATE SCHEMA public").Error; err != nil {
		return fmt.Errorf("create public schema: %w", err)
	}
	return nil
}

func artifactCount(ctx context.Context, db *gorm.DB) int64 {
	var count int64
	if err := db.WithContext(ctx).Table("artifacts").Count(&count).Error; err != nil {
		return -1
	}
	return count
}

type ghcrCleanupConfig struct {
	Owner string
	Token string
}

func cleanupGHCRPackages(ctx context.Context, cfg ghcrCleanupConfig, targets []remotebuild.Target) error {
	owner := strings.TrimSpace(cfg.Owner)
	token := strings.TrimSpace(cfg.Token)
	if owner == "" || token == "" {
		return fmt.Errorf("GHCR cleanup requires owner and token")
	}
	client := github.NewClient(nil).WithAuthToken(token)
	for _, packageName := range ghcrCleanupPackageNames(targets) {
		if err := deleteGHCRPackage(ctx, client, owner, packageName); err != nil {
			return err
		}
	}
	return nil
}

func ghcrCleanupPackageNames(targets []remotebuild.Target) []string {
	seen := make(map[string]bool, len(targets))
	packages := make([]string, 0, len(targets))
	for _, target := range targets {
		name := strings.ToLower(strings.Trim(target.Module, "/"))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		packages = append(packages, name)
	}
	return packages
}

func deleteGHCRPackage(ctx context.Context, client *github.Client, owner, packageName string) error {
	if _, err := client.Organizations.DeletePackage(ctx, owner, "container", packageName); err == nil {
		log.Printf("deleted GHCR package %s/%s", owner, packageName)
		return nil
	} else if !isGitHubNotFound(err) {
		return fmt.Errorf("delete org GHCR package %s/%s: %w", owner, packageName, err)
	}

	if err := deleteUserPackage(ctx, client, owner, "container", packageName); err == nil {
		log.Printf("deleted GHCR package %s/%s", owner, packageName)
		return nil
	} else if !isGitHubNotFound(err) {
		return fmt.Errorf("delete user GHCR package %s/%s: %w", owner, packageName, err)
	}
	log.Printf("GHCR package %s/%s does not exist", owner, packageName)
	return nil
}

func deleteUserPackage(ctx context.Context, client *github.Client, owner, packageType, packageName string) error {
	req, err := client.NewRequest(http.MethodDelete, "users/"+owner+"/packages/"+packageType+"/"+url.PathEscape(packageName), nil)
	if err != nil {
		return err
	}
	_, err = client.Do(ctx, req, nil)
	return err
}

func isGitHubNotFound(err error) bool {
	errResp, ok := err.(*github.ErrorResponse)
	return ok && errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotFound
}

func requestForTarget(target remotebuild.Target, matrix formula.Matrix) remotebuild.Request {
	localTarget := target
	localTarget.Module = localFormulaPath(target)
	return remotebuild.Request{
		Target: localTarget,
		Matrix: matrix,
	}
}

func parseTarget(value string) (remotebuild.Target, error) {
	module, version, ok := strings.Cut(strings.TrimSpace(value), "@")
	if !ok || module == "" || version == "" {
		return remotebuild.Target{}, fmt.Errorf("target must be module@version, got %q", value)
	}
	return remotebuild.Target{Module: module, Version: version}, nil
}

func parseTargets(value string) ([]remotebuild.Target, error) {
	parts := strings.Split(value, ",")
	targets := make([]remotebuild.Target, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		target, err := parseTarget(part)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func validateLocalFormula(target remotebuild.Target) error {
	dir := filepath.Join(localFormulaRoot, filepath.FromSlash(target.Module))
	if _, err := os.Stat(filepath.Join(dir, "versions.json")); err != nil {
		return fmt.Errorf("local formula for %s: %w", target.Module, err)
	}
	return nil
}

func localFormulaPath(target remotebuild.Target) string {
	root, err := filepath.Abs(localFormulaRoot)
	if err != nil {
		root = localFormulaRoot
	}
	return filepath.Join(root, filepath.FromSlash(target.Module))
}

func assertTargetArtifact(cfg configData, target remotebuild.Target, got []remotebuild.TargetArtifact) error {
	if len(got) != 1 {
		return fmt.Errorf("artifact count = %d, want 1: %+v", len(got), got)
	}
	artifact := got[0]
	wantTarget := target.Module + "@" + target.Version
	if artifact.Target != wantTarget {
		return fmt.Errorf("target = %q, want %q", artifact.Target, wantTarget)
	}
	if artifact.Artifact.Source.Type != "ghcr" {
		return fmt.Errorf("source type = %q, want ghcr", artifact.Artifact.Source.Type)
	}
	wantURLPrefix := "https://ghcr.io/v2/" + strings.ToLower(cfg.ghcrOwner+"/"+target.Module) + "/blobs/sha256:"
	if !strings.HasPrefix(artifact.Artifact.Source.URL, wantURLPrefix) {
		return fmt.Errorf("source url = %q, want prefix %q", artifact.Artifact.Source.URL, wantURLPrefix)
	}
	digest, err := sourceDigest(artifact.Artifact.Source.URL)
	if err != nil {
		return err
	}
	if artifact.Artifact.Type != "tar.gz" {
		return fmt.Errorf("archive type = %q, want tar.gz", artifact.Artifact.Type)
	}
	if artifact.Artifact.Metadata == "" {
		return fmt.Errorf("metadata is empty")
	}
	if artifact.Artifact.Checksum == "" {
		return fmt.Errorf("checksum is empty")
	}
	if artifact.Artifact.Checksum != digest {
		return fmt.Errorf("checksum = %q, want source digest %q", artifact.Artifact.Checksum, digest)
	}
	return nil
}

func assertUploadOptions(opts upload.Options, target remotebuild.Target, req remotebuild.Request) error {
	if opts.Name != target.Module {
		return fmt.Errorf("upload name = %q, want %q", opts.Name, target.Module)
	}
	if opts.Tag != target.Version {
		return fmt.Errorf("upload tag = %q, want %q", opts.Tag, target.Version)
	}
	want := map[string]string{
		"org.llar.matrix": req.Matrix.Combinations()[0],
	}
	if values := req.Matrix.Require["os"]; len(values) > 0 && values[0] != "" {
		want["os"] = values[0]
	}
	if values := req.Matrix.Require["arch"]; len(values) > 0 && values[0] != "" {
		want["arch"] = values[0]
	}
	if !reflect.DeepEqual(opts.Attrs, want) {
		return fmt.Errorf("upload attrs = %+v, want %+v", opts.Attrs, want)
	}
	return nil
}

func assertGHCRArtifact(ctx context.Context, cfg configData, target remotebuild.Target, matrix formula.Matrix, got artifact.Artifact) error {
	size, err := assertGHCRBlob(ctx, cfg, got.Source.URL, got.Checksum)
	if err != nil {
		return err
	}
	if err := assertGHCRTag(ctx, cfg, target, matrix, got, size); err != nil {
		return err
	}
	return nil
}

func assertGHCRTag(ctx context.Context, cfg configData, target remotebuild.Target, matrix formula.Matrix, got artifact.Artifact, blobSize int64) error {
	ref, err := name.NewTag(ghcrTag(cfg, target), name.WeakValidation)
	if err != nil {
		return fmt.Errorf("GHCR tag ref: %w", err)
	}
	index, err := remote.Index(ref, ghcrRemoteOptions(ctx, cfg)...)
	if err != nil {
		return fmt.Errorf("read GHCR index %s: %w", ref.String(), err)
	}
	mediaType, err := index.MediaType()
	if err != nil {
		return fmt.Errorf("GHCR index media type: %w", err)
	}
	if mediaType != types.OCIImageIndex {
		return fmt.Errorf("GHCR index media type = %q, want %q", mediaType, types.OCIImageIndex)
	}
	indexManifest, err := index.IndexManifest()
	if err != nil {
		return fmt.Errorf("GHCR index manifest: %w", err)
	}
	if indexManifest.MediaType != types.OCIImageIndex {
		return fmt.Errorf("GHCR index manifest media type = %q, want %q", indexManifest.MediaType, types.OCIImageIndex)
	}
	if len(indexManifest.Manifests) != 1 {
		return fmt.Errorf("GHCR index manifest count = %d, want 1", len(indexManifest.Manifests))
	}
	entry := indexManifest.Manifests[0]
	matrixStr := matrix.Combinations()[0]
	if entry.Annotations["org.llar.matrix"] != matrixStr {
		return fmt.Errorf("GHCR matrix annotation = %q, want %q", entry.Annotations["org.llar.matrix"], matrixStr)
	}
	if err := assertGHCRPlatform(entry.Platform, matrix); err != nil {
		return err
	}

	image, err := index.Image(entry.Digest)
	if err != nil {
		return fmt.Errorf("read GHCR image %s: %w", entry.Digest.String(), err)
	}
	manifest, err := image.Manifest()
	if err != nil {
		return fmt.Errorf("GHCR image manifest: %w", err)
	}
	if manifest.MediaType != types.OCIManifestSchema1 {
		return fmt.Errorf("GHCR image manifest media type = %q, want %q", manifest.MediaType, types.OCIManifestSchema1)
	}
	if len(manifest.Layers) != 1 {
		return fmt.Errorf("GHCR image layer count = %d, want 1", len(manifest.Layers))
	}
	layer := manifest.Layers[0]
	wantLayerType, err := layerMediaType(got.Type)
	if err != nil {
		return err
	}
	if layer.MediaType != wantLayerType {
		return fmt.Errorf("GHCR layer media type = %q, want %q", layer.MediaType, wantLayerType)
	}
	if layer.Digest.Algorithm != "sha256" || layer.Digest.Hex != got.Checksum {
		return fmt.Errorf("GHCR layer digest = %s, want sha256:%s", layer.Digest.String(), got.Checksum)
	}
	if layer.Size != blobSize {
		return fmt.Errorf("GHCR layer size = %d, want blob size %d", layer.Size, blobSize)
	}
	return nil
}

func assertGHCRPlatform(platform *v1.Platform, matrix formula.Matrix) error {
	wantOS := firstMatrixValue(matrix, "os")
	wantArch := firstMatrixValue(matrix, "arch")
	if wantOS == "" && wantArch == "" {
		if platform != nil && (platform.OS != "" || platform.Architecture != "") {
			return fmt.Errorf("GHCR platform = %+v, want empty", platform)
		}
		return nil
	}
	if platform == nil {
		return fmt.Errorf("GHCR platform is nil, want os=%q arch=%q", wantOS, wantArch)
	}
	if platform.OS != wantOS {
		return fmt.Errorf("GHCR platform os = %q, want %q", platform.OS, wantOS)
	}
	if platform.Architecture != wantArch {
		return fmt.Errorf("GHCR platform arch = %q, want %q", platform.Architecture, wantArch)
	}
	return nil
}

func assertGHCRBlob(ctx context.Context, cfg configData, sourceURL, checksum string) (int64, error) {
	body, err := readGHCRBlob(ctx, cfg, sourceURL, checksum)
	if err != nil {
		return 0, err
	}
	hash, size, err := v1.SHA256(bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("hash GHCR blob %s: %w", sourceURL, err)
	}
	if hash.Hex != checksum {
		return 0, fmt.Errorf("GHCR blob checksum = %s, want %s", hash.Hex, checksum)
	}
	if size <= 0 {
		return 0, fmt.Errorf("GHCR blob size = %d, want positive", size)
	}
	return size, nil
}

func readGHCRBlob(ctx context.Context, cfg configData, sourceURL, checksum string) ([]byte, error) {
	digest, err := sourceDigest(sourceURL)
	if err != nil {
		return nil, err
	}
	if checksum != digest {
		return nil, fmt.Errorf("checksum = %q, want source digest %q", checksum, digest)
	}
	repo, err := sourceRepo(sourceURL)
	if err != nil {
		return nil, err
	}
	ref, err := name.NewDigest("ghcr.io/"+repo+"@sha256:"+digest, name.WeakValidation)
	if err != nil {
		return nil, fmt.Errorf("GHCR blob ref: %w", err)
	}
	layer, err := remote.Layer(ref, ghcrRemoteOptions(ctx, cfg)...)
	if err != nil {
		return nil, fmt.Errorf("read GHCR blob %s: %w", ref.String(), err)
	}
	r, err := layer.Compressed()
	if err != nil {
		return nil, fmt.Errorf("open GHCR blob %s: %w", ref.String(), err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read GHCR blob %s: %w", ref.String(), err)
	}
	return body, nil
}

type artifactMetadata struct {
	Deps []string `json:"deps"`
}

func assertArtifactDeps(ctx context.Context, cfg configData, got artifact.Artifact, want []string) error {
	body, err := readGHCRBlob(ctx, cfg, got.Source.URL, got.Checksum)
	if err != nil {
		return err
	}
	metadata, err := readArtifactMetadata(body)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(metadata.Deps, want) {
		return fmt.Errorf("artifact deps = %+v, want %+v", metadata.Deps, want)
	}
	return nil
}

func readArtifactMetadata(body []byte) (artifactMetadata, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return artifactMetadata{}, fmt.Errorf("open artifact gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return artifactMetadata{}, fmt.Errorf("artifact metadata missing")
		}
		if err != nil {
			return artifactMetadata{}, fmt.Errorf("read artifact tar: %w", err)
		}
		if header.Name != ".llar/metadata.json" {
			continue
		}
		var metadata artifactMetadata
		if err := json.NewDecoder(tr).Decode(&metadata); err != nil {
			return artifactMetadata{}, fmt.Errorf("decode artifact metadata: %w", err)
		}
		return metadata, nil
	}
}

type storedArtifactRow struct {
	SourceType string
	SourceURL  string
	Checksum   string
}

func assertStoredArtifactSources(ctx context.Context, db *gorm.DB, cfg configData) error {
	var rows []storedArtifactRow
	if err := db.WithContext(ctx).Table("artifacts").Select("source_type, source_url, checksum").Find(&rows).Error; err != nil {
		return fmt.Errorf("list stored artifacts: %w", err)
	}
	for _, row := range rows {
		if row.SourceType != "ghcr" {
			return fmt.Errorf("stored source type = %q, want ghcr", row.SourceType)
		}
		if _, err := assertGHCRBlob(ctx, cfg, row.SourceURL, row.Checksum); err != nil {
			return err
		}
	}
	return nil
}

func ghcrRemoteOptions(ctx context.Context, cfg configData) []remote.Option {
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: cfg.ghcrUsername,
			Password: cfg.ghcrToken,
		})),
	}
}

func ghcrTag(cfg configData, target remotebuild.Target) string {
	return "ghcr.io/" + strings.ToLower(strings.Trim(cfg.ghcrOwner, "/")+"/"+strings.Trim(target.Module, "/")) + ":" + target.Version
}

func sourceDigest(sourceURL string) (string, error) {
	const marker = "/blobs/sha256:"
	_, digest, ok := strings.Cut(sourceURL, marker)
	if !ok || digest == "" {
		return "", fmt.Errorf("source url = %q, want sha256 blob URL", sourceURL)
	}
	if _, err := v1.NewHash("sha256:" + digest); err != nil {
		return "", fmt.Errorf("source digest %q: %w", digest, err)
	}
	return digest, nil
}

func sourceRepo(sourceURL string) (string, error) {
	const prefix = "https://ghcr.io/v2/"
	if !strings.HasPrefix(sourceURL, prefix) {
		return "", fmt.Errorf("source url = %q, want GHCR URL", sourceURL)
	}
	repo, _, ok := strings.Cut(strings.TrimPrefix(sourceURL, prefix), "/blobs/sha256:")
	if !ok || repo == "" {
		return "", fmt.Errorf("source url = %q, want GHCR blob URL", sourceURL)
	}
	return repo, nil
}

func firstMatrixValue(matrix formula.Matrix, name string) string {
	values := matrix.Require[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func layerMediaType(archiveType string) (types.MediaType, error) {
	switch archiveType {
	case "tar.gz":
		return types.OCILayer, nil
	case "tar.zst":
		return types.OCILayerZStd, nil
	default:
		return "", fmt.Errorf("unsupported archive type %q", archiveType)
	}
}

func assertStoredArtifact(ctx context.Context, store artifact.Store, target remotebuild.Target, matrixStr string, want artifact.Artifact) error {
	got, ok, err := store.Get(ctx, artifact.Key{
		Module:    target.Module,
		Version:   target.Version,
		MatrixStr: matrixStr,
	})
	if err != nil {
		return fmt.Errorf("Get stored artifact: %w", err)
	}
	if !ok {
		return fmt.Errorf("stored artifact missing")
	}
	if got != want {
		return fmt.Errorf("stored artifact = %+v, want %+v", got, want)
	}
	return nil
}

type countingUploader struct {
	mu      sync.Mutex
	calls   int
	options []upload.Options
	inner   upload.Uploader
}

func newCountingUploader(cfg configData) *countingUploader {
	return &countingUploader{
		inner: upload.NewGHCR(upload.GHCRConfig{
			Owner:    cfg.ghcrOwner,
			Username: cfg.ghcrUsername,
			Token:    cfg.ghcrToken,
		}),
	}
}

func (u *countingUploader) Type() string {
	return u.inner.Type()
}

func (u *countingUploader) Upload(ctx context.Context, r io.ReadSeeker, opts upload.Options) (upload.Result, error) {
	u.mu.Lock()
	u.calls++
	u.options = append(u.options, opts)
	u.mu.Unlock()

	offset, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return upload.Result{}, err
	}
	hash, size, err := v1.SHA256(r)
	if err != nil {
		return upload.Result{}, err
	}
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return upload.Result{}, err
	}
	result, err := u.inner.Upload(ctx, r, opts)
	if err != nil {
		return upload.Result{}, err
	}
	if result.Checksum != hash.Hex {
		return upload.Result{}, fmt.Errorf("upload checksum = %q, want archive checksum %q", result.Checksum, hash.Hex)
	}
	if result.Size != size {
		return upload.Result{}, fmt.Errorf("upload size = %d, want archive size %d", result.Size, size)
	}
	return result, nil
}

func (u *countingUploader) Calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *countingUploader) Options() []upload.Options {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]upload.Options(nil), u.options...)
}

type buildResult struct {
	artifacts []remotebuild.TargetArtifact
	err       error
}

type namedBuildResult struct {
	target    remotebuild.Target
	req       remotebuild.Request
	artifacts []remotebuild.TargetArtifact
	err       error
}

func waitBuildResult(ctx context.Context, ch <-chan buildResult) (buildResult, error) {
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return buildResult{}, ctx.Err()
	}
}

func waitNamedBuildResult(ctx context.Context, ch <-chan namedBuildResult) (namedBuildResult, error) {
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return namedBuildResult{}, ctx.Err()
	}
}
