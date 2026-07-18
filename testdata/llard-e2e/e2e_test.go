// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
)

const e2eTimeout = 15 * time.Minute

var (
	zlibTarget   = target{path: "madler/zlib", version: "v1.3.1", metadata: "-lz"}
	libpngTarget = target{path: "pnggroup/libpng", version: "v1.6.47", metadata: "-lpng"}
	cjsonTarget  = target{path: "DaveGamble/cJSON", version: "v1.7.18", metadata: "-lcjson"}
)

type target struct {
	path     string
	version  string
	metadata string
}

func (t target) id(query string) string {
	return t.path + "@" + t.version + "?" + query
}

func (t target) key() string {
	return t.path + "@" + t.version
}

type config struct {
	baseURL  string
	workers  []string
	controls []string
	prefix   string
	access   string
	secret   string
	bucket   string
}

type artifactMessage struct {
	ID   string   `json:"id"`
	Type string   `json:"type"`
	URL  string   `json:"url"`
	Deps []string `json:"deps,omitempty"`
}

type response struct {
	infos     []string
	errors    []string
	artifacts []artifactMessage
	upstream  string
	allow     string
}

type suite struct {
	cfg        config
	client     *http.Client
	artifacts  artifact.Store
	matrix     classfile.Matrix
	matrixStr  string
	query      string
	wantOutput []string
	coldZlib   artifactMessage
	upstreams  map[string]struct{}
}

func TestLLARDE2E(t *testing.T) {
	if os.Getenv("LLARD_E2E_BASE_URL") == "" {
		t.Skip("LLARD_E2E_BASE_URL is required")
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}

	matrix := classfile.Matrix{Require: map[string][]string{
		"arch": {runtime.GOARCH},
		"os":   {runtime.GOOS},
	}}
	query := url.Values{
		"arch": {runtime.GOARCH},
		"os":   {runtime.GOOS},
	}.Encode()
	output, err := os.ReadFile(filepath.Join("..", "kodo-e2e", "formulas", "madler", "zlib", "v1.3.1", "output.txt"))
	if err != nil {
		t.Fatal(err)
	}

	s := &suite{
		cfg:    cfg,
		client: &http.Client{},
		artifacts: artifact.NewKodoArtifact(artifact.KodoArtifactConfig{
			AccessKey: cfg.access,
			SecretKey: cfg.secret,
			Bucket:    cfg.bucket,
			Prefix:    cfg.prefix,
		}),
		matrix:     matrix,
		matrixStr:  matrix.Combinations()[0],
		query:      query,
		wantOutput: strings.Split(strings.TrimSpace(string(output)), "\n"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()
	s.upstreams, err = s.workerUpstreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.deleteArtifacts(ctx, zlibTarget, libpngTarget, cjsonTarget); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.deleteArtifacts(ctx, zlibTarget, libpngTarget, cjsonTarget); err != nil {
			t.Errorf("cleanup artifacts: %v", err)
		}
	})

	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"cold zlib build streams info and stores artifact", s.coldZlibBuild},
		{"warm zlib request reuses stored artifact", s.warmZlibBuild},
		{"concurrent zlib requests run one build", s.concurrentZlibBuild},
		{"concurrent roots share canonical zlib artifact", s.concurrentSharedDependency},
		{"protocol errors use command JSON lines", s.protocolErrors},
	} {
		start := time.Now()
		t.Logf("RUN %s", step.name)
		if err := step.run(ctx); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		t.Logf("PASS %s (%s)", step.name, time.Since(start).Round(time.Millisecond))
	}
}

