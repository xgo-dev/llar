package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/build"
	buildcache "github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/mod/module"
	qiniuclient "github.com/qiniu/go-sdk/v7/client"
	"github.com/qiniu/go-sdk/v7/storagev2/credentials"
	qiniudownloader "github.com/qiniu/go-sdk/v7/storagev2/downloader"
	httpclient "github.com/qiniu/go-sdk/v7/storagev2/http_client"
	"github.com/qiniu/go-sdk/v7/storagev2/objects"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	defaultPostgresDSN   = "host=localhost port=5432 user=llar password=llar dbname=llar_e2e sslmode=disable"
	defaultTarget        = "madler/zlib@v1.3.1"
	defaultSharedTargets = "DaveGamble/cJSON@v1.7.18,pnggroup/libpng@v1.6.47"
	defaultFormulaRoot   = "testdata/kodo-e2e/formulas"
	defaultPublicDomain  = "llar.liuxi.ng"
	kodoEntryMetadataKey = "llar-entry"
)

func main() {
	var cfg config
	flag.StringVar(&cfg.postgresDSN, "postgres-dsn", envOrDefault("POSTGRES_DSN", defaultPostgresDSN), "Postgres DSN; empty uses a temporary SQLite database")
	flag.StringVar(&cfg.accessKey, "qiniu-access-key", os.Getenv("QINIU_ACCESS_KEY"), "Qiniu access key")
	flag.StringVar(&cfg.secretKey, "qiniu-secret-key", os.Getenv("QINIU_SECRET_KEY"), "Qiniu secret key")
	flag.StringVar(&cfg.bucket, "qiniu-bucket", os.Getenv("QINIU_BUCKET"), "Qiniu Kodo bucket")
	flag.StringVar(&cfg.publicDomain, "qiniu-public-domain", envOrDefault("QINIU_PUBLIC_DOMAIN", defaultPublicDomain), "Qiniu Kodo public download domain")
	flag.StringVar(&cfg.prefix, "qiniu-prefix", os.Getenv("QINIU_PREFIX"), "Qiniu Kodo object prefix")
	flag.StringVar(&cfg.formulaRoot, "formula-root", defaultFormulaRoot, "local formula root")
	flag.StringVar(&cfg.target, "target", defaultTarget, "target module@version")
	flag.StringVar(&cfg.sharedTargets, "shared-targets", defaultSharedTargets, "comma-separated module@version targets sharing a dependency")
	flag.StringVar(&cfg.matrix, "matrix", hostMatrix(), "matrix string")
	flag.DurationVar(&cfg.timeout, "timeout", 15*time.Minute, "E2E timeout")
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
	accessKey     string
	secretKey     string
	bucket        string
	publicDomain  string
	prefix        string
	formulaRoot   string
	target        string
	sharedTargets string
	matrix        string
	timeout       time.Duration
}

func (c *config) validate() error {
	var err error
	c.accessKey = strings.TrimSpace(c.accessKey)
	c.secretKey = strings.TrimSpace(c.secretKey)
	c.bucket = strings.TrimSpace(c.bucket)
	c.publicDomain = normalizePublicDomain(c.publicDomain)
	c.prefix = strings.Trim(strings.TrimSpace(c.prefix), "/")
	c.matrix = strings.TrimSpace(c.matrix)
	c.formulaRoot, err = filepath.Abs(c.formulaRoot)
	if err != nil {
		return fmt.Errorf("formula root: %w", err)
	}
	if c.accessKey == "" {
		return fmt.Errorf("missing required QINIU_ACCESS_KEY or -qiniu-access-key")
	}
	if c.secretKey == "" {
		return fmt.Errorf("missing required QINIU_SECRET_KEY or -qiniu-secret-key")
	}
	if c.bucket == "" {
		return fmt.Errorf("missing required QINIU_BUCKET or -qiniu-bucket")
	}
	if _, err := parseHTTPURL(c.publicDomain); err != nil {
		return fmt.Errorf("-qiniu-public-domain: %w", err)
	}
	if c.matrix == "" {
		return fmt.Errorf("missing required -matrix")
	}
	if _, err := parseTarget(c.target); err != nil {
		return fmt.Errorf("-target: %w", err)
	}
	targets, err := parseTargets(c.sharedTargets)
	if err != nil {
		return fmt.Errorf("-shared-targets: %w", err)
	}
	if len(targets) < 2 {
		return fmt.Errorf("-shared-targets must contain at least two targets")
	}
	if c.timeout <= 0 {
		return fmt.Errorf("-timeout must be positive")
	}
	if _, err := os.Stat(filepath.Join(c.formulaRoot, "madler", "zlib", "versions.json")); err != nil {
		return fmt.Errorf("formula root %s: %w", c.formulaRoot, err)
	}
	return nil
}

