package internal

import (
	"bytes"
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
	"runtime"
	"strings"
	"testing"

	"github.com/goplus/llar/formula"
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

func runInstallCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	type flagState struct {
		name    string
		value   string
		changed bool
	}
	states := make([]flagState, 0, 3)
	for _, name := range []string{"verbose", "output", "json"} {
		flag := installCmd.Flags().Lookup(name)
		states = append(states, flagState{name: name, value: flag.Value.String(), changed: flag.Changed})
		if err := flag.Value.Set(flag.DefValue); err != nil {
			t.Fatalf("reset --%s: %v", name, err)
		}
		flag.Changed = false
	}
	defer func() {
		for _, state := range states {
			flag := installCmd.Flags().Lookup(state.name)
			_ = flag.Value.Set(state.value)
			flag.Changed = state.changed
		}
	}()

	origArgs := os.Args
	os.Args = append([]string{"llar", "install"}, args...)
	defer func() { os.Args = origArgs }()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	defer func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	}()

	rootCmd.SetArgs(append([]string{"install"}, args...))
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

func redirectDefaultHTTPClient(t *testing.T, server *httptest.Server) {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	origClient := http.DefaultClient
	transport := server.Client().Transport
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		redirected := req.Clone(req.Context())
		redirected.URL.Scheme = target.Scheme
		redirected.URL.Host = target.Host
		return transport.RoundTrip(redirected)
	})}
	t.Cleanup(func() { http.DefaultClient = origClient })
}

func TestInstallCommand(t *testing.T) {
	workspaceDir := isolatedWorkspaceDir(t)
	query := "arch=testarch&os=testos&shared=ON"
	rootArchive := makeInstallArtifact(t, ".zip", "include/root.h", "root", "/build/root", "-I/build/root/include -lroot", nil)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			if got := r.URL.Query().Encode(); got != query {
				t.Errorf("matrix query = %q, want %q", got, query)
			}
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeInstallCommand(t, w, "info", "resolving test/root")
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": "test/root@v1.0.0?" + query, "type": "zip", "url": server.URL + "/root.zip",
			})
		case "/root.zip":
			_, _ = w.Write(rootArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	redirectDefaultHTTPClient(t, server)

	output := filepath.Join(t.TempDir(), "root.zip")
	stdout, stderr, err := runInstallCmd(t,
		"--verbose", "--json", "--output", output,
		"--os", "testos", "--arch", "testarch", "--option", "shared=ON",
		"test/root",
	)
	if err != nil {
		t.Fatalf("llar install failed: %v", err)
	}
	if stderr != "resolving test/root\n" {
		t.Fatalf("stderr = %q, want llard progress", stderr)
	}
	var result moduleJSONResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nstdout:\n%s", err, stdout)
	}
	if result.Path != "test/root" || result.Version != "v1.0.0" {
		t.Fatalf("result = %+v, want test/root@v1.0.0", result)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("output artifact: %v", err)
	}
	extracted := t.TempDir()
	data, err := archiver.Unpack(output, extracted)
	if err != nil {
		t.Fatal(err)
	}
	assertInstallFile(t, filepath.Join(extracted, "include", "root.h"), "root")
	archiveInfo, err := metadata.Decode(data, extracted)
	if err != nil {
		t.Fatal(err)
	}
	if archiveInfo.Metadata != "-I"+filepath.Join(extracted, "include")+" -lroot" {
		t.Fatalf("archive metadata = %+v", archiveInfo)
	}
	installDir := filepath.Join(workspaceDir, "test/root@v1.0.0-testarch-testos|ON")
	assertInstallFile(t, filepath.Join(installDir, "include", "root.h"), "root")

	directoryOutput := filepath.Join(t.TempDir(), "root-out")
	_, _, err = runInstallCmd(t,
		"--output", directoryOutput,
		"--os", "testos", "--arch", "testarch", "--option", "shared=ON",
		"test/root",
	)
	if err != nil {
		t.Fatalf("llar install -o directory failed: %v", err)
	}
	assertInstallFile(t, filepath.Join(directoryOutput, "include", "root.h"), "root")
}