func loadConfig() (config, error) {
	cfg := config{
		baseURL:  strings.TrimRight(os.Getenv("LLARD_E2E_BASE_URL"), "/"),
		workers:  splitURLs(os.Getenv("LLARD_E2E_WORKERS")),
		controls: splitURLs(os.Getenv("LLARD_E2E_CONTROLS")),
		access:   os.Getenv("QINIU_ACCESS_KEY"),
		secret:   os.Getenv("QINIU_SECRET_KEY"),
		bucket:   os.Getenv("QINIU_BUCKET"),
	}
	prefix := strings.Trim(os.Getenv("QINIU_PREFIX"), "/")
	if prefix != "" {
		prefix += "/"
	}
	runID := os.Getenv("GITHUB_RUN_ID")
	runAttempt := os.Getenv("GITHUB_RUN_ATTEMPT")
	if runID == "" || runAttempt == "" {
		return config{}, errors.New("GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT are required")
	}
	cfg.prefix = prefix + "llard-cluster-e2e/" + runID + "-" + runAttempt

	if len(cfg.workers) != 2 {
		return config{}, fmt.Errorf("LLARD_E2E_WORKERS contains %d workers, want 2", len(cfg.workers))
	}
	if len(cfg.controls) != 2 {
		return config{}, fmt.Errorf("LLARD_E2E_CONTROLS contains %d controls, want 2", len(cfg.controls))
	}
	if cfg.access == "" || cfg.secret == "" || cfg.bucket == "" {
		return config{}, errors.New("QINIU_ACCESS_KEY, QINIU_SECRET_KEY, and QINIU_BUCKET are required")
	}
	return cfg, nil
}

func splitURLs(value string) []string {
	parts := strings.Split(value, ",")
	urls := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimRight(strings.TrimSpace(part), "/"); part != "" {
			urls = append(urls, part)
		}
	}
	return urls
}

func (s *suite) coldZlibBuild(ctx context.Context) error {
	before, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}
	got, err := s.get(ctx, s.cfg.baseURL, zlibTarget)
	if err != nil {
		return err
	}
	if err := s.requireSuccess(got, zlibTarget); err != nil {
		return err
	}
	after, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}
	if delta := after[zlibTarget.key()] - before[zlibTarget.key()]; delta != 1 {
		return fmt.Errorf("zlib build count = %d, want 1", delta)
	}
	if got.upstream == "" {
		return errors.New("nginx response has no X-Upstream-Addr")
	}
	if _, ok := s.upstreams[got.upstream]; !ok {
		return fmt.Errorf("nginx upstream = %q, workers = %v", got.upstream, s.upstreams)
	}
	if err := containsInOrder(got.infos[1:], s.wantOutput); err != nil {
		return fmt.Errorf("zlib info output: %w", err)
	}
	s.coldZlib = got.artifacts[0]
	info, checksum, err := s.downloadArtifact(ctx, s.coldZlib)
	if err != nil {
		return err
	}
	if info.Metadata != zlibTarget.metadata {
		return fmt.Errorf("zlib metadata = %q, want %q", info.Metadata, zlibTarget.metadata)
	}
	stored, err := s.artifacts.Get(ctx, s.artifactKey(zlibTarget))
	if err != nil {
		return err
	}
	if stored.Source.URL != s.coldZlib.URL || stored.Checksum != checksum {
		return fmt.Errorf("stored zlib artifact = %+v, response URL %q checksum %q", stored, s.coldZlib.URL, checksum)
	}
	return nil
}

func (s *suite) warmZlibBuild(ctx context.Context) error {
	before, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}
	got, err := s.get(ctx, s.cfg.baseURL, zlibTarget)
	if err != nil {
		return err
	}
	if err := s.requireSuccess(got, zlibTarget); err != nil {
		return err
	}
	after, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}
	if delta := after[zlibTarget.key()] - before[zlibTarget.key()]; delta != 0 {
		return fmt.Errorf("warm zlib build count = %d, want 0", delta)
	}
	if len(got.infos) != 1 {
		return fmt.Errorf("warm zlib info = %q, want resolving only", got.infos)
	}
	if !reflect.DeepEqual(got.artifacts[0], s.coldZlib) {
		return fmt.Errorf("warm zlib artifact = %+v, want %+v", got.artifacts[0], s.coldZlib)
	}
	return nil
}

