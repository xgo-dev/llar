// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/build/cache"
)

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		wantModule  string
		wantVer     string
		wantMatrix  string
		wantRequire map[string][]string
		wantOptions map[string][]string
		wantErr     string
	}{
		{
			name:        "module and version",
			target:      "/v1/artifacts/madler/zlib@v1.3.1?os=linux&arch=amd64",
			wantModule:  "madler/zlib",
			wantVer:     "v1.3.1",
			wantMatrix:  "amd64-linux",
			wantRequire: map[string][]string{"arch": {"amd64"}, "os": {"linux"}},
		},
		{
			name:        "latest version",
			target:      "/v1/artifacts/madler/zlib?os=linux",
			wantModule:  "madler/zlib",
			wantMatrix:  "linux",
			wantRequire: map[string][]string{"os": {"linux"}},
		},
		{
			name:        "require and options",
			target:      "/v1/artifacts/madler/zlib@v1.3.1?os=linux&arch=amd64&debug=OFF&shared=ON",
			wantModule:  "madler/zlib",
			wantVer:     "v1.3.1",
			wantMatrix:  "amd64-linux|OFF-ON",
			wantRequire: map[string][]string{"arch": {"amd64"}, "os": {"linux"}},
			wantOptions: map[string][]string{"debug": {"OFF"}, "shared": {"ON"}},
		},
		{name: "wrong path", target: "/v1/modules/madler/zlib?os=linux", wantErr: "artifact path not found"},
		{name: "missing module", target: "/v1/artifacts/?os=linux", wantErr: "module is required"},
		{name: "trailing slash", target: "/v1/artifacts/madler/zlib/?os=linux", wantErr: "module is required"},
		{name: "empty module before version", target: "/v1/artifacts/@v1.3.1?os=linux", wantErr: "module is required"},
		{name: "missing matrix", target: "/v1/artifacts/madler/zlib@v1.3.1", wantErr: "build matrix is required"},
		{name: "empty matrix key", target: "/v1/artifacts/madler/zlib@v1.3.1?=linux", wantErr: "matrix key is required"},
		{name: "empty matrix value", target: "/v1/artifacts/madler/zlib@v1.3.1?os=", wantErr: `matrix "os" requires exactly one value`},
		{name: "multiple matrix values", target: "/v1/artifacts/madler/zlib@v1.3.1?os=linux&os=darwin", wantErr: `matrix "os" requires exactly one value`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := parseRequest(httptest.NewRequest(http.MethodGet, tt.target, nil))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("parseRequest error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if req.module != tt.wantModule || req.version != tt.wantVer || req.matrixStr != tt.wantMatrix {
				t.Fatalf("parseRequest = module %q, version %q, matrix %q", req.module, req.version, req.matrixStr)
			}
			if !reflect.DeepEqual(req.matrix.Require, tt.wantRequire) || !reflect.DeepEqual(req.matrix.Options, tt.wantOptions) {
				t.Fatalf("parseRequest matrix = %#v, want require %#v options %#v", req.matrix, tt.wantRequire, tt.wantOptions)
			}
			if requestKey(req) != moduleID(req.module, req.version, req.query) {
				t.Fatalf("requestKey = %q", requestKey(req))
			}
			for key, values := range req.query {
				values[0] = "changed"
				matrixValues := req.matrix.Options[key]
				if key == "os" || key == "arch" {
					matrixValues = req.matrix.Require[key]
				}
				if matrixValues[0] == "changed" {
					t.Fatalf("request query aliases matrix value for %q", key)
				}
				break
			}
		})
	}
}

func TestModuleID(t *testing.T) {
	tests := []struct {
		module  string
		version string
		query   string
		want    string
	}{
		{module: "madler/zlib", want: "madler/zlib"},
		{module: "madler/zlib", version: "v1.3.1", want: "madler/zlib@v1.3.1"},
		{module: "madler/zlib", query: "os=linux", want: "madler/zlib?os=linux"},
	}
	for _, tt := range tests {
		if got := moduleIDFromQuery(tt.module, tt.version, tt.query); got != tt.want {
			t.Errorf("moduleIDFromQuery(%q, %q, %q) = %q, want %q", tt.module, tt.version, tt.query, got, tt.want)
		}
	}
}

