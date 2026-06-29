package build

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/upload"
)

func TestBuildDoesNotExportMakeImplementationDetails(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "build.go", nil, 0)
	if err != nil {
		t.Fatalf("parse build.go: %v", err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec := spec.(*ast.TypeSpec)
			switch typeSpec.Name.Name {
			case "Maker", "MakeResult", "Runner", "RunResult", "Result":
				t.Fatalf("%s should stay package-private", typeSpec.Name.Name)
			}
		}
	}
}

func TestBuildDoesNotKeepTrivialHelpers(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "build.go", nil, 0)
	if err != nil {
		t.Fatalf("parse build.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "resultFor", "artifactKey", "targetString", "uploadName":
			t.Fatalf("%s should be inlined", fn.Name.Name)
		}
	}
}

func TestBuildUsesSingleflightForDuplicateSuppression(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "build.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse build.go: %v", err)
	}
	for _, imp := range file.Imports {
		if imp.Path.Value == `"golang.org/x/sync/singleflight"` {
			return
		}
	}
	t.Fatal("build.go should use golang.org/x/sync/singleflight for duplicate suppression")
}

func TestE2ELLARMakeUsesRequiredLinkerFlag(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../../../testdata/remote-build-e2e/main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse remote-build-e2e runner: %v", err)
	}
	hasRequiredFlag := false
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if value == "-ldflags=-checklinkname=0" {
			hasRequiredFlag = true
		}
		return true
	})
	if !hasRequiredFlag {
		t.Fatal("llar make E2E should use go run with -ldflags=-checklinkname=0")
	}
}

func TestE2EResetsDatabaseAtStartup(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../../../testdata/remote-build-e2e/main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse remote-build-e2e runner: %v", err)
	}
	resetsDatabase := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "resetDatabase" {
			return true
		}
		resetsDatabase = true
		return true
	})
	if !resetsDatabase {
		t.Fatal("remote build E2E should reset the database during test init")
	}
}

func TestE2EDoesNotDeleteArtifacts(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../../../testdata/remote-build-e2e/main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse remote-build-e2e runner: %v", err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Delete" {
			return true
		}
		t.Fatalf("remote build E2E should leave artifacts in the database")
		return false
	})
}

func TestLLARMakerDelegatesTarGzOutputToMakeCommand(t *testing.T) {
	maker := &llarMaker{
		command: os.Args[0],
		args: []string{
			"-test.run=TestLLARMakeHelperProcess",
			"--",
		},
	}
	t.Setenv("LLAR_MAKE_HELPER_PROCESS", "1")

	got, err := maker.make(context.Background(), testRequest(), nil)
	if err != nil {
		t.Fatalf("make: %v", err)
	}
	if got.Type != "tar.gz" {
		t.Fatalf("type = %q, want tar.gz", got.Type)
	}
	if got.Metadata != "-lz" {
		t.Fatalf("metadata = %q, want -lz", got.Metadata)
	}
	archive, err := io.ReadAll(got.Archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if string(archive) != "archive" {
		t.Fatalf("archive = %q, want archive", archive)
	}
}

func TestRemoteBuildDoesNotOwnTarGzImplementation(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "make.go", nil, 0)
	if err != nil {
		t.Fatalf("parse make.go: %v", err)
	}
	for _, imp := range file.Imports {
		switch strings.Trim(imp.Path.Value, `"`) {
		case "archive/tar", "compress/gzip":
			t.Fatalf("make.go should delegate tar.gz output to llar make -o, not import %s", imp.Path.Value)
		}
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "writeTarGz" {
			t.Fatal("make.go should not keep its own writeTarGz implementation")
		}
	}
}