func (s *suite) concurrentZlibBuild(ctx context.Context) error {
	if err := s.deleteArtifacts(ctx, zlibTarget); err != nil {
		return err
	}
	before, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}

	const clients = 8
	start := make(chan struct{})
	results := make(chan struct {
		response response
		err      error
	}, clients)
	for range clients {
		go func() {
			<-start
			got, err := s.get(ctx, s.cfg.baseURL, zlibTarget)
			results <- struct {
				response response
				err      error
			}{response: got, err: err}
		}()
	}
	close(start)

	responses := make([]response, 0, clients)
	for range clients {
		result := <-results
		if result.err != nil {
			return result.err
		}
		if err := s.requireSuccess(result.response, zlibTarget); err != nil {
			return err
		}
		responses = append(responses, result.response)
	}
	after, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}
	if delta := after[zlibTarget.key()] - before[zlibTarget.key()]; delta != 1 {
		return fmt.Errorf("concurrent zlib build count = %d, want 1", delta)
	}

	wantArtifact := responses[0].artifacts[0]
	wantUpstream := responses[0].upstream
	if _, ok := s.upstreams[wantUpstream]; !ok {
		return fmt.Errorf("concurrent nginx upstream = %q, workers = %v", wantUpstream, s.upstreams)
	}
	fullOutput := false
	for i, got := range responses {
		if got.upstream != wantUpstream {
			return fmt.Errorf("response %d upstream = %q, want %q", i, got.upstream, wantUpstream)
		}
		if !reflect.DeepEqual(got.artifacts[0], wantArtifact) {
			return fmt.Errorf("response %d artifact = %+v, want %+v", i, got.artifacts[0], wantArtifact)
		}
		if len(got.infos) < 2 {
			return fmt.Errorf("response %d has no streamed build info: %q", i, got.infos)
		}
		if containsInOrder(got.infos[1:], s.wantOutput) == nil {
			fullOutput = true
		}
	}
	if !fullOutput {
		return errors.New("no concurrent client received the complete zlib build output")
	}
	return nil
}

func (s *suite) concurrentSharedDependency(ctx context.Context) error {
	if err := s.deleteArtifacts(ctx, zlibTarget, libpngTarget, cjsonTarget); err != nil {
		return err
	}
	before, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}

	targets := []target{libpngTarget, cjsonTarget}
	start := make(chan struct{})
	results := make(chan struct {
		index    int
		response response
		err      error
	}, len(targets))
	for i, target := range targets {
		go func() {
			<-start
			got, err := s.get(ctx, s.cfg.workers[i], target)
			results <- struct {
				index    int
				response response
				err      error
			}{index: i, response: got, err: err}
		}()
	}
	close(start)

	responses := make([]response, len(targets))
	for range targets {
		result := <-results
		if result.err != nil {
			return result.err
		}
		if err := s.requireSuccess(result.response, zlibTarget, targets[result.index]); err != nil {
			return err
		}
		responses[result.index] = result.response
	}
	after, err := s.buildCounts(ctx)
	if err != nil {
		return err
	}
	if delta := after[libpngTarget.key()] - before[libpngTarget.key()]; delta != 1 {
		return fmt.Errorf("libpng build count = %d, want 1", delta)
	}
	if delta := after[cjsonTarget.key()] - before[cjsonTarget.key()]; delta != 1 {
		return fmt.Errorf("cJSON build count = %d, want 1", delta)
	}
	zlibBuilds := after[zlibTarget.key()] - before[zlibTarget.key()]
	if zlibBuilds < 1 || zlibBuilds > 2 {
		return fmt.Errorf("shared zlib build count = %d, want 1 or 2", zlibBuilds)
	}

	zlibArtifacts := make([]artifactMessage, len(responses))
	for i, got := range responses {
		root, err := artifactFor(got, targets[i].id(s.query))
		if err != nil {
			return err
		}
		zlib, err := artifactFor(got, zlibTarget.id(s.query))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(root.Deps, []string{zlib.ID}) {
			return fmt.Errorf("%s deps = %q, want %q", root.ID, root.Deps, []string{zlib.ID})
		}
		zlibArtifacts[i] = zlib

		info, _, err := s.downloadArtifact(ctx, root)
		if err != nil {
			return err
		}
		if info.Metadata != targets[i].metadata {
			return fmt.Errorf("%s metadata = %q, want %q", targets[i].key(), info.Metadata, targets[i].metadata)
		}
		if !reflect.DeepEqual(info.Deps, []module.Version{{Path: zlibTarget.path, Version: zlibTarget.version}}) {
			return fmt.Errorf("%s archive deps = %+v", targets[i].key(), info.Deps)
		}
	}
	if !reflect.DeepEqual(zlibArtifacts[0], zlibArtifacts[1]) {
		return fmt.Errorf("shared zlib artifacts differ: %+v != %+v", zlibArtifacts[0], zlibArtifacts[1])
	}
	return nil
}

