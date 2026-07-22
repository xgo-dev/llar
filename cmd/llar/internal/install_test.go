package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
)

func TestInstallDownloadsRootAndDependencies(t *testing.T) {
	workspaceDir := isolatedWorkspaceDir(t)
	matrixStr := computeMatrixStr()
	query := url.Values{
		"arch": {runtime.GOARCH},
		"os":   {runtime.GOOS},
	}.Encode()

	depID := "test/dep@v1.2.3?" + query
	rootID := "test/root@v1.0.0?" + query
	depArchive := makeInstallArtifact(t, ".zip", "lib/libdep.a", "dep", "/build/dep", "-L/build/dep/lib -ldep", nil)
	rootArchive := makeInstallArtifact(t, ".tar.gz", "include/root.h", "root", "/build/root", "-I/build/root/include -lroot", []module.Version{{Path: "test/dep", Version: "v1.2.3"}})

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			if got := r.URL.Query().Encode(); got != query {
				t.Errorf("matrix query = %q, want %q", got, query)
			}
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeInstallCommand(t, w, "info", "resolving test/root")
			writeInstallCommand(t, w, "artifact", installArtifactMessage{
				ID: rootID, Type: "tar.gz", URL: server.URL + "/downloads/root.tar.gz", Deps: []string{depID},
			})
			writeInstallCommand(t, w, "artifact", installArtifactMessage{
				ID: depID, Type: "zip", URL: server.URL + "/downloads/dep.zip",
			})
		case "/downloads/dep.zip":
			_, _ = w.Write(depArchive)
		case "/downloads/root.tar.gz":
			_, _ = w.Write(rootArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var progress bytes.Buffer
	if err := install(context.Background(), &progress, server.Client(), server.URL, "test/root"); err != nil {
		t.Fatal(err)
	}
	if got := progress.String(); got != "resolving test/root\n" {
		t.Fatalf("progress = %q", got)
	}

	depDir := filepath.Join(workspaceDir, fmt.Sprintf("test/dep@v1.2.3-%s", matrixStr))
	rootDir := filepath.Join(workspaceDir, fmt.Sprintf("test/root@v1.0.0-%s", matrixStr))
	assertInstallFile(t, filepath.Join(depDir, "lib", "libdep.a"), "dep")
	assertInstallFile(t, filepath.Join(rootDir, "include", "root.h"), "root")
	assertInstallCache(t, workspaceDir, "test/dep", "v1.2.3", matrixStr, "-L"+filepath.Join(depDir, "lib")+" -ldep")
	assertInstallCache(t, workspaceDir, "test/root", "v1.0.0", matrixStr, "-I"+filepath.Join(rootDir, "include")+" -lroot")
}

func TestInstallRejectsLocalFormula(t *testing.T) {
	abs, err := filepath.Abs("formula")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{".", "..", "./formula", "../formula@v1.0.0", abs} {
		t.Run(arg, func(t *testing.T) {
			err := install(context.Background(), nil, http.DefaultClient, "not a URL", arg)
			if err == nil || !strings.Contains(err.Error(), "does not support local formulas") {
				t.Fatalf("install() error = %v, want local formula error", err)
			}
		})
	}
}

func TestInstallReturnsLlardError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-cmdjsonl")
		writeInstallCommand(t, w, "info", "resolving")
		writeInstallCommand(t, w, "error", "module not found")
	}))
	defer server.Close()

	err := install(context.Background(), nil, server.Client(), server.URL, "test/missing@v1.0.0")
	if err == nil || err.Error() != "llard: module not found" {
		t.Fatalf("install() error = %v, want llard error", err)
	}
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

func assertInstallCache(t *testing.T, workspaceDir, modPath, version, matrixStr, wantMetadata string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workspaceDir, filepath.FromSlash(modPath), ".cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cached struct {
		Cache map[string]struct {
			Metadata string `json:"metadata"`
		} `json:"cache"`
	}
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatal(err)
	}
	entry, ok := cached.Cache[version+"-"+matrixStr]
	if !ok {
		t.Fatalf("cache entry %q is missing", version+"-"+matrixStr)
	}
	if entry.Metadata != wantMetadata {
		t.Fatalf("cache metadata = %q, want %q", entry.Metadata, wantMetadata)
	}
}
