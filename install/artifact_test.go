package install

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRequestInstallArtifactsRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
	}{
		{name: "status", status: http.StatusServiceUnavailable, contentType: "application/x-cmdjsonl", want: "llard returned 503 Service Unavailable"},
		{name: "content type", contentType: "text/plain", body: "artifact {}\n", want: `llard returned content type "text/plain", want application/x-cmdjsonl`},
		{name: "content type syntax", contentType: "application/x-cmdjsonl; invalid", body: "artifact {}\n", want: `llard returned content type "application/x-cmdjsonl; invalid", want application/x-cmdjsonl`},
		{name: "line", contentType: "application/x-cmdjsonl", body: "invalid\n", want: "1: parse error when ParseCommand: no space found"},
		{name: "info JSON", contentType: "application/x-cmdjsonl", body: "info {\n", want: "1: parse error when UnmarshalParam: unexpected end of JSON input"},
		{name: "error JSON", contentType: "application/x-cmdjsonl", body: "error {\n", want: "1: parse error when UnmarshalParam: unexpected end of JSON input"},
		{name: "artifact JSON", contentType: "application/x-cmdjsonl", body: "artifact {\n", want: "1: parse error when UnmarshalParam: unexpected end of JSON input"},
		{name: "artifact fields", contentType: "application/x-cmdjsonl", body: "artifact {\"id\":\"test/root@v1?os=linux\"}\n", want: "1: parse error when CallHandler artifact: invalid llard artifact"},
		{name: "command", contentType: "application/x-cmdjsonl", body: "done {}\n", want: "1: parse error when ParseCommand: unknown command 'done'"},
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
			baseURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = requestInstallArtifacts(
				context.Background(), nil, server.Client(), baseURL,
				module.Version{Path: "test/root"}, url.Values{"os": {"linux"}},
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("requestInstallArtifacts() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRequestInstallArtifactsReturnsTransportError(t *testing.T) {
	baseURL, err := url.Parse("https://llard.example")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})}

	_, err = requestInstallArtifacts(
		context.Background(), nil, client, baseURL,
		module.Version{Path: "test/root"}, url.Values{"os": {"linux"}},
	)
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("requestInstallArtifacts() error = %v, want transport error", err)
	}
}

func TestRequestInstallArtifactsRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-cmdjsonl")
		_, _ = io.WriteString(w, strings.Repeat("x", bufio.MaxScanTokenSize+1)+"\n")
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = requestInstallArtifacts(
		context.Background(), nil, server.Client(), baseURL,
		module.Version{Path: "test/root"}, url.Values{"os": {"linux"}},
	)
	if err == nil || err.Error() != "1: parse error when ReadLine: line too long" {
		t.Fatalf("requestInstallArtifacts() error = %v, want oversized line error", err)
	}
}

func TestDownloadInstallArtifactResponses(t *testing.T) {
	archive := makeInstallArtifact(t, ".zip", "include/root.h", "root", "/build/root", "-lroot", nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/artifact.zip":
			_, _ = w.Write(archive)
		case "/unavailable.zip":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	baseURL, err := url.Parse(server.URL + "/base/")
	if err != nil {
		t.Fatal(err)
	}

	installDir := filepath.Join(t.TempDir(), "install")
	info, err := downloadInstallArtifact(context.Background(), server.Client(), baseURL, installArtifactMessage{Type: "zip", URL: "/artifact.zip"}, installDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Metadata != "-lroot" {
		t.Fatalf("metadata = %q", info.Metadata)
	}
	assertInstallFile(t, filepath.Join(installDir, "include", "root.h"), "root")

	tests := []struct {
		name     string
		artifact installArtifactMessage
		ctx      func() context.Context
		want     string
	}{
		{name: "type", artifact: installArtifactMessage{Type: "tar.zst", URL: "/artifact"}, want: `unsupported artifact type "tar.zst"`},
		{name: "URL parse", artifact: installArtifactMessage{Type: "zip", URL: "%"}, want: "invalid URL escape"},
		{name: "URL scheme", artifact: installArtifactMessage{Type: "zip", URL: "file:///tmp/artifact.zip"}, want: "invalid artifact URL"},
		{name: "status", artifact: installArtifactMessage{Type: "zip", URL: "/unavailable.zip"}, want: "download returned 503 Service Unavailable"},
		{name: "canceled", artifact: installArtifactMessage{Type: "zip", URL: "/artifact.zip"}, ctx: func() context.Context {
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
			_, err := downloadInstallArtifact(ctx, server.Client(), baseURL, tt.artifact, filepath.Join(t.TempDir(), "install"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("downloadInstallArtifact() error = %v, want %q", err, tt.want)
			}
		})
	}

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if _, err := downloadInstallArtifact(context.Background(), server.Client(), baseURL, installArtifactMessage{Type: "zip", URL: "/artifact.zip"}, filepath.Join(t.TempDir(), "install")); err == nil {
		t.Fatal("downloadInstallArtifact() succeeded with an unavailable temporary directory")
	}
}

func TestInstallDownloadedArtifactErrors(t *testing.T) {
	t.Run("parent", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "parent")
		if err := os.WriteFile(parent, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := installDownloadedArtifact("artifact.zip", filepath.Join(parent, "install")); err == nil {
			t.Fatal("installDownloadedArtifact() succeeded with an invalid parent")
		}
	})

	t.Run("metadata", func(t *testing.T) {
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "root.h"), []byte("root"), 0o644); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(t.TempDir(), "artifact.zip")
		if err := archiver.Pack(source, archive, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := installDownloadedArtifact(archive, filepath.Join(t.TempDir(), "install")); err == nil || !strings.Contains(err.Error(), "metadata is required") {
			t.Fatalf("installDownloadedArtifact() error = %v, want metadata error", err)
		}
	})
}

func makeInstallArtifact(t *testing.T, suffix, name, contents, buildDir, value string, deps []module.Version) []byte {
	t.Helper()
	sourceDir := t.TempDir()
	path := filepath.Join(sourceDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := metadata.Encode(metadata.Info{Metadata: value, Deps: deps}, buildDir)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "artifact"+suffix)
	if err := archiver.Pack(sourceDir, archive, data); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func writeInstallCommand(t *testing.T, w http.ResponseWriter, command string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", command, data)
}

func assertInstallFile(t *testing.T, name, want string) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", name, data, want)
	}
}