func TestServeHTTPRejectsInvalidRequest(t *testing.T) {
	h := New(Options{})

	t.Run("method", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/artifacts/madler/zlib?os=linux", nil))
		if got := recorder.Header().Get("Content-Type"); got != "application/x-cmdjsonl" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := recorder.Header().Get("Allow"); got != http.MethodGet {
			t.Fatalf("Allow = %q", got)
		}
		if got := recorder.Body.String(); got != "error \"method not allowed\"\n" {
			t.Fatalf("body = %q", got)
		}
	})

	t.Run("path", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unknown", nil))
		if got := recorder.Body.String(); got != "error \"artifact path not found\"\n" {
			t.Fatalf("body = %q", got)
		}
	})
}

func TestServeHTTPSingleflight(t *testing.T) {
	store := localFormulas(t)
	buildCache := &testCache{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	artifacts := &testArtifactStore{}
	h := New(Options{
		FormulaStore: store,
		Cache:        buildCache,
		Artifacts:    artifacts,
		WorkspaceDir: t.TempDir(),
	}).(*handler)
	const target = "/v1/artifacts/DaveGamble/cJSON@v1.7.18?os=linux&arch=amd64"

	var recorders [2]*httptest.ResponseRecorder
	var wg sync.WaitGroup
	serve := func(index int) {
		defer wg.Done()
		recorders[index] = httptest.NewRecorder()
		h.ServeHTTP(recorders[index], httptest.NewRequest(http.MethodGet, target, nil))
	}
	wg.Add(1)
	go serve(0)
	select {
	case <-buildCache.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach build cache")
	}

	wg.Add(1)
	go serve(1)
	key := "DaveGamble/cJSON@v1.7.18?arch=amd64&os=linux"
	waitFor(t, func() bool {
		stream, ok := h.infos.Load(key)
		if !ok {
			return false
		}
		fanout := stream.(*fanout)
		fanout.mu.Lock()
		defer fanout.mu.Unlock()
		return len(fanout.writers) == 2
	})
	close(buildCache.release)
	wg.Wait()

	if recorders[0].Body.String() != recorders[1].Body.String() {
		t.Fatalf("responses differ:\nfirst:\n%s\nsecond:\n%s", recorders[0].Body.String(), recorders[1].Body.String())
	}
	if got := buildCache.getCount(); got != 2 {
		t.Fatalf("cache Get calls = %d, want 2 for one cJSON and one zlib build", got)
	}
	if got := artifacts.getCount(); got != 2 {
		t.Fatalf("artifact Get calls = %d, want 2", got)
	}

	wantLines := []string{
		`info "resolving DaveGamble/cJSON@v1.7.18?arch=amd64\u0026os=linux"`,
		`artifact {"id":"madler/zlib@v1.3.1?arch=amd64\u0026os=linux","type":"tar.gz","url":"https://artifacts.example/madler/zlib"}`,
		`artifact {"id":"DaveGamble/cJSON@v1.7.18?arch=amd64\u0026os=linux","type":"tar.gz","url":"https://artifacts.example/DaveGamble/cJSON","deps":["madler/zlib@v1.3.1?arch=amd64\u0026os=linux"]}`,
	}
	gotLines := strings.Split(strings.TrimSpace(recorders[0].Body.String()), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("response lines = %d, want %d:\n%s", len(gotLines), len(wantLines), recorders[0].Body.String())
	}
	for i := range wantLines {
		if gotLines[i] != wantLines[i] {
			t.Errorf("response line %d = %q, want %q", i, gotLines[i], wantLines[i])
		}
	}
}

func TestServeHTTPBuildErrors(t *testing.T) {
	errFormula := errors.New("formula unavailable")
	errCache := errors.New("cache unavailable")
	errArtifact := errors.New("artifact unavailable")
	formulas := localFormulas(t)
	tests := []struct {
		name string
		opts Options
		err  error
	}{
		{
			name: "formula",
			opts: Options{FormulaStore: errorFormulaStore{err: errFormula}, WorkspaceDir: t.TempDir()},
			err:  errFormula,
		},
		{
			name: "cache",
			opts: Options{FormulaStore: formulas, Cache: &testCache{err: errCache}, WorkspaceDir: t.TempDir()},
			err:  errCache,
		},
		{
			name: "artifact",
			opts: Options{FormulaStore: formulas, Cache: &testCache{}, Artifacts: &testArtifactStore{err: errArtifact}, WorkspaceDir: t.TempDir()},
			err:  errArtifact,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			New(tt.opts).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/artifacts/madler/zlib@v1.3.1?os=linux", nil))
			want := "error " + strconv.Quote(tt.err.Error()) + "\n"
			lines := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n")
			if got := lines[len(lines)-1]; got != strings.TrimSuffix(want, "\n") {
				t.Fatalf("last response line = %q, want %q", got, strings.TrimSuffix(want, "\n"))
			}
		})
	}
}

