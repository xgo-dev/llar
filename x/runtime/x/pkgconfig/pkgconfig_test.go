package pkgconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/execbroker"
)

func setenv(t *testing.T, key, value string) {
	t.Helper()
	old, existed := execbroker.LookupEnv(key)
	if err := execbroker.Setenv(key, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = execbroker.Setenv(key, old)
		} else {
			_ = execbroker.Unsetenv(key)
		}
	})
}

func TestUse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "lib", "pkgconfig")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setenv(t, "PKG_CONFIG_PATH", "/existing")

	Use(root)

	if got, want := execbroker.Getenv("PKG_CONFIG_PATH"), dir+string(os.PathListSeparator)+"/existing"; got != want {
		t.Fatalf("PKG_CONFIG_PATH = %q, want %q", got, want)
	}
}

func TestUseIgnoresMissingDirectory(t *testing.T) {
	setenv(t, "PKG_CONFIG_PATH", "/existing")

	Use(t.TempDir())

	if got := execbroker.Getenv("PKG_CONFIG_PATH"); got != "/existing" {
		t.Fatalf("PKG_CONFIG_PATH = %q, want unchanged", got)
	}
}

func TestUseIgnoresSetenvError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "lib", "pkgconfig")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := execbroker.Do(execbroker.Scope{Env: []string{"PKG_CONFIG_PATH=\x00"}}, func() error {
		Use(root)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueries(t *testing.T) {
	tests := []struct {
		name  string
		query func(string) (string, error)
		args  []string
	}{
		{name: "lookup", query: Lookup, args: []string{"--cflags", "--libs", "demo"}},
		{name: "cflags", query: CFlags, args: []string{"--cflags", "demo"}},
		{name: "libs", query: Libs, args: []string{"--libs", "demo"}},
		{name: "static libs", query: StaticLibs, args: []string{"--static", "--libs", "demo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var request execbroker.Request
			var output string
			err := execbroker.Do(execbroker.Scope{
				Middleware: func(req execbroker.Request) (execbroker.Request, error) {
					request = req
					req.Name = os.Args[0]
					req.Args = []string{"-test.run=TestLookupHelperProcess"}
					req.Env = append(execbroker.Environ(), "GO_WANT_PKGCONFIG_HELPER=1")
					return req, nil
				},
			}, func() error {
				var err error
				output, err = tt.query("demo")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if request.Name != "pkg-config" {
				t.Fatalf("command = %q, want pkg-config", request.Name)
			}
			if !slices.Equal(request.Args, tt.args) {
				t.Fatalf("args = %q, want %q", request.Args, tt.args)
			}
			if output != "-I/include -ldemo" {
				t.Fatalf("output = %q", output)
			}
		})
	}
}

func TestQueryErrors(t *testing.T) {
	tests := []struct {
		name   string
		detail string
		want   string
	}{
		{name: "without stderr", want: `pkg-config "demo": exit status 1`},
		{name: "with stderr", detail: "package not found", want: `pkg-config "demo": exit status 1: package not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := execbroker.Do(execbroker.Scope{
				Middleware: func(req execbroker.Request) (execbroker.Request, error) {
					req.Name = os.Args[0]
					req.Args = []string{"-test.run=TestLookupHelperProcess"}
					req.Env = append(execbroker.Environ(),
						"GO_WANT_PKGCONFIG_HELPER=1",
						"GO_PKGCONFIG_HELPER_FAIL=1",
						"GO_PKGCONFIG_HELPER_STDERR="+tt.detail,
					)
					return req, nil
				},
			}, func() error {
				_, err := Libs("demo")
				return err
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLookupHelperProcess(t *testing.T) {
	if execbroker.Getenv("GO_WANT_PKGCONFIG_HELPER") != "1" {
		return
	}
	if execbroker.Getenv("GO_PKGCONFIG_HELPER_FAIL") == "1" {
		if detail := execbroker.Getenv("GO_PKGCONFIG_HELPER_STDERR"); detail != "" {
			fmt.Fprintln(os.Stderr, detail)
		}
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "  -I/include -ldemo  ")
	os.Exit(0)
}