func run(cfg config) error {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	target, err := parseTarget(cfg.target)
	if err != nil {
		return err
	}
	sharedTargets, err := parseTargets(cfg.sharedTargets)
	if err != nil {
		return err
	}

	runPrefix := cfg.prefix
	if runPrefix != "" {
		runPrefix += "/"
	}
	runPrefix += fmt.Sprintf("llar-kodo-e2e/%d", time.Now().UnixNano())

	db, cleanupDB, err := openDatabase(cfg.postgresDSN)
	if err != nil {
		return err
	}
	defer cleanupDB()
	if err := resetDatabase(ctx, db); err != nil {
		return err
	}
	artifacts, err := artifact.NewGormStore(db)
	if err != nil {
		return fmt.Errorf("NewGormStore: %w", err)
	}
	if count := artifactCount(ctx, db); count != 0 {
		return fmt.Errorf("artifact count after reset = %d, want 0", count)
	}

	kodo := newKodoClient(cfg)
	if err := kodo.deletePrefix(ctx, runPrefix); err != nil {
		return fmt.Errorf("cleanup kodo prefix before run: %w", err)
	}
	defer func() {
		if err := kodo.deletePrefix(context.Background(), runPrefix); err != nil {
			log.Printf("cleanup kodo prefix after run: %v", err)
		}
	}()

	s := suite{
		cfg: configData{
			accessKey:     cfg.accessKey,
			secretKey:     cfg.secretKey,
			bucket:        cfg.bucket,
			publicDomain:  cfg.publicDomain,
			prefix:        runPrefix,
			formulaRoot:   cfg.formulaRoot,
			target:        target,
			sharedTargets: sharedTargets,
			matrix:        cfg.matrix,
		},
		formulas:  newLocalFormulaStore(cfg.formulaRoot),
		artifacts: artifacts,
		db:        db,
		kodo:      kodo,
	}

	for _, target := range append([]module.Version{target}, sharedTargets...) {
		if err := validateLocalFormula(cfg.formulaRoot, target); err != nil {
			return err
		}
	}

	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"cold build uploads and stores artifact", s.coldBuild},
		{"repeated build uses stored artifact", s.repeatedBuild},
		{"new builder instance uses persisted artifact cache", s.persistedCache},
		{"different matrix stores independent artifact", s.differentMatrix},
		{"concurrent duplicate build stores one artifact", s.concurrentDuplicate},
		{"concurrent targets sharing dependency both complete", s.concurrentSharedDependency},
	} {
		start := time.Now()
		log.Printf("RUN %s", step.name)
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		log.Printf("PASS %s (%s)", step.name, time.Since(start).Round(time.Millisecond))
	}

	wantArtifacts := int64(6)
	if count := artifactCount(ctx, db); count != wantArtifacts {
		return fmt.Errorf("artifact count after E2E cases = %d, want %d", count, wantArtifacts)
	}
	if count, err := kodo.objectCount(ctx, runPrefix); err != nil {
		return err
	} else if count != wantArtifacts {
		return fmt.Errorf("kodo object count under %s = %d, want %d", runPrefix, count, wantArtifacts)
	}
	log.Printf("PASS Kodo build E2E (%d artifacts)", wantArtifacts)
	return nil
}

type configData struct {
	accessKey     string
	secretKey     string
	bucket        string
	publicDomain  string
	prefix        string
	formulaRoot   string
	target        module.Version
	sharedTargets []module.Version
	matrix        string
}

type suite struct {
	cfg       configData
	formulas  *localFormulaStore
	artifacts artifact.Store
	db        *gorm.DB
	kodo      *kodoClient

	baseCache     *countingCache
	baseWorkspace string
	baseResult    build.Result
	baseArtifact  artifact.Artifact
}

