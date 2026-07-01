package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/upload"
)

func TestBuildReturnsStoredArtifactWithoutRunningMakeOrUpload(t *testing.T) {
	req := testRequest()
	key := artifactKey(req)
	stored := artifact.Artifact{
		Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:stored"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "stored",
	}
	store := newFakeStore()
	store.artifacts[key] = stored
	uploader := &fakeUploader{}
	t.Setenv("PATH", t.TempDir())

	got, err := New(Options{
		Store:    store,
		Uploader: uploader,
	}).Build(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []TargetArtifact{{
		Target:   "madler/zlib@v1.3.1",
		Artifact: stored,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v, want %+v", got, want)
	}
	if uploader.Calls() != 0 {
		t.Fatalf("uploader calls = %d, want 0", uploader.Calls())
	}
	if store.PutCalls() != 0 {
		t.Fatalf("store Put calls = %d, want 0", store.PutCalls())
	}
}

func TestBuildRunsMakeUploadsAndStoresArtifact(t *testing.T) {
	installLLARHelper(t)

	req := testRequest()
	key := artifactKey(req)
	canonical := artifact.Artifact{
		Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:stored"},
		Type:     "tar.gz",
		Metadata: "-lz-canonical",
		Checksum: "stored",
	}
	store := newFakeStore()
	store.put = func(gotKey artifact.Key, candidate artifact.Artifact) (artifact.Artifact, error) {
		if gotKey != key {
			t.Fatalf("Put key = %+v, want %+v", gotKey, key)
		}
		if candidate != (artifact.Artifact{}) {
			t.Fatalf("Put artifact = %+v, want empty placeholder", candidate)
		}
		return artifact.Artifact{}, nil
	}
	store.getOrUpdate = func(gotKey artifact.Key, update func() (artifact.Artifact, error)) (artifact.Artifact, error) {
		if gotKey != key {
			t.Fatalf("GetOrUpdate key = %+v, want %+v", gotKey, key)
		}
		candidate, err := update()
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		wantCandidate := artifact.Artifact{
			Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:candidate"},
			Type:     "tar.gz",
			Metadata: "-lz",
			Checksum: "candidate",
		}
		if candidate != wantCandidate {
			t.Fatalf("updated artifact = %+v, want %+v", candidate, wantCandidate)
		}
		return canonical, nil
	}
	uploader := &fakeUploader{
		result: upload.Result{
			URL:      "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:candidate",
			Checksum: "candidate",
		},
	}

	got, err := New(Options{
		Store:    store,
		Uploader: uploader,
	}).Build(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []TargetArtifact{{
		Target:   "madler/zlib@v1.3.1",
		Artifact: canonical,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v, want %+v", got, want)
	}
	opts := uploader.Options()
	if len(opts) != 1 {
		t.Fatalf("upload calls = %d, want 1", len(opts))
	}
	wantOptions := upload.Options{
		Name: "madler/zlib",
		Tag:  "v1.3.1",
		Type: "tar.gz",
		Attrs: map[string]string{
			"org.llar.matrix": "amd64-linux",
			"os":              "linux",
			"arch":            "amd64",
		},
	}
	if !reflect.DeepEqual(opts[0], wantOptions) {
		t.Fatalf("upload options = %+v, want %+v", opts[0], wantOptions)
	}
	if payloads := uploader.Payloads(); !reflect.DeepEqual(payloads, []string{"archive"}) {
		t.Fatalf("upload payloads = %+v, want [archive]", payloads)
	}
	if store.GetOrUpdateCalls() != 1 {
		t.Fatalf("store GetOrUpdate calls = %d, want 1", store.GetOrUpdateCalls())
	}
	if store.PutCalls() != 1 {
		t.Fatalf("store Put calls = %d, want 1", store.PutCalls())
	}
}

func TestBuildRunsMakeWithLocalTargetPath(t *testing.T) {
	installLLARHelper(t)
	oldAllowLocal := AllowLocal
	AllowLocal = true
	t.Cleanup(func() {
		AllowLocal = oldAllowLocal
	})

	formulaDir := filepath.Join(t.TempDir(), "zlib")
	if err := os.MkdirAll(filepath.Join(formulaDir, "v1.3.1"), 0o755); err != nil {
		t.Fatalf("create formula dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(formulaDir, "versions.json"), []byte(`{"path":"madler/zlib","deps":{}}`), 0o644); err != nil {
		t.Fatalf("write versions.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(formulaDir, "v1.3.1", "Zlib_llar.gox"), []byte(`id "madler/zlib"`), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("LLAR_MAKE_ARGS_FILE", argsFile)
	t.Setenv("LLAR_MAKE_EXPECT_LOCAL_FORMULA", "1")

	req := testRequest()
	req.Target.Module = formulaDir
	uploader := &fakeUploader{
		result: upload.Result{
			URL:      "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:abc",
			Checksum: "abc",
		},
	}
	got, err := New(Options{
		Store:    newFakeStore(),
		Uploader: uploader,
	}).Build(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(got) != 1 || got[0].Target != "madler/zlib@v1.3.1" {
		t.Fatalf("Build target = %+v, want madler/zlib@v1.3.1", got)
	}
	if got := uploader.Options()[0].Name; got != "madler/zlib" {
		t.Fatalf("upload name = %q, want madler/zlib", got)
	}
	if got := uploader.Options()[0].Tag; got != "v1.3.1" {
		t.Fatalf("upload tag = %q, want v1.3.1", got)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read llar args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	target := args[len(args)-1]
	wantPattern := req.Target.Module
	wantPattern, err = filepath.EvalSymlinks(wantPattern)
	if err != nil {
		t.Fatalf("eval make target: %v", err)
	}
	wantTarget := wantPattern + "@v1.3.1"
	if target != wantTarget {
		t.Fatalf("llar make target = %q, want %q", target, wantTarget)
	}
}

func TestBuildRejectsLocalTargetByDefault(t *testing.T) {
	for _, module := range []string{
		filepath.Join(t.TempDir(), "zlib"),
		"./zlib",
	} {
		t.Run(module, func(t *testing.T) {
			req := testRequest()
			req.Target.Module = module

			_, err := New(Options{
				Store:    newFakeStore(),
				Uploader: &fakeUploader{},
			}).Build(context.Background(), req, nil)
			if err == nil || !strings.Contains(err.Error(), "local target module is not allowed") {
				t.Fatalf("Build error = %v, want local target disabled error", err)
			}
		})
	}
}

func TestBuildJoinsConcurrentBuildsForSameArtifactKey(t *testing.T) {
	installLLARHelper(t)

	req := testRequest()
	store := newFakeStore()
	uploader := &fakeUploader{
		result: upload.Result{
			URL:      "https://ghcr.io/v2/meteorsliu/madler/zlib/blobs/sha256:abc",
			Checksum: "abc",
		},
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	calls := filepath.Join(dir, "calls")
	t.Setenv("LLAR_MAKE_START_FILE", started)
	t.Setenv("LLAR_MAKE_RELEASE_FILE", release)
	t.Setenv("LLAR_MAKE_CALLS_FILE", calls)

	builds := New(Options{
		Store:    store,
		Uploader: uploader,
	})
	results := make(chan buildCall, 2)
	go func() {
		result, err := builds.Build(context.Background(), req, nil)
		results <- buildCall{result: result, err: err}
	}()
	waitForFile(t, started, "llar make start")

	go func() {
		result, err := builds.Build(context.Background(), req, nil)
		results <- buildCall{result: result, err: err}
	}()
	waitForStoreGetCalls(t, store, 2)
	if err := os.WriteFile(release, []byte("release"), 0o644); err != nil {
		t.Fatalf("release llar helper: %v", err)
	}

	first := waitForBuildCall(t, results)
	second := waitForBuildCall(t, results)
	if first.err != nil {
		t.Fatalf("first Build: %v", first.err)
	}
	if second.err != nil {
		t.Fatalf("second Build: %v", second.err)
	}
	if !reflect.DeepEqual(first.result, second.result) {
		t.Fatalf("joined result = %+v, want %+v", second.result, first.result)
	}
	if helperCalls(t, calls) != 1 {
		t.Fatalf("llar make calls = %d, want 1", helperCalls(t, calls))
	}
	if uploader.Calls() != 1 {
		t.Fatalf("uploader calls = %d, want 1", uploader.Calls())
	}
	if store.GetOrUpdateCalls() != 1 {
		t.Fatalf("store GetOrUpdate calls = %d, want 1", store.GetOrUpdateCalls())
	}
	if store.PutCalls() != 1 {
		t.Fatalf("store Put calls = %d, want 1", store.PutCalls())
	}
}

func TestBuildRequiresStore(t *testing.T) {
	_, err := New(Options{
		Uploader: &fakeUploader{},
	}).Build(context.Background(), testRequest(), nil)
	if err == nil || !strings.Contains(err.Error(), "build store is required") {
		t.Fatalf("Build error = %v, want missing store error", err)
	}
}

func TestBuildUploadRequiresUploader(t *testing.T) {
	_, _, _, err := (&Builds{}).upload(context.Background(), testRequest(), "madler/zlib", "amd64-linux", makeResult{
		Archive: strings.NewReader("archive"),
		Type:    "tar.gz",
	})
	if err == nil || !strings.Contains(err.Error(), "build uploader is required") {
		t.Fatalf("upload error = %v, want missing uploader error", err)
	}
}

func TestBuildUploadOmitsAttrsForNonGHCRUploader(t *testing.T) {
	uploader := &fakeUploader{
		typ: "file",
		result: upload.Result{
			URL:      "file:///tmp/archive.tar.gz",
			Checksum: "abc",
		},
	}
	_, _, _, err := (&Builds{uploader: uploader}).upload(context.Background(), testRequest(), "madler/zlib", "amd64-linux", makeResult{
		Archive: strings.NewReader("archive"),
		Type:    "tar.gz",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	opts := uploader.Options()
	if len(opts) != 1 {
		t.Fatalf("upload calls = %d, want 1", len(opts))
	}
	if opts[0].Attrs != nil {
		t.Fatalf("upload attrs = %+v, want nil", opts[0].Attrs)
	}
}

type buildCall struct {
	result []TargetArtifact
	err    error
}

func testRequest() Request {
	return Request{
		Target: Target{Module: "madler/zlib", Version: "v1.3.1"},
		Matrix: formula.Matrix{
			Require: map[string][]string{
				"arch": {"amd64"},
				"os":   {"linux"},
			},
		},
	}
}

func artifactKey(req Request) artifact.Key {
	return artifact.Key{
		Module:    req.Target.Module,
		Version:   req.Target.Version,
		MatrixStr: req.Matrix.Combinations()[0],
	}
}

func installLLARHelper(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell wrapper")
	}
	binDir := t.TempDir()
	helperPath := filepath.Join(binDir, "llar")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("find test executable: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestLLARMakeHelperProcess -- \"$@\"\n", exe)
	if err := os.WriteFile(helperPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write llar helper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LLAR_MAKE_HELPER_PROCESS", "1")
}

func TestLLARMakeHelperProcess(t *testing.T) {
	if os.Getenv("LLAR_MAKE_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) < 4 || args[0] != "make" {
		_, _ = os.Stderr.WriteString("usage: llar make -o output target\n")
		os.Exit(2)
	}
	if calls := os.Getenv("LLAR_MAKE_CALLS_FILE"); calls != "" {
		f, err := os.OpenFile(calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(5)
		}
		if _, err := f.WriteString("1\n"); err != nil {
			_ = f.Close()
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(5)
		}
		if err := f.Close(); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(5)
		}
	}
	if argsFile := os.Getenv("LLAR_MAKE_ARGS_FILE"); argsFile != "" {
		if err := os.WriteFile(argsFile, []byte(strings.Join(args, "\n")+"\n"), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(5)
		}
	}
	if os.Getenv("LLAR_MAKE_EXPECT_LOCAL_FORMULA") == "1" {
		target := args[len(args)-1]
		i := strings.LastIndex(target, "@")
		if i < 0 {
			_, _ = os.Stderr.WriteString("expected local formula target, got " + target + "\n")
			os.Exit(7)
		}
		pattern := target[:i]
		if !filepath.IsAbs(pattern) {
			_, _ = os.Stderr.WriteString("expected absolute local formula target, got " + target + "\n")
			os.Exit(7)
		}
		cwd, err := os.Getwd()
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(7)
		}
		rel, err := filepath.Rel(cwd, pattern)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			_, _ = os.Stderr.WriteString("local formula target escapes cwd: " + target + "\n")
			os.Exit(7)
		}
		if _, err := os.Stat(filepath.Join(pattern, "versions.json")); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(7)
		}
	}
	if started := os.Getenv("LLAR_MAKE_START_FILE"); started != "" {
		if err := os.WriteFile(started, []byte("started"), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(5)
		}
	}
	if release := os.Getenv("LLAR_MAKE_RELEASE_FILE"); release != "" {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				break
			}
			if time.Now().After(deadline) {
				_, _ = os.Stderr.WriteString("timed out waiting for release\n")
				os.Exit(6)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	output := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			output = args[i+1]
			break
		}
	}
	if output == "" {
		_, _ = os.Stderr.WriteString("missing -o\n")
		os.Exit(2)
	}
	if !strings.HasSuffix(output, ".tar.gz") {
		_, _ = os.Stderr.WriteString("output must end with .tar.gz: " + output + "\n")
		os.Exit(3)
	}
	if err := os.WriteFile(output, []byte("archive"), 0o644); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(4)
	}
	_, _ = os.Stdout.WriteString("-lz\n")
	os.Exit(0)
}

func waitForBuildCall(t *testing.T, ch <-chan buildCall) buildCall {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Build")
		return buildCall{}
	}
}

func waitForFile(t *testing.T, path, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
}

func waitForStoreGetCalls(t *testing.T, store *fakeStore, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.GetCalls() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("store Get calls = %d, want at least %d", store.GetCalls(), want)
}

func helperCalls(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read helper calls: %v", err)
	}
	return strings.Count(string(data), "\n")
}

type fakeStore struct {
	mu               sync.Mutex
	artifacts        map[artifact.Key]artifact.Artifact
	put              func(artifact.Key, artifact.Artifact) (artifact.Artifact, error)
	getOrUpdate      func(artifact.Key, func() (artifact.Artifact, error)) (artifact.Artifact, error)
	getCalls         int
	putCalls         int
	getOrUpdateCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{artifacts: map[artifact.Key]artifact.Artifact{}}
}

func (s *fakeStore) Get(ctx context.Context, key artifact.Key) (artifact.Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	got, ok := s.artifacts[key]
	if ok && got.Source.URL == "" {
		return artifact.Artifact{}, false, nil
	}
	return got, ok, nil
}

func (s *fakeStore) Put(ctx context.Context, key artifact.Key, value artifact.Artifact) (artifact.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putCalls++
	if s.put != nil {
		return s.put(key, value)
	}
	if got, ok := s.artifacts[key]; ok {
		return got, nil
	}
	s.artifacts[key] = value
	return value, nil
}

func (s *fakeStore) GetOrUpdate(ctx context.Context, key artifact.Key, update func() (artifact.Artifact, error)) (artifact.Artifact, error) {
	s.mu.Lock()
	s.getOrUpdateCalls++
	if got, ok := s.artifacts[key]; ok && got.Source.URL != "" {
		s.mu.Unlock()
		return got, nil
	}
	if s.getOrUpdate != nil {
		s.mu.Unlock()
		return s.getOrUpdate(key, update)
	}
	s.mu.Unlock()
	got, err := update()
	if err != nil {
		return artifact.Artifact{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.artifacts[key]; ok && existing.Source.URL != "" {
		return existing, nil
	}
	s.artifacts[key] = got
	return got, nil
}

func (s *fakeStore) Delete(ctx context.Context, key artifact.Key) error {
	return errors.New("Delete should not be called")
}

func (s *fakeStore) GetCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

func (s *fakeStore) PutCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putCalls
}

func (s *fakeStore) GetOrUpdateCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getOrUpdateCalls
}

type fakeUploader struct {
	mu       sync.Mutex
	typ      string
	result   upload.Result
	options  []upload.Options
	payloads []string
}

func (u *fakeUploader) Type() string {
	if u.typ == "" {
		return "ghcr"
	}
	return u.typ
}

func (u *fakeUploader) Upload(ctx context.Context, r io.ReadSeeker, opts upload.Options) (upload.Result, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return upload.Result{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.options = append(u.options, opts)
	u.payloads = append(u.payloads, string(payload))
	return u.result, nil
}

func (u *fakeUploader) Calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.options)
}

func (u *fakeUploader) Options() []upload.Options {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]upload.Options(nil), u.options...)
}

func (u *fakeUploader) Payloads() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.payloads...)
}
