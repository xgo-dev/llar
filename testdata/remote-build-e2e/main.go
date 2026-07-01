package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

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
	sharedTargets, err := parseTargets(cfg.sharedTargets)
	if err != nil {
		return err
	}
	cleanupTargets := append([]remotebuild.Target{target}, sharedTargets...)
	if err := cleanupGHCRPackages(ctx, ghcrCleanupConfig{
		Owner: cfg.ghcrOwner,
		Token: cfg.ghcrToken,
	}, cleanupTargets); err != nil {
		return fmt.Errorf("cleanup GHCR packages: %w", err)
	}
	matrixStr := strings.TrimSpace(cfg.matrix)
	if matrixStr == "" {
		matrixStr = hostMatrixStr()
	}
	e2e := suite{
		cfg: configData{
			postgresDSN:   cfg.postgresDSN,
			ghcrOwner:     cfg.ghcrOwner,
			ghcrUsername:  cfg.ghcrUsername,
			ghcrToken:     cfg.ghcrToken,
			target:        target,
			matrixStr:     matrixStr,
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
		{"concurrent different targets both complete", e2e.concurrentDifferentTargets},
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
	log.Printf("PASS remote build E2E (%d artifacts)", wantArtifacts)
	return nil
}

type configData struct {
	postgresDSN   string
	ghcrOwner     string
	ghcrUsername  string
	ghcrToken     string
	target        remotebuild.Target
	matrixStr     string
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
	s.baseReq = requestForTarget(s.cfg.target, s.cfg.matrixStr)
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
	if err := assertUploadAttrs(s.baseUploader.Options()[0], s.baseReq); err != nil {
		return err
	}
	if err := assertStoredArtifact(ctx, s.store, s.baseReq, got[0].Artifact); err != nil {
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
	if uploader.Calls() != 0 {
		return fmt.Errorf("uploader calls = %d, want 0", uploader.Calls())
	}
	return nil
}

func (s *suite) differentMatrix(ctx context.Context) error {
	req := requestForTarget(s.cfg.target, s.cfg.matrixStr+"-variant")
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
	if err := assertUploadAttrs(uploader.Options()[0], req); err != nil {
		return err
	}
	return assertStoredArtifact(ctx, s.store, req, got[0].Artifact)
}

func (s *suite) concurrentDuplicate(ctx context.Context) error {
	req := requestForTarget(s.cfg.target, s.cfg.matrixStr+"-concurrent")
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
	if err := assertUploadAttrs(uploader.Options()[0], req); err != nil {
		return err
	}
	return assertStoredArtifact(ctx, s.store, req, first.artifacts[0].Artifact)
}

func (s *suite) concurrentDifferentTargets(ctx context.Context) error {
	uploader := newCountingUploader(s.cfg)
	opts := s.buildOptions(uploader)
	builds := remotebuild.New(opts)
	matrixStr := s.cfg.matrixStr + "-shareddep"

	results := make(chan namedBuildResult, len(s.cfg.sharedTargets))
	start := make(chan struct{})
	for _, target := range s.cfg.sharedTargets {
		req := requestForTarget(target, matrixStr)
		go func(req remotebuild.Request) {
			<-start
			got, err := builds.Build(ctx, req, nil)
			results <- namedBuildResult{req: req, artifacts: got, err: err}
		}(req)
	}
	close(start)

	gotByTarget := make(map[string][]remotebuild.TargetArtifact, len(s.cfg.sharedTargets))
	for range s.cfg.sharedTargets {
		result, err := waitNamedBuildResult(ctx, results)
		if err != nil {
			return err
		}
		if result.err != nil {
			return fmt.Errorf("Build %s@%s: %w", result.req.Target.Module, result.req.Target.Version, result.err)
		}
		if err := assertTargetArtifact(s.cfg, result.req.Target, result.artifacts); err != nil {
			return err
		}
		if err := assertStoredArtifact(ctx, s.store, result.req, result.artifacts[0].Artifact); err != nil {
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

func requestForTarget(target remotebuild.Target, matrixStr string) remotebuild.Request {
	return remotebuild.Request{
		Target:    target,
		MatrixStr: matrixStr,
		Matrix: remotebuild.Matrix{
			Require: map[string]string{
				"os":   runtime.GOOS,
				"arch": runtime.GOARCH,
			},
		},
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
	if artifact.Artifact.Type != "tar.gz" {
		return fmt.Errorf("archive type = %q, want tar.gz", artifact.Artifact.Type)
	}
	if artifact.Artifact.Metadata == "" {
		return fmt.Errorf("metadata is empty")
	}
	if artifact.Artifact.Checksum == "" {
		return fmt.Errorf("checksum is empty")
	}
	return nil
}

func assertUploadAttrs(opts upload.Options, req remotebuild.Request) error {
	want := map[string]string{
		"org.llar.matrix": req.MatrixStr,
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
	}
	if !reflect.DeepEqual(opts.Attrs, want) {
		return fmt.Errorf("upload attrs = %+v, want %+v", opts.Attrs, want)
	}
	return nil
}

func assertStoredArtifact(ctx context.Context, store artifact.Store, req remotebuild.Request, want artifact.Artifact) error {
	got, ok, err := store.Get(ctx, artifact.Key{
		Module:    req.Target.Module,
		Version:   req.Target.Version,
		MatrixStr: req.MatrixStr,
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

func hostMatrixStr() string {
	matrix := formula.Matrix{
		Require: map[string][]string{
			"os":   {runtime.GOOS},
			"arch": {runtime.GOARCH},
		},
	}
	return matrix.Combinations()[0]
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
	return u.inner.Upload(ctx, r, opts)
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