func (s *suite) protocolErrors(ctx context.Context) error {
	missingMatrix, err := s.do(ctx, http.MethodGet, s.cfg.baseURL+"/v1/artifacts/"+zlibTarget.path+"@"+zlibTarget.version)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(missingMatrix.errors, []string{"build matrix is required"}) || len(missingMatrix.artifacts) != 0 {
		return fmt.Errorf("missing matrix response = %+v", missingMatrix)
	}

	method, err := s.do(ctx, http.MethodPost, s.requestURL(s.cfg.baseURL, zlibTarget))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(method.errors, []string{"method not allowed"}) || len(method.artifacts) != 0 || method.allow != http.MethodGet {
		return fmt.Errorf("invalid method response = %+v", method)
	}
	return nil
}

func (s *suite) get(ctx context.Context, baseURL string, target target) (response, error) {
	return s.do(ctx, http.MethodGet, s.requestURL(baseURL, target))
}

func (s *suite) requestURL(baseURL string, target target) string {
	return strings.TrimRight(baseURL, "/") + "/v1/artifacts/" + target.path + "@" + target.version + "?" + s.query
}

func (s *suite) do(ctx context.Context, method, requestURL string) (response, error) {
	req, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return response{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return response{}, fmt.Errorf("%s returned %s", requestURL, resp.Status)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return response{}, err
	}
	if mediaType != "application/x-cmdjsonl" {
		return response{}, fmt.Errorf("Content-Type = %q", mediaType)
	}

	got := response{
		upstream: resp.Header.Get("X-Upstream-Addr"),
		allow:    resp.Header.Get("Allow"),
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		command, data, ok := strings.Cut(line, " ")
		if !ok {
			return response{}, fmt.Errorf("invalid command line %q", line)
		}
		switch command {
		case "info":
			var message string
			if err := json.Unmarshal([]byte(data), &message); err != nil {
				return response{}, fmt.Errorf("decode info %q: %w", line, err)
			}
			got.infos = append(got.infos, message)
		case "error":
			var message string
			if err := json.Unmarshal([]byte(data), &message); err != nil {
				return response{}, fmt.Errorf("decode error %q: %w", line, err)
			}
			got.errors = append(got.errors, message)
		case "artifact":
			var message artifactMessage
			if err := json.Unmarshal([]byte(data), &message); err != nil {
				return response{}, fmt.Errorf("decode artifact %q: %w", line, err)
			}
			got.artifacts = append(got.artifacts, message)
		default:
			return response{}, fmt.Errorf("unexpected command %q", command)
		}
	}
	if err := scanner.Err(); err != nil {
		return response{}, err
	}
	return got, nil
}

func (s *suite) requireSuccess(got response, targets ...target) error {
	if len(got.errors) != 0 {
		return fmt.Errorf("response errors: %v", got.errors)
	}
	if len(got.infos) == 0 {
		return errors.New("response contains no info commands")
	}
	if len(got.artifacts) != len(targets) {
		return fmt.Errorf("artifacts = %+v, want %d", got.artifacts, len(targets))
	}
	for _, target := range targets {
		message, err := artifactFor(got, target.id(s.query))
		if err != nil {
			return err
		}
		if message.Type != "tar.gz" || message.URL == "" {
			return fmt.Errorf("artifact = %+v, want tar.gz download URL", message)
		}
	}
	return nil
}