func TestInstallCommandReturnsLlardError(t *testing.T) {
	isolatedWorkspaceDir(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-cmdjsonl")
		writeInstallCommand(t, w, "error", "module not found")
	}))
	defer server.Close()
	redirectDefaultHTTPClient(t, server)

	_, _, err := runInstallCmd(t, "test/missing@v1.0.0")
	if err == nil || err.Error() != "llard: module not found" {
		t.Fatalf("llar install error = %v, want llard error", err)
	}
}

func TestWriteModuleOutputPrefersExistingDirectory(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "include", "root.h"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "root.zip")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeModuleOutput(moduleOutputResult{OutputDir: source}, dest); err != nil {
		t.Fatalf("writeModuleOutput() error = %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("output %q is not a directory", dest)
	}
	assertInstallFile(t, filepath.Join(dest, "include", "root.h"), "root")
}

func TestRunInstallReturnsMatrixError(t *testing.T) {
	origArgs := os.Args
	os.Args = []string{"llar", "install", "test/root", "--os"}
	defer func() { os.Args = origArgs }()

	err := runInstall(installCmd, []string{"test/root"})
	if err == nil || !strings.Contains(err.Error(), "missing value for matrix flag --os") {
		t.Fatalf("runInstall() error = %v, want matrix error", err)
	}
}

func TestRunInstallReturnsOutputWriteError(t *testing.T) {
	isolatedWorkspaceDir(t)
	query := url.Values{"arch": {runtime.GOARCH}, "os": {runtime.GOOS}}.Encode()
	rootArchive := makeInstallArtifact(t, ".zip", "include/root.h", "root", "/build/root", "-lroot", nil)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": "test/root@v1.0.0?" + query, "type": "zip", "url": server.URL + "/root.zip",
			})
		case "/root.zip":
			_, _ = w.Write(rootArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	redirectDefaultHTTPClient(t, server)

	origArgs := os.Args
	os.Args = []string{"llar", "install", "test/root"}
	defer func() { os.Args = origArgs }()
	installCmd.SetOut(errorWriter{})
	defer installCmd.SetOut(nil)

	err := runInstall(installCmd, []string{"test/root"})
	if err == nil || err.Error() != "write failed" {
		t.Fatalf("runInstall() error = %v, want output write error", err)
	}
}

