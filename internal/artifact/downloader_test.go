// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/mod/module"
)

func TestDownloaderDownloadsRequestedArtifact(t *testing.T) {
	query := "arch=" + runtime.GOARCH + "&os=" + runtime.GOOS
	rootID := "test/root@v1.0.0?" + query
	depID := "test/dep@v1.2.3?" + query
	var rootDownloads, depDownloads int

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			if got := r.URL.Query().Encode(); got != query {
				t.Errorf("matrix query = %q, want %q", got, query)
			}
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeDownloadCommand(t, w, "artifact", downloadArtifactMessage{
				ID: rootID, Type: "tar.gz", URL: server.URL + "/root.tar.gz", Deps: []string{depID},
			})
		case "/root.tar.gz":
			rootDownloads++
			_, _ = io.WriteString(w, "root")
		case "/dep.zip":
			depDownloads++
			_, _ = io.WriteString(w, "dep")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewDownloader(DownloaderOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := downloader.Download(context.Background(), module.Version{Path: "test/root"}, hostDownloadMatrix(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	name := result.File.Name()
	defer os.Remove(name)
	defer result.File.Close()

	if result.Module != (module.Version{Path: "test/root", Version: "v1.0.0"}) {
		t.Fatalf("module = %+v", result.Module)
	}
	if len(result.Deps) != 1 || result.Deps[0] != (module.Version{Path: "test/dep", Version: "v1.2.3"}) {
		t.Fatalf("deps = %+v", result.Deps)
	}
	if !strings.HasSuffix(name, ".tar.gz") {
		t.Fatalf("file name = %q, want .tar.gz suffix", name)
	}
	data, err := io.ReadAll(result.File)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "root" {
		t.Fatalf("file contents = %q, want root", data)
	}
	if rootDownloads != 1 || depDownloads != 0 {
		t.Fatalf("downloads = root:%d dep:%d, want root only", rootDownloads, depDownloads)
	}
}

func TestDownloaderReturnsLlardError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-cmdjsonl")
		writeDownloadCommand(t, w, "error", "module not found")
	}))
	defer server.Close()

	downloader, err := NewDownloader(DownloaderOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Download(context.Background(), module.Version{Path: "test/missing"}, hostDownloadMatrix(), io.Discard)
	if err == nil || err.Error() != "llard: module not found" {
		t.Fatalf("Download() error = %v, want llard error", err)
	}
}