func artifactFor(got response, id string) (artifactMessage, error) {
	for _, message := range got.artifacts {
		if message.ID == id {
			return message, nil
		}
	}
	return artifactMessage{}, fmt.Errorf("artifact %q not found in %+v", id, got.artifacts)
}

func (s *suite) downloadArtifact(ctx context.Context, message artifactMessage) (metadata.Info, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, message.URL, nil)
	if err != nil {
		return metadata.Info{}, "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return metadata.Info{}, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return metadata.Info{}, "", fmt.Errorf("download %s returned %s", message.ID, resp.Status)
	}

	file, err := os.CreateTemp("", "llard-e2e-*."+message.Type)
	if err != nil {
		return metadata.Info{}, "", err
	}
	fileName := file.Name()
	defer os.Remove(fileName)
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, hash), resp.Body); err != nil {
		_ = file.Close()
		return metadata.Info{}, "", err
	}
	if err := file.Close(); err != nil {
		return metadata.Info{}, "", err
	}

	dst, err := os.MkdirTemp("", "llard-e2e-unpack-")
	if err != nil {
		return metadata.Info{}, "", err
	}
	defer os.RemoveAll(dst)
	data, err := archiver.Unpack(fileName, dst)
	if err != nil {
		return metadata.Info{}, "", err
	}
	info, err := metadata.Decode(data, dst)
	if err != nil {
		return metadata.Info{}, "", err
	}
	if message.ID == zlibTarget.id(s.query) {
		if _, err := os.Stat(filepath.Join(dst, "lib", "libz.a")); err != nil {
			return metadata.Info{}, "", fmt.Errorf("zlib archive: %w", err)
		}
	}
	return info, hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *suite) buildCounts(ctx context.Context) (map[string]int, error) {
	counts := make(map[string]int)
	for _, control := range s.cfg.controls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, control+"/builds", nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("%s/builds returned %s", control, resp.Status)
		}
		var sources []string
		err = json.NewDecoder(resp.Body).Decode(&sources)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			base := filepath.Base(source)
			for _, target := range []target{zlibTarget, libpngTarget, cjsonTarget} {
				prefix := "source-" + strings.ReplaceAll(target.path, "/", "-") + "-" + target.version
				if strings.HasPrefix(base, prefix) {
					counts[target.key()]++
				}
			}
		}
	}
	return counts, nil
}

func (s *suite) workerUpstreams(ctx context.Context) (map[string]struct{}, error) {
	upstreams := make(map[string]struct{}, len(s.cfg.controls))
	for _, control := range s.cfg.controls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, control+"/identity", nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("%s/identity returned %s", control, resp.Status)
		}
		var upstream string
		err = json.NewDecoder(resp.Body).Decode(&upstream)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if upstream == "" {
			return nil, fmt.Errorf("%s/identity returned an empty upstream", control)
		}
		upstreams[upstream] = struct{}{}
	}
	if len(upstreams) != len(s.cfg.controls) {
		return nil, fmt.Errorf("worker upstreams = %v, want %d unique addresses", upstreams, len(s.cfg.controls))
	}
	return upstreams, nil
}

func (s *suite) deleteArtifacts(ctx context.Context, targets ...target) error {
	for _, target := range targets {
		if err := s.artifacts.Delete(ctx, s.artifactKey(target)); err != nil {
			return fmt.Errorf("delete %s: %w", target.key(), err)
		}
	}
	return nil
}

func (s *suite) artifactKey(target target) artifact.Key {
	return artifact.Key{Module: target.path, Version: target.version, MatrixStr: s.matrixStr}
}

func containsInOrder(got, want []string) error {
	next := 0
	for _, expected := range want {
		found := -1
		for i := next; i < len(got); i++ {
			if strings.Contains(got[i], expected) {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("missing %q after line %d in:\n%s", expected, next, strings.Join(got, "\n"))
		}
		next = found + 1
	}
	return nil
}