func TestInstallDownloadsRootAndDependencies(t *testing.T) {
	workspaceDir := isolatedWorkspaceDir(t)
	matrix := hostMatrix()
	matrix.Options = map[string][]string{"shared": {"ON"}}
	matrixStr := matrix.Combinations()[0]
	query := url.Values{
		"arch":   {runtime.GOARCH},
		"os":     {runtime.GOOS},
		"shared": {"ON"},
	}.Encode()

	depID := "test/dep@v1.2.3?" + query
	rootID := "test/root@v1.0.0?" + query
	depArchive := makeInstallArtifact(t, ".zip", "lib/libdep.a", "dep", "/build/dep", "-L/build/dep/lib -ldep", nil)
	rootArchive := makeInstallArtifact(t, ".tar.gz", "include/root.h", "root", "/build/root", "-I/build/root/include -lroot", []module.Version{{Path: "test/dep", Version: "v1.2.3"}})

	var artifactRequests, rootDownloads, depDownloads int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			artifactRequests++
			if got := r.URL.Query().Encode(); got != query {
				t.Errorf("matrix query = %q, want %q", got, query)
			}
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeInstallCommand(t, w, "info", "resolving test/root")
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": depID, "type": "zip", "url": server.URL + "/downloads/dep.zip",
			})
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": rootID, "type": "tar.gz", "url": server.URL + "/downloads/root.tar.gz", "deps": []string{depID},
			})
		case "/downloads/dep.zip":
			depDownloads++
			_, _ = w.Write(depArchive)
		case "/downloads/root.tar.gz":
			rootDownloads++
			_, _ = w.Write(rootArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var progress bytes.Buffer
	result, err := install(context.Background(), &progress, server.URL, "test/root", matrix)
	if err != nil {
		t.Fatal(err)
	}
	if got := progress.String(); got != "resolving test/root\n" {
		t.Fatalf("progress = %q", got)
	}
	if result.Module != (module.Version{Path: "test/root", Version: "v1.0.0"}) {
		t.Fatalf("result module = %+v", result.Module)
	}
	if len(result.Deps) != 1 || result.Deps[0] != (module.Version{Path: "test/dep", Version: "v1.2.3"}) {
		t.Fatalf("result deps = %+v", result.Deps)
	}
	if result.Metadata != "-I"+filepath.Join(result.OutputDir, "include")+" -lroot" {
		t.Fatalf("result metadata = %q", result.Metadata)
	}
	if rootDownloads != 1 || depDownloads != 1 {
		t.Fatalf("downloads = root:%d dep:%d, want one each", rootDownloads, depDownloads)
	}
	if artifactRequests != 1 {
		t.Fatalf("llard artifact requests = %d, want one", artifactRequests)
	}

	depDir := filepath.Join(workspaceDir, fmt.Sprintf("test/dep@v1.2.3-%s", matrixStr))
	rootDir := filepath.Join(workspaceDir, fmt.Sprintf("test/root@v1.0.0-%s", matrixStr))
	assertInstallFile(t, filepath.Join(depDir, "lib", "libdep.a"), "dep")
	assertInstallFile(t, filepath.Join(rootDir, "include", "root.h"), "root")
	assertInstallCache(t, workspaceDir, "test/dep", "v1.2.3", matrixStr, "-L"+filepath.Join(depDir, "lib")+" -ldep")
	assertInstallCache(t, workspaceDir, "test/root", "v1.0.0", matrixStr, "-I"+filepath.Join(rootDir, "include")+" -lroot")

	var plain bytes.Buffer
	if err := writeModuleResult(&plain, result, false); err != nil {
		t.Fatal(err)
	}
	if got := plain.String(); got != result.Metadata+"\n" {
		t.Fatalf("plain output = %q", got)
	}
	plain.Reset()
	if err := writeModuleResult(&plain, moduleOutputResult{}, false); err != nil {
		t.Fatal(err)
	}
	if plain.Len() != 0 {
		t.Fatalf("empty metadata output = %q, want empty", plain.String())
	}

	var jsonOutput bytes.Buffer
	if err := writeModuleResult(&jsonOutput, result, true); err != nil {
		t.Fatal(err)
	}
	var jsonResult moduleJSONResult
	if err := json.Unmarshal(jsonOutput.Bytes(), &jsonResult); err != nil {
		t.Fatal(err)
	}
	if jsonResult.Path != "test/root" || jsonResult.Version != "v1.0.0" || len(jsonResult.Deps) != 1 {
		t.Fatalf("JSON result = %+v", jsonResult)
	}

}