func (s *suite) coldBuild(ctx context.Context) error {
	s.baseWorkspace = mustTempDir("llar-kodo-e2e-workspace-")
	s.baseCache = s.newCache(s.baseWorkspace)

	got, err := s.build(ctx, s.cfg.target, s.cfg.matrix, s.baseWorkspace, s.baseCache)
	if err != nil {
		return err
	}
	if s.baseCache.totalPuts() != 1 {
		return fmt.Errorf("cache Put calls = %d, want 1", s.baseCache.totalPuts())
	}
	if got.Metadata != "-lz" {
		return fmt.Errorf("metadata = %q, want -lz", got.Metadata)
	}
	if err := assertZlibOutput(got.OutputDir); err != nil {
		return err
	}
	key := cacheKey(s.cfg.target, s.cfg.matrix)
	art, err := s.assertStoredArtifact(ctx, key, "-lz", nil)
	if err != nil {
		return err
	}
	s.baseResult = got
	s.baseArtifact = art
	return nil
}

func (s *suite) repeatedBuild(ctx context.Context) error {
	got, err := s.build(ctx, s.cfg.target, s.cfg.matrix, s.baseWorkspace, s.baseCache)
	if err != nil {
		return err
	}
	if got != s.baseResult {
		return fmt.Errorf("result = %+v, want %+v", got, s.baseResult)
	}
	if s.baseCache.totalPuts() != 1 {
		return fmt.Errorf("cache Put calls after cache hit = %d, want 1", s.baseCache.totalPuts())
	}
	art, err := s.assertStoredArtifact(ctx, cacheKey(s.cfg.target, s.cfg.matrix), "-lz", nil)
	if err != nil {
		return err
	}
	if art != s.baseArtifact {
		return fmt.Errorf("stored artifact changed = %+v, want %+v", art, s.baseArtifact)
	}
	return nil
}

func (s *suite) persistedCache(ctx context.Context) error {
	workspace := mustTempDir("llar-kodo-e2e-persisted-")
	c := s.newCache(workspace)
	got, err := s.build(ctx, s.cfg.target, s.cfg.matrix, workspace, c)
	if err != nil {
		return err
	}
	if got.Metadata != s.baseResult.Metadata {
		return fmt.Errorf("metadata = %q, want %q", got.Metadata, s.baseResult.Metadata)
	}
	if c.totalPuts() != 0 {
		return fmt.Errorf("cache Put calls = %d, want 0", c.totalPuts())
	}
	if err := assertZlibOutput(got.OutputDir); err != nil {
		return err
	}
	return nil
}

func (s *suite) differentMatrix(ctx context.Context) error {
	matrix := s.cfg.matrix + "-variant"
	workspace := mustTempDir("llar-kodo-e2e-matrix-")
	c := s.newCache(workspace)
	got, err := s.build(ctx, s.cfg.target, matrix, workspace, c)
	if err != nil {
		return err
	}
	if got.Metadata != "-lz" {
		return fmt.Errorf("metadata = %q, want -lz", got.Metadata)
	}
	if c.totalPuts() != 1 {
		return fmt.Errorf("cache Put calls = %d, want 1", c.totalPuts())
	}
	if _, err := s.assertStoredArtifact(ctx, cacheKey(s.cfg.target, matrix), "-lz", nil); err != nil {
		return err
	}
	return nil
}