func TestDoReturnsCanceledContext(t *testing.T) {
	store := &blockingFormulaStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	h := New(Options{FormulaStore: store, WorkspaceDir: t.TempDir()}).(*handler)
	req, err := parseRequest(httptest.NewRequest(http.MethodGet, "/v1/artifacts/madler/zlib@v1.3.1?os=linux", nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.do(ctx, req, io.Discard)
		done <- err
	}()
	select {
	case <-store.started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start loading formula")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("do error = %v, want context.Canceled", err)
	}
	close(store.release)
	waitFor(t, func() bool {
		_, ok := h.infos.Load(requestKey(req))
		return !ok
	})
}

func TestFanout(t *testing.T) {
	fanout := newFanout()
	removeNil := fanout.add(nil)
	removeNil()

	var first, second bytes.Buffer
	removeFirst := fanout.add(&first)
	removeSecond := fanout.add(&second)
	if n, err := fanout.Write([]byte("one")); err != nil || n != 3 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	removeFirst()
	if _, err := fanout.Write([]byte(" two")); err != nil {
		t.Fatal(err)
	}
	removeSecond()
	if first.String() != "one" || second.String() != "one two" {
		t.Fatalf("fanout output = %q, %q", first.String(), second.String())
	}
}

func TestInfoWriter(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &infoWriter{w: recorder}
	if n, err := writer.Write([]byte("first\n\npartial")); err != nil || n != len("first\n\npartial") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := writer.Write([]byte(" line\nlast")); err != nil {
		t.Fatal(err)
	}
	writer.flush()
	writer.flush()
	want := "info \"first\"\ninfo \"partial line\"\ninfo \"last\"\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("info output = %q, want %q", got, want)
	}
}

func TestWriteCommandMarshalError(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeCommand(recorder, "artifact", func() {})
	if got := recorder.Body.String(); !strings.HasPrefix(got, `error "json: unsupported type:`) {
		t.Fatalf("writeCommand output = %q", got)
	}
}

func localFormulas(t *testing.T) *localFormulaStore {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return &localFormulaStore{
		root: filepath.Join(filepath.Dir(filename), "..", "..", "..", "testdata", "kodo-e2e", "formulas"),
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(time.Millisecond)
	}
}

type testCache struct {
	mu      sync.Mutex
	gets    []cache.Key
	err     error
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *testCache) Get(_ context.Context, key cache.Key) (cache.Entry, bool, error) {
	c.mu.Lock()
	c.gets = append(c.gets, key)
	c.mu.Unlock()
	if c.started != nil {
		c.once.Do(func() { close(c.started) })
	}
	if c.release != nil {
		<-c.release
	}
	if c.err != nil {
		return cache.Entry{}, false, c.err
	}
	return cache.Entry{Metadata: "cached " + key.Module.Path}, true, nil
}

func (c *testCache) Put(context.Context, cache.Key, fs.FS, cache.Entry) (cache.Entry, error) {
	return cache.Entry{}, errors.New("unexpected cache Put")
}

func (c *testCache) getCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.gets)
}

type testArtifactStore struct {
	mu   sync.Mutex
	gets []artifact.Key
	err  error
}

func (s *testArtifactStore) Get(_ context.Context, key artifact.Key) (artifact.Artifact, error) {
	s.mu.Lock()
	s.gets = append(s.gets, key)
	s.mu.Unlock()
	if s.err != nil {
		return artifact.Artifact{}, s.err
	}
	return artifact.Artifact{
		Source: artifact.Source{Type: "tar.gz", URL: "https://artifacts.example/" + key.Module},
		Type:   "tar.gz",
	}, nil
}

func (s *testArtifactStore) Put(context.Context, artifact.Key, artifact.Artifact) (artifact.Artifact, error) {
	return artifact.Artifact{}, errors.New("unexpected artifact Put")
}

func (s *testArtifactStore) Delete(context.Context, artifact.Key) error {
	return errors.New("unexpected artifact Delete")
}

func (s *testArtifactStore) getCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.gets)
}

type errorFormulaStore struct {
	err error
}

func (s errorFormulaStore) ModuleFS(context.Context, string) (fs.FS, error) {
	return nil, s.err
}

func (errorFormulaStore) LockModule(string) (func(), error) {
	return func() {}, nil
}

type blockingFormulaStore struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingFormulaStore) ModuleFS(context.Context, string) (fs.FS, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil, errors.New("formula load released")
}

func (*blockingFormulaStore) LockModule(string) (func(), error) {
	return func() {}, nil
}

var _ cache.Cache = (*testCache)(nil)
var _ artifact.Store = (*testArtifactStore)(nil)