func TestInstallSkipsCachedArtifacts(t *testing.T) {
	workspaceDir := isolatedWorkspaceDir(t)
	matrix := hostMatrix()
	matrix.Options = map[string][]string{"shared": {"ON"}}
	matrixStr := matrix.Combinations()[0]
	query := url.Values{
		"arch":   {runtime.GOARCH},
		"os":     {runtime.GOOS},
		"shared": {"ON"},
	}.Encode()

	depDir := filepath.Join(workspaceDir, fmt.Sprintf("test/dep@v1.2.3-%s", matrixStr))
	depMetadata := "-L" + filepath.Join(depDir, "lib") + " -ldep"
	prepopulateCache(t, workspaceDir, "test/dep", "v1.2.3", matrixStr, depMetadata)
	depSentinel := filepath.Join(depDir, "cached.txt")
	if err := os.WriteFile(depSentinel, []byte("cached dependency"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootArchive := makeInstallArtifact(t, ".zip", "include/root.h", "root", "/build/root", "-I/build/root/include -lroot", nil)
	depArchive := makeInstallArtifact(t, ".zip", "lib/libdep.a", "downloaded dep", "/build/dep", "-ldep", nil)
	rootID := "test/root@v1.0.0?" + query
	depID := "test/dep@v1.2.3?" + query
	var manifestRequests, rootDownloads, depDownloads int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			manifestRequests++
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": depID, "type": "zip", "url": server.URL + "/dep.zip",
			})
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": rootID, "type": "zip", "url": server.URL + "/root.zip",
			})
		case "/dep.zip":
			depDownloads++
			_, _ = w.Write(depArchive)
		case "/root.zip":
			rootDownloads++
			_, _ = w.Write(rootArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := install(context.Background(), nil, server.URL, "test/root", matrix)
	if err != nil {
		t.Fatal(err)
	}
	if manifestRequests != 1 || rootDownloads != 1 || depDownloads != 0 {
		t.Fatalf("requests = manifest:%d root:%d dep:%d, want 1, 1, 0", manifestRequests, rootDownloads, depDownloads)
	}
	assertInstallFile(t, depSentinel, "cached dependency")
	if result.Module != (module.Version{Path: "test/root", Version: "v1.0.0"}) {
		t.Fatalf("result module = %+v", result.Module)
	}
	if len(result.Deps) != 1 || result.Deps[0] != (module.Version{Path: "test/dep", Version: "v1.2.3"}) {
		t.Fatalf("result deps = %+v", result.Deps)
	}
	if result.Metadata != "-I"+filepath.Join(result.OutputDir, "include")+" -lroot" {
		t.Fatalf("result metadata = %q", result.Metadata)
	}

	rootHeader := filepath.Join(result.OutputDir, "include", "root.h")
	if err := os.WriteFile(rootHeader, []byte("cached root"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = install(context.Background(), nil, server.URL, "test/root", matrix)
	if err != nil {
		t.Fatal(err)
	}
	if manifestRequests != 2 || rootDownloads != 1 || depDownloads != 0 {
		t.Fatalf("requests after cache hit = manifest:%d root:%d dep:%d, want 2, 1, 0", manifestRequests, rootDownloads, depDownloads)
	}
	assertInstallFile(t, rootHeader, "cached root")
	assertInstallFile(t, depSentinel, "cached dependency")
	if result.Metadata != "-I"+filepath.Join(result.OutputDir, "include")+" -lroot" {
		t.Fatalf("cached result metadata = %q", result.Metadata)
	}
}

func TestInstallOutputFlags(t *testing.T) {
	if !installCmd.FParseErrWhitelist.UnknownFlags {
		t.Fatal("install command does not allow matrix shortcut flags")
	}
	for _, tt := range []struct {
		name      string
		shorthand string
	}{
		{name: "verbose", shorthand: "v"},
		{name: "output", shorthand: "o"},
		{name: "json", shorthand: "j"},
	} {
		flag := installCmd.Flags().Lookup(tt.name)
		if flag == nil {
			t.Fatalf("flag --%s is missing", tt.name)
		}
		if flag.Shorthand != tt.shorthand {
			t.Fatalf("flag --%s shorthand = %q, want %q", tt.name, flag.Shorthand, tt.shorthand)
		}
	}
}

func TestInstallRejectsLocalFormula(t *testing.T) {
	abs, err := filepath.Abs("formula")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{".", "..", "./formula", "../formula@v1.0.0", abs} {
		t.Run(arg, func(t *testing.T) {
			_, err := install(context.Background(), nil, "not a URL", arg, hostMatrix())
			if err == nil || !strings.Contains(err.Error(), "does not support local formulas") {
				t.Fatalf("install() error = %v, want local formula error", err)
			}
		})
	}
}

func TestInstallRejectsInvalidInputAndEnvironment(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	t.Run("module argument", func(t *testing.T) {
		_, err := install(context.Background(), nil, server.URL, ".@v1.0.0", hostMatrix())
		if err == nil || !strings.Contains(err.Error(), "invalid local pattern") {
			t.Fatalf("install() error = %v, want module argument error", err)
		}
	})
	t.Run("service URL", func(t *testing.T) {
		_, err := install(context.Background(), nil, "not a URL", "test/root", hostMatrix())
		if err == nil || !strings.Contains(err.Error(), "invalid llard service URL") {
			t.Fatalf("install() error = %v, want service URL error", err)
		}
	})
	t.Run("user cache", func(t *testing.T) {
		t.Setenv("HOME", "")
		t.Setenv("XDG_CACHE_HOME", "")
		_, err := install(context.Background(), nil, server.URL, "test/root", hostMatrix())
		if err == nil {
			t.Fatal("install() succeeded without a user cache directory")
		}
	})
	t.Run("workspace", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "home")
		if err := os.WriteFile(home, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CACHE_HOME", "")
		_, err := install(context.Background(), nil, server.URL, "test/root", hostMatrix())
		if err == nil {
			t.Fatal("install() succeeded with an invalid workspace path")
		}
	})
}

func TestInstallRejectsInvalidMatrix(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

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
			_, err := install(context.Background(), nil, server.URL, "test/root", tt.matrix)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("install() error = %v, want %q", err, tt.want)
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

	_, err := install(context.Background(), nil, server.URL, "test/missing@v1.0.0", hostMatrix())
	if err == nil || err.Error() != "llard: module not found" {
		t.Fatalf("install() error = %v, want llard error", err)
	}
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

func TestInstallRemovesDownloadedArtifactOnError(t *testing.T) {
	isolatedWorkspaceDir(t)
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	query := "arch=" + runtime.GOARCH + "&os=" + runtime.GOOS

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": "test/root@v1.0.0?" + query, "type": "zip", "url": server.URL + "/root.zip",
			})
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": "test/dep@v1.0.0?" + query, "type": "zip", "url": server.URL + "/dep.zip",
			})
		case "/root.zip":
			_, _ = w.Write([]byte("not a zip archive"))
		case "/dep.zip":
			_, _ = w.Write([]byte("unprocessed dependency"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := install(context.Background(), nil, server.URL, "test/root", hostMatrix())
	if err == nil || !strings.Contains(err.Error(), "install artifact test/root@v1.0.0") {
		t.Fatalf("install() error = %v, want artifact installation error", err)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary artifacts were not removed: %v", entries)
	}
}

func TestInstallReturnsInvalidModulePath(t *testing.T) {
	isolatedWorkspaceDir(t)
	matrix := hostMatrix()
	query := url.Values{"arch": {runtime.GOARCH}, "os": {runtime.GOOS}}.Encode()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-cmdjsonl")
		writeInstallCommand(t, w, "artifact", map[string]any{
			"id": "../bad@v1.0.0?" + query, "type": "zip", "url": server.URL + "/artifact.zip",
		})
		writeInstallCommand(t, w, "artifact", map[string]any{
			"id": "test/root@v1.0.0?" + query, "type": "zip", "url": server.URL + "/artifact.zip",
		})
	}))
	defer server.Close()

	_, err := install(context.Background(), nil, server.URL, "test/root", matrix)
	if err == nil || !strings.Contains(err.Error(), "invalid artifact id") {
		t.Fatalf("install() error = %v, want invalid module error", err)
	}
}

func TestInstallReturnsCacheError(t *testing.T) {
	matrix := hostMatrix()
	query := url.Values{"arch": {runtime.GOARCH}, "os": {runtime.GOOS}}.Encode()
	rootArchive := makeInstallArtifact(t, ".zip", "include/root.h", "root", "/build/root", "-lroot", nil)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/artifacts/test/root":
			w.Header().Set("Content-Type", "application/x-cmdjsonl")
			writeInstallCommand(t, w, "artifact", map[string]any{
				"id": "test/root@v1.0.0?" + query, "type": "zip", "url": server.URL + "/root.zip",
			})
		case "/root.zip":
			_, _ = w.Write(rootArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	workspaceDir := isolatedWorkspaceDir(t)
	if err := os.MkdirAll(filepath.Join(workspaceDir, "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "test", "root"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := install(context.Background(), nil, server.URL, "test/root", matrix)
	if err == nil || !strings.Contains(err.Error(), "cache artifact test/root@v1.0.0") {
		t.Fatalf("install() error = %v, want cache error", err)
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