func (s *suite) concurrentDuplicate(ctx context.Context) error {
	matrix := s.cfg.matrix + "-concurrent"
	key := cacheKey(s.cfg.target, matrix)
	workspace1 := mustTempDir("llar-kodo-e2e-concurrent-")
	workspace2 := mustTempDir("llar-kodo-e2e-concurrent-")
	c1 := s.newCache(workspace1)
	c2 := s.newCache(workspace2)

	results := make(chan buildResult, 2)
	start := make(chan struct{})
	go func() {
		<-start
		got, err := s.build(ctx, s.cfg.target, matrix, workspace1, c1)
		results <- buildResult{result: got, err: err}
	}()
	go func() {
		<-start
		got, err := s.build(ctx, s.cfg.target, matrix, workspace2, c2)
		results <- buildResult{result: got, err: err}
	}()
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
		return fmt.Errorf("first build: %w", first.err)
	}
	if second.err != nil {
		return fmt.Errorf("second build: %w", second.err)
	}
	if first.result.Metadata != second.result.Metadata {
		return fmt.Errorf("concurrent metadata = %q, want %q", second.result.Metadata, first.result.Metadata)
	}
	if err := assertZlibOutput(first.result.OutputDir); err != nil {
		return err
	}
	if err := assertZlibOutput(second.result.OutputDir); err != nil {
		return err
	}
	if total := c1.totalPuts() + c2.totalPuts(); total != 2 {
		return fmt.Errorf("cache Put calls = %d, want 2", total)
	}
	if _, err := s.assertStoredArtifact(ctx, key, "-lz", nil); err != nil {
		return err
	}
	return nil
}

func (s *suite) concurrentSharedDependency(ctx context.Context) error {
	matrix := s.cfg.matrix + "-shareddep"
	workspace := mustTempDir("llar-kodo-e2e-shared-")
	c := s.newCache(workspace)

	results := make(chan namedBuildResult, len(s.cfg.sharedTargets))
	start := make(chan struct{})
	for _, target := range s.cfg.sharedTargets {
		go func(target module.Version) {
			<-start
			got, err := s.build(ctx, target, matrix, workspace, c)
			results <- namedBuildResult{target: target, result: got, err: err}
		}(target)
	}
	close(start)

	gotByTarget := make(map[string]build.Result, len(s.cfg.sharedTargets))
	for range s.cfg.sharedTargets {
		result, err := waitNamedBuildResult(ctx, results)
		if err != nil {
			return err
		}
		if result.err != nil {
			return fmt.Errorf("build %s@%s: %w", result.target.Path, result.target.Version, result.err)
		}
		gotByTarget[targetKey(result.target)] = result.result
	}
	if c.totalPuts() != 4 {
		return fmt.Errorf("cache Put calls = %d, want 4", c.totalPuts())
	}

	zlib := module.Version{Path: "madler/zlib", Version: "v1.3.1"}
	if c.putCount(cacheKey(zlib, matrix)) != 2 {
		return fmt.Errorf("shared dependency Put calls = %d, want 2", c.putCount(cacheKey(zlib, matrix)))
	}
	if _, err := s.assertStoredArtifact(ctx, cacheKey(zlib, matrix), "-lz", nil); err != nil {
		return err
	}

	for _, target := range s.cfg.sharedTargets {
		got, ok := gotByTarget[targetKey(target)]
		if !ok {
			return fmt.Errorf("missing result for %s", targetKey(target))
		}
		wantMetadata := map[string]string{
			"DaveGamble/cJSON": "-lcjson",
			"pnggroup/libpng":  "-lpng",
		}[target.Path]
		if got.Metadata != wantMetadata {
			return fmt.Errorf("%s metadata = %q, want %q", targetKey(target), got.Metadata, wantMetadata)
		}
		if _, err := s.assertStoredArtifact(ctx, cacheKey(target, matrix), wantMetadata, []module.Version{zlib}); err != nil {
			return err
		}
	}
	return nil
}

func (s *suite) build(ctx context.Context, target module.Version, matrix, workspaceDir string, c buildcache.Cache) (build.Result, error) {
	mods, err := modules.Load(ctx, target, modules.Options{FormulaStore: s.formulas})
	if err != nil {
		return build.Result{}, fmt.Errorf("modules.Load %s: %w", targetKey(target), err)
	}
	builder, err := build.NewBuilder(build.Options{
		Store:        s.formulas,
		MatrixStr:    matrix,
		WorkspaceDir: workspaceDir,
		Cache:        c,
	})
	if err != nil {
		return build.Result{}, fmt.Errorf("NewBuilder: %w", err)
	}
	results, err := builder.Build(ctx, mods)
	if err != nil {
		return build.Result{}, fmt.Errorf("Build %s: %w", targetKey(target), err)
	}
	if len(results) == 0 {
		return build.Result{}, fmt.Errorf("Build %s returned no results", targetKey(target))
	}
	return results[len(results)-1], nil
}