func TestBuildReturnsCompletedArtifactBeforeEntryLookup(t *testing.T) {
	req := testRequest()
	key := artifact.Key{
		Module:    req.Target.Module,
		Version:   req.Target.Version,
		MatrixStr: req.MatrixStr,
	}
	completed := artifact.Artifact{
		Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/owner/madler/zlib/blobs/sha256:stored"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "stored",
	}
	store := newFakeStore()
	store.artifacts[key] = completed
	maker := &fakeMaker{
		makeFn: func(context.Context, Request, io.Writer) (makeResult, error) {
			t.Fatal("maker should not be called for completed artifact")
			return makeResult{}, nil
		},
	}
	uploader := &fakeUploader{}

	got, err := newBuilds(Options{
		Store:    store,
		Uploader: uploader,
	}, maker).Build(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []TargetArtifact{{
		Target:   "madler/zlib@v1.3.1",
		Artifact: completed,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v, want %+v", got, want)
	}
	if maker.Calls() != 0 {
		t.Fatalf("maker calls = %d, want 0", maker.Calls())
	}
	if uploader.Calls() != 0 {
		t.Fatalf("uploader calls = %d, want 0", uploader.Calls())
	}
}

func TestBuildJoinsSingleflight(t *testing.T) {
	req := testRequest()
	store := newFakeStore()
	uploader := &fakeUploader{
		result: upload.Result{
			URL:      "https://ghcr.io/v2/owner/madler/zlib/blobs/sha256:abc",
			Checksum: "abc",
		},
	}
	started := make(chan struct{})
	release := make(chan struct{})
	maker := &fakeMaker{
		makeFn: func(ctx context.Context, req Request, info io.Writer) (makeResult, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return makeResult{}, ctx.Err()
			}
			return makeResult{
				Archive:  bytes.NewReader([]byte("archive")),
				Type:     "tar.gz",
				Metadata: "-lz",
			}, nil
		},
	}
	builds := newBuilds(Options{
		Store:    store,
		Uploader: uploader,
	}, maker)

	results := make(chan buildCall, 2)
	go func() {
		result, err := builds.Build(context.Background(), req, nil)
		results <- buildCall{result: result, err: err}
	}()
	waitForSignal(t, started, "maker start")

	go func() {
		result, err := builds.Build(context.Background(), req, nil)
		results <- buildCall{result: result, err: err}
	}()
	waitForStoreGetCalls(t, store, 2)
	close(release)

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
	if maker.Calls() != 1 {
		t.Fatalf("maker calls = %d, want 1", maker.Calls())
	}
	if uploader.Calls() != 1 {
		t.Fatalf("uploader calls = %d, want 1", uploader.Calls())
	}
}

func TestBuildUsesArtifactReturnedByPut(t *testing.T) {
	req := testRequest()
	canonical := artifact.Artifact{
		Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/owner/madler/zlib/blobs/sha256:stored"},
		Type:     "tar.gz",
		Metadata: "-lz-canonical",
		Checksum: "stored",
	}
	store := newFakeStore()
	store.put = func(key artifact.Key, candidate artifact.Artifact) (artifact.Artifact, error) {
		wantCandidate := artifact.Artifact{
			Source:   artifact.Source{Type: "ghcr", URL: "https://ghcr.io/v2/owner/madler/zlib/blobs/sha256:candidate"},
			Type:     "tar.gz",
			Metadata: "-lz",
			Checksum: "candidate",
		}
		if candidate != wantCandidate {
			t.Fatalf("Put candidate = %+v, want %+v", candidate, wantCandidate)
		}
		return canonical, nil
	}
	uploader := &fakeUploader{
		result: upload.Result{
			URL:      "https://ghcr.io/v2/owner/madler/zlib/blobs/sha256:candidate",
			Checksum: "candidate",
		},
	}
	maker := &fakeMaker{
		makeFn: func(context.Context, Request, io.Writer) (makeResult, error) {
			return makeResult{
				Archive:  bytes.NewReader([]byte("archive")),
				Type:     "tar.gz",
				Metadata: "-lz",
			}, nil
		},
	}

	got, err := newBuilds(Options{
		Store:    store,
		Uploader: uploader,
	}, maker).Build(context.Background(), req, nil)
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
	wantAttrs := map[string]string{
		"org.llar.matrix": req.MatrixStr,
		"os":              "linux",
		"arch":            "amd64",
	}
	if !reflect.DeepEqual(opts[0].Attrs, wantAttrs) {
		t.Fatalf("upload attrs = %+v, want %+v", opts[0].Attrs, wantAttrs)
	}
}

func TestBuildOmitsUploadAttrsForUnknownType(t *testing.T) {
	req := testRequest()
	store := newFakeStore()
	uploader := &fakeUploader{
		typ: "file",
		result: upload.Result{
			URL:      "file:///tmp/archive.tar.gz",
			Checksum: "candidate",
		},
	}
	maker := &fakeMaker{
		makeFn: func(context.Context, Request, io.Writer) (makeResult, error) {
			return makeResult{
				Archive:  bytes.NewReader([]byte("archive")),
				Type:     "tar.gz",
				Metadata: "-lz",
			}, nil
		},
	}

	_, err := newBuilds(Options{
		Store:    store,
		Uploader: uploader,
	}, maker).Build(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	opts := uploader.Options()
	if len(opts) != 1 {
		t.Fatalf("upload calls = %d, want 1", len(opts))
	}
	if len(opts[0].Attrs) != 0 {
		t.Fatalf("upload attrs = %+v, want empty", opts[0].Attrs)
	}
}

type buildCall struct {
	result []TargetArtifact
	err    error
}

func testRequest() Request {
	return Request{
		Target:    Target{Module: "madler/zlib", Version: "v1.3.1"},
		MatrixStr: "amd64-linux",
		Matrix: Matrix{
			Require: map[string]string{"arch": "amd64", "os": "linux"},
		},
	}
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

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForBuildCall(t *testing.T, ch <-chan buildCall) buildCall {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Build")
		return buildCall{}
	}
}

func waitForStoreGetCalls(t *testing.T, store *fakeStore, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if store.GetCalls() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("store Get calls = %d, want at least %d", store.GetCalls(), want)
}

type fakeStore struct {
	mu        sync.Mutex
	artifacts map[artifact.Key]artifact.Artifact
	put       func(artifact.Key, artifact.Artifact) (artifact.Artifact, error)
	getCalls  int
	putCalls  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{artifacts: map[artifact.Key]artifact.Artifact{}}
}

func (s *fakeStore) Get(ctx context.Context, key artifact.Key) (artifact.Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	got, ok := s.artifacts[key]
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

func (s *fakeStore) Delete(ctx context.Context, key artifact.Key) error {
	return errors.New("Delete should not be called")
}

func (s *fakeStore) GetCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

type fakeUploader struct {
	mu      sync.Mutex
	typ     string
	result  upload.Result
	options []upload.Options
}

func (u *fakeUploader) Type() string {
	if u.typ == "" {
		return "ghcr"
	}
	return u.typ
}

func (u *fakeUploader) Upload(ctx context.Context, r io.ReadSeeker, opts upload.Options) (upload.Result, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.options = append(u.options, opts)
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

type fakeMaker struct {
	mu     sync.Mutex
	calls  int
	makeFn func(context.Context, Request, io.Writer) (makeResult, error)
}

func (m *fakeMaker) make(ctx context.Context, req Request, info io.Writer) (makeResult, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return m.makeFn(ctx, req, info)
}

func (m *fakeMaker) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}
