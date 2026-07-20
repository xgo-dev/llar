// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/build/cache"
)

const kodoPublicDomain = "llar.liuxi.ng"

func TestHTTPKodoE2E(t *testing.T) {
	accessKey := os.Getenv("QINIU_ACCESS_KEY")
	secretKey := os.Getenv("QINIU_SECRET_KEY")
	bucket := os.Getenv("QINIU_BUCKET")
	if accessKey == "" || secretKey == "" || bucket == "" {
		t.Skip("QINIU_ACCESS_KEY, QINIU_SECRET_KEY, and QINIU_BUCKET are required")
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	formulas := &localFormulaStore{
		root: filepath.Join(filepath.Dir(filename), "..", "..", "..", "testdata", "kodo-e2e", "formulas"),
	}
	wantOutput, err := os.ReadFile(filepath.Join(formulas.root, "madler", "zlib", "v1.3.1", "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.Trim(os.Getenv("QINIU_PREFIX"), "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += fmt.Sprintf("llar-http-e2e/%d", time.Now().UnixNano())
	publicDomain := os.Getenv("QINIU_PUBLIC_DOMAIN")
	if publicDomain == "" {
		publicDomain = kodoPublicDomain
	}

	artifacts := artifact.NewKodoArtifact(artifact.KodoArtifactConfig{
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		Prefix:    prefix,
	})
	matrix := formula.Matrix{Require: map[string][]string{
		"arch": {runtime.GOARCH},
		"os":   {runtime.GOOS},
	}}
	matrixStr := matrix.Combinations()[0]
	key := artifact.Key{Module: "madler/zlib", Version: "v1.3.1", MatrixStr: matrixStr}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := artifacts.Delete(ctx, key); err != nil {
			t.Errorf("delete Kodo artifact: %v", err)
		}
	})

	requestURL := "/v1/artifacts/madler/zlib@v1.3.1?arch=" + runtime.GOARCH + "&os=" + runtime.GOOS
	for i, name := range []string{"cold build", "persisted artifact"} {
		t.Run(name, func(t *testing.T) {
			workspaceDir := t.TempDir()
			buildCache := cache.NewKodo(cache.KodoConfig{
				AccessKey:    accessKey,
				SecretKey:    secretKey,
				Bucket:       bucket,
				PublicDomain: publicDomain,
				Prefix:       prefix,
				WorkspaceDir: workspaceDir,
				Artifacts:    artifacts,
			})
			server := httptest.NewServer(New(Options{
				FormulaStore: formulas,
				Cache:        buildCache,
				Artifacts:    artifacts,
				WorkspaceDir: workspaceDir,
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+requestURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if got := resp.Header.Get("Content-Type"); got != "application/x-cmdjsonl" {
				t.Fatalf("Content-Type = %q", got)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			var infos, errors []string
			var gotArtifacts []artifactMessage
			for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
				command, data, ok := strings.Cut(line, " ")
				if !ok {
					t.Fatalf("invalid response line %q", line)
				}
				switch command {
				case "info":
					var message string
					if err := json.Unmarshal([]byte(data), &message); err != nil {
						t.Fatal(err)
					}
					infos = append(infos, message)
				case "error":
					var message string
					if err := json.Unmarshal([]byte(data), &message); err != nil {
						t.Fatal(err)
					}
					errors = append(errors, message)
				case "artifact":
					var message artifactMessage
					if err := json.Unmarshal([]byte(data), &message); err != nil {
						t.Fatal(err)
					}
					gotArtifacts = append(gotArtifacts, message)
				default:
					t.Fatalf("unexpected response command %q", command)
				}
			}
			if len(errors) != 0 {
				t.Fatalf("HTTP build errors: %v", errors)
			}
			if len(infos) == 0 {
				t.Fatal("HTTP build returned no info messages")
			}
			if i == 0 {
				buildOutput := infos[1:]
				if len(buildOutput) < 30 {
					t.Fatalf("build output has %d lines, want at least 30:\n%s", len(buildOutput), strings.Join(buildOutput, "\n"))
				}
				next := 0
				for _, want := range strings.Split(strings.TrimSpace(string(wantOutput)), "\n") {
					found := -1
					for line := next; line < len(buildOutput); line++ {
						if strings.Contains(buildOutput[line], want) {
							found = line
							break
						}
					}
					if found < 0 {
						t.Fatalf("build output missing %q after line %d:\n%s", want, next, strings.Join(buildOutput, "\n"))
					}
					next = found + 1
				}
			} else if len(infos) != 1 {
				t.Fatalf("persisted artifact info = %q, want resolving only", infos)
			}
			if len(gotArtifacts) != 1 {
				t.Fatalf("artifacts = %+v, want one", gotArtifacts)
			}
			got := gotArtifacts[0]
			wantID := "madler/zlib@v1.3.1?arch=" + runtime.GOARCH + "&os=" + runtime.GOOS
			if got.ID != wantID || got.Type != "tar.gz" || got.URL == "" || len(got.Deps) != 0 {
				t.Fatalf("artifact = %+v, want id %q and tar.gz URL", got, wantID)
			}

			download, err := http.Get(got.URL)
			if err != nil {
				t.Fatalf("download artifact: %v", err)
			}
			defer download.Body.Close()
			if download.StatusCode != http.StatusOK {
				_, _ = io.Copy(io.Discard, download.Body)
				t.Fatalf("download status = %s", download.Status)
			}
			if cwd, err := os.Getwd(); err != nil {
				t.Fatal(err)
			} else if cwd != originalCwd {
				t.Fatalf("cwd = %q, want %q", cwd, originalCwd)
			}
		})
	}
}

type localFormulaStore struct {
	root string
}

func (s *localFormulaStore) ModuleFS(_ context.Context, modPath string) (fs.FS, error) {
	dir := filepath.Join(s.root, filepath.FromSlash(modPath))
	if _, err := os.Stat(filepath.Join(dir, "versions.json")); err != nil {
		return nil, err
	}
	return os.DirFS(dir), nil
}

func (s *localFormulaStore) LockModule(string) (func(), error) {
	return func() {}, nil
}