func (s *suite) newCache(workspaceDir string) *countingCache {
	return &countingCache{
		inner: buildcache.NewKodo(buildcache.KodoConfig{
			AccessKey:    s.cfg.accessKey,
			SecretKey:    s.cfg.secretKey,
			Bucket:       s.cfg.bucket,
			PublicDomain: s.cfg.publicDomain,
			Prefix:       s.cfg.prefix,
			WorkspaceDir: workspaceDir,
			Artifacts:    s.artifacts,
		}),
	}
}

func (s *suite) assertStoredArtifact(ctx context.Context, key buildcache.Key, metadata string, deps []module.Version) (artifact.Artifact, error) {
	got, ok, err := s.artifacts.Get(ctx, artifact.Key{
		Module:    key.Module.Path,
		Version:   key.Module.Version,
		MatrixStr: key.Matrix,
	})
	if err != nil {
		return artifact.Artifact{}, fmt.Errorf("Get stored artifact %s: %w", keyString(key), err)
	}
	if !ok {
		return artifact.Artifact{}, fmt.Errorf("stored artifact missing for %s", keyString(key))
	}
	if got.Source.Type != "kodo" {
		return artifact.Artifact{}, fmt.Errorf("source type = %q, want kodo", got.Source.Type)
	}
	if got.Type != "tar.gz" {
		return artifact.Artifact{}, fmt.Errorf("artifact type = %q, want tar.gz", got.Type)
	}
	if got.Metadata != metadata {
		return artifact.Artifact{}, fmt.Errorf("artifact metadata = %q, want %q", got.Metadata, metadata)
	}
	if len(got.Checksum) != 64 {
		return artifact.Artifact{}, fmt.Errorf("artifact checksum = %q, want sha256 hex", got.Checksum)
	}

	objectName, err := parseKodoSourceURL(got.Source.URL)
	if err != nil {
		return artifact.Artifact{}, err
	}
	wantObject := objectNameFor(s.cfg.prefix, key)
	if objectName != wantObject {
		return artifact.Artifact{}, fmt.Errorf("artifact object = %q, want %q", objectName, wantObject)
	}
	wantURL := publicURL(s.cfg.publicDomain, wantObject)
	if got.Source.URL != wantURL {
		return artifact.Artifact{}, fmt.Errorf("artifact source url = %q, want %q", got.Source.URL, wantURL)
	}

	entry, err := s.kodo.entry(ctx, objectName)
	if err != nil {
		return artifact.Artifact{}, err
	}
	if entry.Metadata != metadata {
		return artifact.Artifact{}, fmt.Errorf("kodo entry metadata = %q, want %q", entry.Metadata, metadata)
	}
	if !slices.Equal(entry.Deps, deps) {
		return artifact.Artifact{}, fmt.Errorf("kodo entry deps = %+v, want %+v", entry.Deps, deps)
	}
	if err := s.kodo.assertChecksum(ctx, objectName, got.Checksum); err != nil {
		return artifact.Artifact{}, err
	}
	if err := assertPublicURLChecksum(ctx, got.Source.URL, got.Checksum); err != nil {
		return artifact.Artifact{}, err
	}
	return got, nil
}

type localFormulaStore struct {
	root string
}

func newLocalFormulaStore(root string) *localFormulaStore {
	return &localFormulaStore{root: root}
}

func (s *localFormulaStore) ModuleFS(ctx context.Context, modPath string) (fs.FS, error) {
	dir := filepath.Join(s.root, filepath.FromSlash(modPath))
	if _, err := os.Stat(filepath.Join(dir, "versions.json")); err != nil {
		return nil, fmt.Errorf("local formula %s: %w", modPath, err)
	}
	return os.DirFS(dir), nil
}

func (s *localFormulaStore) LockModule(modPath string) (func(), error) {
	if modPath == "" {
		return nil, fmt.Errorf("empty module path")
	}
	return func() {}, nil
}

type countingCache struct {
	inner buildcache.Cache
	mu    sync.Mutex
	puts  map[buildcache.Key]int
}

func (c *countingCache) Get(ctx context.Context, key buildcache.Key) (buildcache.Entry, bool, error) {
	return c.inner.Get(ctx, key)
}