func TestDownloadQuery(t *testing.T) {
	query, err := downloadQuery(formula.Matrix{
		Require: map[string][]string{"os": {"linux"}, "arch": {"amd64"}},
		Options: map[string][]string{"shared": {"ON"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := query.Encode(), "arch=amd64&os=linux&shared=ON"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}

	tests := []struct {
		name   string
		matrix formula.Matrix
		want   string
	}{
		{name: "empty", want: "build matrix is required"},
		{name: "empty key", matrix: formula.Matrix{Require: map[string][]string{"": {"linux"}}}, want: "matrix key is required"},
		{name: "no values", matrix: formula.Matrix{Require: map[string][]string{"os": nil}}, want: `matrix "os" requires exactly one value`},
		{name: "multiple values", matrix: formula.Matrix{Require: map[string][]string{"os": {"linux", "darwin"}}}, want: `matrix "os" requires exactly one value`},
		{name: "empty value", matrix: formula.Matrix{Require: map[string][]string{"os": {""}}}, want: `matrix "os" requires exactly one value`},
		{name: "duplicate", matrix: formula.Matrix{Require: map[string][]string{"os": {"linux"}}, Options: map[string][]string{"os": {"darwin"}}}, want: `matrix "os" is duplicated`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := downloadQuery(tt.matrix)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("downloadQuery() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDownloaderRejectsInvalidLlardResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
	}{
		{name: "status", status: http.StatusServiceUnavailable, contentType: "application/x-cmdjsonl", want: "llard returned 503 Service Unavailable"},
		{name: "content type", contentType: "text/plain", body: "artifact {}\n", want: `llard returned content type "text/plain", want application/x-cmdjsonl`},
		{name: "line", contentType: "application/x-cmdjsonl", body: "invalid\n", want: "invalid llard response line 1"},
		{name: "info JSON", contentType: "application/x-cmdjsonl", body: "info {\n", want: "decode llard info line 1"},
		{name: "error JSON", contentType: "application/x-cmdjsonl", body: "error {\n", want: "decode llard error line 1"},
		{name: "artifact JSON", contentType: "application/x-cmdjsonl", body: "artifact {\n", want: "decode llard artifact line 1"},
		{name: "artifact fields", contentType: "application/x-cmdjsonl", body: "artifact {\"id\":\"test/root@v1?os=linux\"}\n", want: "invalid llard artifact line 1"},
		{name: "command", contentType: "application/x-cmdjsonl", body: "done {}\n", want: `unsupported llard response command "done"`},
		{name: "no artifacts", contentType: "application/x-cmdjsonl", body: "info \"checking\"\n", want: "llard returned no artifacts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			downloader, err := NewDownloader(DownloaderOptions{BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = downloader.Download(context.Background(), module.Version{Path: "test/root"}, hostDownloadMatrix(), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Download() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveDownloadArtifactRejectsInvalidResponses(t *testing.T) {
	query := url.Values{"arch": {"amd64"}, "os": {"linux"}}
	wantQuery := query.Encode()
	otherQuery := url.Values{"arch": {"arm64"}, "os": {"linux"}}.Encode()
	artifact := func(id string, deps ...string) downloadArtifactMessage {
		return downloadArtifactMessage{ID: id, Type: "zip", URL: "https://example.com/artifact.zip", Deps: deps}
	}
	tests := []struct {
		name      string
		messages  []downloadArtifactMessage
		requested module.Version
		want      string
	}{
		{name: "invalid artifact ID", messages: []downloadArtifactMessage{artifact("invalid")}, requested: module.Version{Path: "test/root"}, want: `invalid artifact id "invalid"`},
		{name: "artifact matrix", messages: []downloadArtifactMessage{artifact("test/root@v1?" + otherQuery)}, requested: module.Version{Path: "test/root"}, want: "has matrix query"},
		{name: "multiple roots", messages: []downloadArtifactMessage{artifact("test/root@v1?" + wantQuery), artifact("test/root@v2?" + wantQuery)}, requested: module.Version{Path: "test/root"}, want: "llard returned multiple artifacts"},
		{name: "missing root", messages: []downloadArtifactMessage{artifact("test/other@v1?" + wantQuery)}, requested: module.Version{Path: "test/root"}, want: "llard response is missing requested artifact"},
		{name: "invalid dependency ID", messages: []downloadArtifactMessage{artifact("test/root@v1?"+wantQuery, "invalid")}, requested: module.Version{Path: "test/root"}, want: `invalid artifact id "invalid"`},
		{name: "dependency matrix", messages: []downloadArtifactMessage{artifact("test/root@v1?"+wantQuery, "test/dep@v1?"+otherQuery)}, requested: module.Version{Path: "test/root"}, want: "has matrix query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := resolveDownloadArtifact(tt.messages, tt.requested, query)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolveDownloadArtifact() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseDownloadArtifactIDRejectsInvalidID(t *testing.T) {
	for _, id := range []string{
		"https://example.com/test/root@v1?os=linux",
		"test/root?os=linux",
		"test/root@?os=linux",
		"test/root@v1",
		"test/root@v1?%",
	} {
		t.Run(id, func(t *testing.T) {
			if _, _, err := parseDownloadArtifactID(id); err == nil {
				t.Fatalf("parseDownloadArtifactID(%q) succeeded", id)
			}
		})
	}
}

func TestDownloaderDownloadResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artifact.zip":
			_, _ = io.WriteString(w, "zip")
		case "/unavailable.zip":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewDownloader(DownloaderOptions{BaseURL: server.URL + "/base/"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := downloader.download(context.Background(), downloadArtifactMessage{Type: "zip", URL: "/artifact.zip"})
	if err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "zip" || !strings.HasSuffix(name, ".zip") {
		t.Fatalf("downloaded %q to %q", data, name)
	}

	tests := []struct {
		name     string
		artifact downloadArtifactMessage
		ctx      func() context.Context
		want     string
	}{
		{name: "type", artifact: downloadArtifactMessage{Type: "tar.zst", URL: "/artifact"}, want: `unsupported artifact type "tar.zst"`},
		{name: "URL parse", artifact: downloadArtifactMessage{Type: "zip", URL: "%"}, want: "invalid URL escape"},
		{name: "URL scheme", artifact: downloadArtifactMessage{Type: "zip", URL: "file:///tmp/artifact.zip"}, want: "invalid artifact URL"},
		{name: "status", artifact: downloadArtifactMessage{Type: "zip", URL: "/unavailable.zip"}, want: "download returned 503 Service Unavailable"},
		{name: "canceled", artifact: downloadArtifactMessage{Type: "zip", URL: "/artifact.zip"}, ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, want: "context canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}
			_, err := downloader.download(ctx, tt.artifact)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("download() error = %v, want %q", err, tt.want)
			}
		})
	}

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if _, err := downloader.download(context.Background(), downloadArtifactMessage{Type: "zip", URL: "/artifact.zip"}); err == nil {
		t.Fatal("download() succeeded with an unavailable temporary directory")
	}
}

func TestDownloaderPropagatesResolveAndDownloadErrors(t *testing.T) {
	query := "arch=" + runtime.GOARCH + "&os=" + runtime.GOOS
	tests := []struct {
		name string
		body downloadArtifactMessage
		want string
	}{
		{name: "resolve", body: downloadArtifactMessage{ID: "test/other@v1?" + query, Type: "zip", URL: "/artifact.zip"}, want: "missing requested artifact"},
		{name: "download", body: downloadArtifactMessage{ID: "test/root@v1?" + query, Type: "tar.zst", URL: "/artifact.tar.zst"}, want: "download artifact test/root@v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-cmdjsonl")
				writeDownloadCommand(t, w, "artifact", tt.body)
			}))
			defer server.Close()

			downloader, err := NewDownloader(DownloaderOptions{BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = downloader.Download(context.Background(), module.Version{Path: "test/root"}, hostDownloadMatrix(), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Download() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestNewDownloaderRejectsInvalidBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", "llar.xgo.dev", "file:///tmp/llar", "http://%"} {
		t.Run(baseURL, func(t *testing.T) {
			if _, err := NewDownloader(DownloaderOptions{BaseURL: baseURL}); err == nil {
				t.Fatalf("NewDownloader(%q) succeeded", baseURL)
			}
		})
	}
}

func hostDownloadMatrix() formula.Matrix {
	return formula.Matrix{Require: map[string][]string{
		"arch": {runtime.GOARCH},
		"os":   {runtime.GOOS},
	}}
}

func writeDownloadCommand(t *testing.T, w io.Writer, command string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", command, data)
}
