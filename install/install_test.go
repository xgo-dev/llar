package install

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/goplus/llar/mod/module"
)

func TestInstallerInstallConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		query  url.Values
		matrix string
	}{
		{
			name:   "host defaults",
			query:  url.Values{"arch": {runtime.GOARCH}, "os": {runtime.GOOS}},
			matrix: runtime.GOARCH + "-" + runtime.GOOS,
		},
		{
			name:   "OS override",
			config: Config{Require: Require{OS: "testos"}},
			query:  url.Values{"arch": {runtime.GOARCH}, "os": {"testos"}},
			matrix: runtime.GOARCH + "-testos",
		},
		{
			name:   "architecture override",
			config: Config{Require: Require{Arch: "testarch"}},
			query:  url.Values{"arch": {"testarch"}, "os": {runtime.GOOS}},
			matrix: "testarch-" + runtime.GOOS,
		},
		{
			name: "linux musl with options",
			config: Config{
				Require: Require{OS: "linux", Arch: "arm64", Libc: "musl"},
				Options: map[string]string{"zlib": "system", "ssl": "openssl"},
			},
			query:  url.Values{"arch": {"arm64"}, "libc": {"musl"}, "os": {"linux"}, "ssl": {"openssl"}, "zlib": {"system"}},
			matrix: "arm64-musl-linux|openssl-system",
		},
		{
			name:   "linux service libc default",
			config: Config{Require: Require{OS: "linux", Arch: "amd64"}},
			query:  url.Values{"arch": {"amd64"}, "os": {"linux"}},
			matrix: "amd64-linux",
		},
		{
			name:   "darwin",
			config: Config{Require: Require{OS: "darwin", Arch: "arm64"}},
			query:  url.Values{"arch": {"arm64"}, "os": {"darwin"}},
			matrix: "arm64-darwin",
		},
		{
			name:   "windows",
			config: Config{Require: Require{OS: "windows", Arch: "amd64"}},
			query:  url.Values{"arch": {"amd64"}, "os": {"windows"}},
			matrix: "amd64-windows",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			archive := makeInstallArtifact(t, ".zip", "include/root.h", "root", "/build/root", "-I/build/root/include -lroot", nil)
			requests, downloads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/artifacts/test/root@v1.0.0":
					requests++
					if got := r.URL.Query(); !reflect.DeepEqual(got, tt.query) {
						t.Errorf("query = %v, want %v", got, tt.query)
					}
					w.Header().Set("Content-Type", "application/x-cmdjsonl")
					writeInstallCommand(t, w, "info", "installing")
					writeInstallCommand(t, w, "artifact", map[string]string{
						"id": "test/root@v1.0.0?" + tt.query.Encode(), "type": "zip", "url": "/root.zip",
					})
				case "/root.zip":
					downloads++
					_, _ = w.Write(archive)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			installer := Installer{WorkspaceDir: workspace, ServiceURL: server.URL}
			before := tt.config.Require
			var progress bytes.Buffer
			mod := module.Version{Path: "test/root", Version: "v1.0.0"}
			first, err := installer.Install(context.Background(), mod, Options{Config: tt.config, Progress: &progress})
			if err != nil {
				t.Fatal(err)
			}
			wantDir := filepath.Join(workspace, "test/root@v1.0.0-"+tt.matrix)
			if first.Module != mod || first.Dir != wantDir || first.Metadata != "-I"+filepath.Join(wantDir, "include")+" -lroot" {
				t.Fatalf("result = %+v, want %v installed in %s", first, mod, wantDir)
			}
			assertInstallFile(t, filepath.Join(first.Dir, "include", "root.h"), "root")
			second, err := installer.Install(context.Background(), mod, Options{Config: tt.config})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) || requests != 2 || downloads != 1 {
				t.Fatalf("cache reuse: first=%+v second=%+v requests=%d downloads=%d", first, second, requests, downloads)
			}
			if progress.String() != "installing\n" || tt.config.Require != before {
				t.Fatalf("progress=%q require=%+v, want unchanged %+v", progress.String(), tt.config.Require, before)
			}
		})
	}
}

func TestInstallerRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		options map[string]string
		want    string
	}{
		{map[string]string{"": "ON"}, "matrix key is required"},
		{map[string]string{"ssl": ""}, `matrix "ssl" requires exactly one value`},
		{map[string]string{"os": "linux"}, `matrix "os" is duplicated`},
		{map[string]string{"arch": "arm64"}, `matrix "arch" is duplicated`},
		{map[string]string{"libc": "musl"}, `matrix "libc" is duplicated`},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			i := Installer{ServiceURL: "https://invalid.example"}
			_, err := i.Install(context.Background(), module.Version{Path: "test/root"}, Options{Config: Config{Options: tt.options}})
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestInstallerDefaultService(t *testing.T) {
	previous := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.Scheme + "://" + r.URL.Host; got != defaultServiceURL {
			t.Errorf("service URL = %q, want %q", got, defaultServiceURL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/x-cmdjsonl"}},
			Body:       http.NoBody,
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = previous })
	var installer Installer
	_, err := installer.Install(context.Background(), module.Version{Path: "test/root"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "llard returned no artifacts") {
		t.Fatalf("error = %v, want empty service response", err)
	}
}