func (c *countingCache) Put(ctx context.Context, key buildcache.Key, output fs.FS, entry buildcache.Entry) (buildcache.Entry, error) {
	got, err := c.inner.Put(ctx, key, output, entry)
	if err != nil {
		return buildcache.Entry{}, err
	}
	c.mu.Lock()
	if c.puts == nil {
		c.puts = make(map[buildcache.Key]int)
	}
	c.puts[key]++
	c.mu.Unlock()
	return got, nil
}

func (c *countingCache) putCount(key buildcache.Key) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.puts[key]
}

func (c *countingCache) totalPuts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int
	for _, count := range c.puts {
		n += count
	}
	return n
}

type kodoClient struct {
	bucket     string
	objects    *objects.ObjectsManager
	downloader *qiniudownloader.DownloadManager
}

func newKodoClient(cfg config) *kodoClient {
	cred := credentials.NewCredentials(cfg.accessKey, cfg.secretKey)
	options := httpclient.Options{Credentials: cred}
	return &kodoClient{
		bucket: cfg.bucket,
		objects: objects.NewObjectsManager(&objects.ObjectsManagerOptions{
			Options: options,
		}),
		downloader: qiniudownloader.NewDownloadManager(&qiniudownloader.DownloadManagerOptions{
			Options: options,
		}),
	}
}

func (c *kodoClient) entry(ctx context.Context, objectName string) (buildcache.Entry, error) {
	object, err := c.objects.Bucket(c.bucket).Object(objectName).Stat().Call(ctx)
	if err != nil {
		return buildcache.Entry{}, fmt.Errorf("stat kodo object %s: %w", objectName, err)
	}
	raw := object.Metadata[kodoEntryMetadataKey]
	if raw == "" {
		raw = object.Metadata["x-qn-meta-"+kodoEntryMetadataKey]
	}
	if raw == "" {
		return buildcache.Entry{}, fmt.Errorf("kodo object %s missing %s metadata", objectName, kodoEntryMetadataKey)
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return buildcache.Entry{}, fmt.Errorf("decode kodo entry metadata for %s: %w", objectName, err)
	}
	var entry buildcache.Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return buildcache.Entry{}, fmt.Errorf("unmarshal kodo entry metadata for %s: %w", objectName, err)
	}
	return entry, nil
}

