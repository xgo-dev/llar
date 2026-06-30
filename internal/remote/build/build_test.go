package build

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestBuildOptionsDoesNotExposeMakeCommandConfiguration(t *testing.T) {
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
			if typeSpec.Name.Name != "Options" {
				continue
			}
			st, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("Options is not a struct")
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					switch name.Name {
					case "MakeCommand", "MakeArgs", "MakeWorkDir", "MakeHomeDir":
						t.Fatalf("Options should not expose %s", name.Name)
					}
				}
			}
			return
		}
	}
	t.Fatal("Options type not found")
}

func TestBuildDoesNotKeepMakerAbstraction(t *testing.T) {
	for _, fileName := range []string{"build.go", "make.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), fileName, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", fileName, err)
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.GenDecl:
				if decl.Tok != token.TYPE {
					continue
				}
				for _, spec := range decl.Specs {
					typeSpec := spec.(*ast.TypeSpec)
					switch typeSpec.Name.Name {
					case "maker", "llarMaker":
						t.Fatalf("%s should not keep %s", fileName, typeSpec.Name.Name)
					}
				}
			case *ast.FuncDecl:
				switch decl.Name.Name {
				case "newBuilds", "newLLARMaker":
					t.Fatalf("%s should not keep %s", fileName, decl.Name.Name)
				}
			}
		}
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

func TestRemoteBuildE2EWorkflowDoesNotHardcodeRepository(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/remote-build-e2e.yml")
	if err != nil {
		t.Fatalf("read remote build E2E workflow: %v", err)
	}
	hardcodedRepo := strings.Join([]string{"Meteors", "Liu/llar"}, "")
	if strings.Contains(string(data), hardcodedRepo) {
		t.Fatal("remote build E2E workflow should run in any repository")
	}
}

func TestRemoteBuildE2EDoesNotHardcodeGHCROwner(t *testing.T) {
	for _, path := range []string{
		"../../../testdata/remote-build-e2e/main.go",
		"../../../.github/workflows/remote-build-e2e.yml",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		hardcodedOwner := strings.Join([]string{"Meteors", "Liu"}, "")
		if strings.Contains(string(data), hardcodedOwner) {
			t.Fatalf("%s should not hardcode GHCR owner", path)
		}
	}
}

func TestRemoteBuildE2EWorkflowPassesGHCROwnerFromEnv(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/remote-build-e2e.yml")
	if err != nil {
		t.Fatalf("read remote build E2E workflow: %v", err)
	}
	workflow := string(data)
	if !strings.Contains(workflow, "GHCR_OWNER") {
		t.Fatal("remote build E2E workflow should define GHCR_OWNER")
	}
	if !strings.Contains(workflow, `--ghcr-owner "$GHCR_OWNER"`) {
		t.Fatal("remote build E2E workflow should pass GHCR_OWNER to -ghcr-owner")
	}
}

func TestRemoteBuildE2EWorkflowInstallsLLARCommand(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/remote-build-e2e.yml")
	if err != nil {
		t.Fatalf("read remote build E2E workflow: %v", err)
	}
	workflow := string(data)
	if !strings.Contains(workflow, "go install ./cmd/llar") {
		t.Fatal("remote build E2E workflow should install llar before running the E2E runner")
	}
	if !strings.Contains(workflow, "$(go env GOPATH)/bin") {
		t.Fatal("remote build E2E workflow should add the installed llar command to PATH")
	}
}

func TestRunLLARMakeUsesLLARFromPATHWithTemporaryWorkAndHome(t *testing.T) {
	installLLARHelper(t)

	recordPath := filepath.Join(t.TempDir(), "record.txt")
	originalHome := filepath.Join(t.TempDir(), "original-home")
	if err := os.MkdirAll(originalHome, 0o755); err != nil {
		t.Fatalf("create original home: %v", err)
	}
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Setenv("LLAR_MAKE_RECORD", recordPath)
	t.Setenv("HOME", originalHome)

	got, err := runLLARMake(context.Background(), testRequest(), nil)
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

	record, err := readHelperRecord(recordPath)
	if err != nil {
		t.Fatalf("read helper record: %v", err)
	}
	if record["cwd"] == originalDir {
		t.Fatalf("cmd.Dir = %q, want temporary work dir", record["cwd"])
	}
	if filepath.Base(record["cwd"]) != "work" {
		t.Fatalf("cmd.Dir = %q, want temp work dir ending in work", record["cwd"])
	}
	if record["home"] == originalHome {
		t.Fatalf("HOME = %q, want temporary home dir", record["home"])
	}
	if filepath.Base(record["home"]) != "home" {
		t.Fatalf("HOME = %q, want temp home dir ending in home", record["home"])
	}
	if record["cwd_exists"] != "true" || record["home_exists"] != "true" {
		t.Fatalf("helper saw cwd/home existence flags: %+v", record)
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
		Artifact: completed,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v, want %+v", got, want)
	}
	if uploader.Calls() != 0 {
		t.Fatalf("uploader calls = %d, want 0", uploader.Calls())
	}
}

func TestBuildJoinsSingleflight(t *testing.T) {
	installLLARHelper(t)
	req := testRequest()
	store := newFakeStore()
	uploader := &fakeUploader{
		result: upload.Result{
			URL:      "https://ghcr.io/v2/owner/madler/zlib/blobs/sha256:abc",
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
}

func TestBuildUsesArtifactReturnedByPut(t *testing.T) {
	installLLARHelper(t)
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
	installLLARHelper(t)
	req := testRequest()
	store := newFakeStore()
	uploader := &fakeUploader{
		typ: "file",
		result: upload.Result{
			URL:      "file:///tmp/archive.tar.gz",
			Checksum: "candidate",
		},
	}

	_, err := New(Options{
		Store:    store,
		Uploader: uploader,
	}).Build(context.Background(), req, nil)
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

func installLLARHelper(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell wrapper")
	}
	binDir := t.TempDir()
	helperPath := filepath.Join(binDir, "llar")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestLLARMakeHelperProcess -- \"$@\"\n", os.Args[0])
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
	if record := os.Getenv("LLAR_MAKE_RECORD"); record != "" {
		cwd, _ := os.Getwd()
		home := os.Getenv("HOME")
		_, cwdErr := os.Stat(cwd)
		_, homeErr := os.Stat(home)
		body := fmt.Sprintf(
			"cwd=%s\nhome=%s\ncwd_exists=%t\nhome_exists=%t\nargs=%s\n",
			cwd,
			home,
			cwdErr == nil,
			homeErr == nil,
			strings.Join(args, " "),
		)
		if err := os.WriteFile(record, []byte(body), 0o644); err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(5)
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

func readHelperRecord(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	record := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		record[key] = value
	}
	return record, nil
}

func waitForFile(t *testing.T, path, name string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
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