func (c *kodoClient) assertChecksum(ctx context.Context, objectName, checksum string) error {
	file, err := os.CreateTemp("", "llar-kodo-e2e-download-*.tar.gz")
	if err != nil {
		return err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	defer os.Remove(name)

	if _, err := c.downloader.DownloadToFile(ctx, objectName, name, &qiniudownloader.ObjectOptions{
		GenerateOptions: qiniudownloader.GenerateOptions{
			BucketName: c.bucket,
		},
	}); err != nil {
		return fmt.Errorf("download kodo object %s: %w", objectName, err)
	}
	got, err := fileSHA256(name)
	if err != nil {
		return err
	}
	if got != checksum {
		return fmt.Errorf("kodo object %s checksum = %s, want %s", objectName, got, checksum)
	}
	return nil
}

func (c *kodoClient) objectCount(ctx context.Context, prefix string) (int64, error) {
	lister := c.objects.Bucket(c.bucket).List(ctx, &objects.ListObjectsOptions{Prefix: prefix})
	defer lister.Close()
	var count int64
	var details objects.ObjectDetails
	for lister.Next(&details) {
		count++
	}
	if err := lister.Error(); err != nil {
		return 0, fmt.Errorf("list kodo prefix %s: %w", prefix, err)
	}
	return count, nil
}

func (c *kodoClient) deletePrefix(ctx context.Context, prefix string) error {
	lister := c.objects.Bucket(c.bucket).List(ctx, &objects.ListObjectsOptions{Prefix: prefix})
	var names []string
	var details objects.ObjectDetails
	for lister.Next(&details) {
		names = append(names, details.Name)
	}
	if err := lister.Close(); err != nil {
		return fmt.Errorf("list kodo prefix %s: %w", prefix, err)
	}
	for _, name := range names {
		if err := c.objects.Bucket(c.bucket).Object(name).Delete().Call(ctx); err != nil && !isKodoObjectNotFound(err) {
			return fmt.Errorf("delete kodo object %s: %w", name, err)
		}
	}
	return nil
}

func openDatabase(dsn string) (*gorm.DB, func(), error) {
	var dial gorm.Dialector
	var cleanup func()
	if strings.TrimSpace(dsn) == "" {
		name := filepath.Join(mustTempDir("llar-kodo-e2e-db-"), "artifacts.db")
		dial = sqlite.Open(name)
		cleanup = func() {}
	} else {
		dial = postgres.Open(dsn)
		cleanup = func() {}
	}
	db, err := gorm.Open(dial, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, cleanup, fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, cleanup, fmt.Errorf("database handle: %w", err)
	}
	return db, func() {
		_ = sqlDB.Close()
		cleanup()
	}, nil
}

func resetDatabase(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec("DROP TABLE IF EXISTS artifacts").Error; err != nil {
		return fmt.Errorf("drop artifacts table: %w", err)
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

func validateLocalFormula(root string, target module.Version) error {
	dir := filepath.Join(root, filepath.FromSlash(target.Path))
	if _, err := os.Stat(filepath.Join(dir, "versions.json")); err != nil {
		return fmt.Errorf("local formula for %s: %w", target.Path, err)
	}
	return nil
}

func parseTarget(value string) (module.Version, error) {
	path, version, ok := strings.Cut(strings.TrimSpace(value), "@")
	if !ok || path == "" || version == "" {
		return module.Version{}, fmt.Errorf("target must be module@version, got %q", value)
	}
	return module.Version{Path: path, Version: version}, nil
}

func parseTargets(value string) ([]module.Version, error) {
	parts := strings.Split(value, ",")
	targets := make([]module.Version, 0, len(parts))
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

func cacheKey(target module.Version, matrix string) buildcache.Key {
	return buildcache.Key{Module: target, Matrix: matrix}
}

func keyString(key buildcache.Key) string {
	return fmt.Sprintf("%s@%s matrix=%s", key.Module.Path, key.Module.Version, key.Matrix)
}

func targetKey(target module.Version) string {
	return target.Path + "@" + target.Version
}

func objectNameFor(prefix string, key buildcache.Key) string {
	parts := make([]string, 0, 4)
	if prefix != "" {
		parts = append(parts, strings.Trim(prefix, "/"))
	}
	parts = append(parts, strings.Trim(key.Module.Path, "/"), strings.Trim(key.Module.Version, "/"), key.Matrix+".tar.gz")
	return strings.Join(parts, "/")
}

func normalizePublicDomain(domain string) string {
	domain = strings.TrimRight(strings.TrimSpace(domain), "/")
	if domain == "" || strings.Contains(domain, "://") {
		return domain
	}
	return "http://" + domain
}

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("must be http(s), got %q", raw)
	}
	return u, nil
}

func parseKodoSourceURL(raw string) (string, error) {
	u, err := parseHTTPURL(raw)
	if err != nil {
		return "", err
	}
	objectName, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil {
		return "", err
	}
	if objectName == "" {
		return "", fmt.Errorf("invalid kodo source url %q", raw)
	}
	return objectName, nil
}

func publicURL(domain, objectName string) string {
	u, _ := parseHTTPURL(domain)
	u.Path = "/" + objectName
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func assertZlibOutput(dir string) error {
	for _, name := range []string{
		filepath.Join("include", "zlib.h"),
		filepath.Join("lib", "libz.a"),
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("zlib output %s missing in %s: %w", name, dir, err)
		}
	}
	return nil
}

func isKodoObjectNotFound(err error) bool {
	var info *qiniuclient.ErrorInfo
	return errors.As(err, &info) && info.Code == 612
}

func fileSHA256(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func assertPublicURLChecksum(ctx context.Context, rawURL, checksum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, resp.Body); err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != checksum {
		return fmt.Errorf("GET %s checksum = %s, want %s", rawURL, got, checksum)
	}
	return nil
}

func mustTempDir(pattern string) string {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		panic(err)
	}
	return dir
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func hostMatrix() string {
	return runtime.GOARCH + "-" + runtime.GOOS
}

type buildResult struct {
	result build.Result
	err    error
}

type namedBuildResult struct {
	target module.Version
	result build.Result
	err    error
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
