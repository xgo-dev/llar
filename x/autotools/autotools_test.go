package autotools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestUseSetsEnv(t *testing.T) {
	root := t.TempDir()
	includeDir := filepath.Join(root, "include")
	libDir := filepath.Join(root, "lib")
	pkgconfigDir := filepath.Join(libDir, "pkgconfig")
	for _, d := range []string{includeDir, libDir, pkgconfigDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	for _, key := range []string{
		"PKG_CONFIG_PATH", "CMAKE_PREFIX_PATH", "CMAKE_INCLUDE_PATH",
		"CMAKE_LIBRARY_PATH", "INCLUDE", "LIB", "CPPFLAGS", "LDFLAGS",
	} {
		setenv(t, key, "")
	}

	a := New("", "", "")
	a.Use(root)

	for key, want := range map[string]string{
		"PKG_CONFIG_PATH":    pkgconfigDir,
		"CMAKE_PREFIX_PATH":  root,
		"CMAKE_INCLUDE_PATH": includeDir,
		"CMAKE_LIBRARY_PATH": libDir,
	} {
		if got := execbroker.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if runtime.GOOS == "windows" {
		if got := execbroker.Getenv("INCLUDE"); got != includeDir {
			t.Errorf("INCLUDE = %q, want %q", got, includeDir)
		}
		if got := execbroker.Getenv("LIB"); got != libDir {
			t.Errorf("LIB = %q, want %q", got, libDir)
		}
	} else {
		if got := execbroker.Getenv("CPPFLAGS"); strings.TrimSpace(got) != "-I"+includeDir {
			t.Errorf("CPPFLAGS = %q, want %q", got, "-I"+includeDir)
		}
		if got := execbroker.Getenv("LDFLAGS"); strings.TrimSpace(got) != "-L"+libDir {
			t.Errorf("LDFLAGS = %q, want %q", got, "-L"+libDir)
		}
	}
}

func TestUseMultipleDeps(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	for _, r := range []string{root1, root2} {
		os.MkdirAll(filepath.Join(r, "include"), 0o755)
		os.MkdirAll(filepath.Join(r, "lib"), 0o755)
	}

	setenv(t, "CMAKE_INCLUDE_PATH", "")
	setenv(t, "CMAKE_LIBRARY_PATH", "")
	setenv(t, "CMAKE_PREFIX_PATH", "")
	setenv(t, "CPPFLAGS", "")
	setenv(t, "LDFLAGS", "")

	a := New("", "", "")
	a.Use(root1)
	a.Use(root2)

	// prependPath: root2 should be prepended before root1
	got := execbroker.Getenv("CMAKE_PREFIX_PATH")
	if !strings.HasPrefix(got, root2) {
		t.Errorf("CMAKE_PREFIX_PATH = %q, expected %q to be first", got, root2)
	}
	if !strings.Contains(got, root1) {
		t.Errorf("CMAKE_PREFIX_PATH = %q, missing %q", got, root1)
	}

	// appendFlag: root1 flag should come before root2 flag
	cppflags := execbroker.Getenv("CPPFLAGS")
	i1 := strings.Index(cppflags, filepath.Join(root1, "include"))
	i2 := strings.Index(cppflags, filepath.Join(root2, "include"))
	if i1 < 0 || i2 < 0 || i1 >= i2 {
		t.Errorf("CPPFLAGS = %q, expected root1 before root2", cppflags)
	}
}

func TestUsePartialDirs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "include"), 0o755)

	for _, key := range []string{
		"PKG_CONFIG_PATH", "CMAKE_LIBRARY_PATH", "CPPFLAGS", "LDFLAGS",
	} {
		setenv(t, key, "")
	}

	a := New("", "", "")
	a.Use(root)

	if got := execbroker.Getenv("PKG_CONFIG_PATH"); got != "" {
		t.Errorf("PKG_CONFIG_PATH = %q, want empty", got)
	}
	if got := execbroker.Getenv("CMAKE_LIBRARY_PATH"); got != "" {
		t.Errorf("CMAKE_LIBRARY_PATH = %q, want empty", got)
	}
}

func TestSource(t *testing.T) {
	a := New("original", "", "")
	a.Source("/new/src")
	if a.sourceDir != "/new/src" {
		t.Errorf("sourceDir = %q, want %q", a.sourceDir, "/new/src")
	}
}

func TestOutputDir(t *testing.T) {
	if got := New("", "build", "").OutputDir(); got != "build" {
		t.Errorf("OutputDir = %q, want %q", got, "build")
	}
	if got := New("", "build", "inst").OutputDir(); got != "inst" {
		t.Errorf("OutputDir = %q, want %q", got, "inst")
	}
}

func TestWorkDir(t *testing.T) {
	if got := New("", "", "").workDir(); got != "." {
		t.Errorf("workDir empty = %q, want %q", got, ".")
	}
	if got := New("", "/tmp/b", "").workDir(); got != "/tmp/b" {
		t.Errorf("workDir set = %q, want %q", got, "/tmp/b")
	}
}

func TestPrependPath(t *testing.T) {
	setenv(t, "TEST_PREPEND", "/existing")
	prependPath("TEST_PREPEND", "/new")

	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	want := "/new" + sep + "/existing"
	if got := execbroker.Getenv("TEST_PREPEND"); got != want {
		t.Errorf("TEST_PREPEND = %q, want %q", got, want)
	}
}

func TestAppendFlag(t *testing.T) {
	setenv(t, "TEST_FLAGS", "-Ifoo")
	appendFlag("TEST_FLAGS", "-Ibar")

	want := "-Ifoo -Ibar"
	if got := execbroker.Getenv("TEST_FLAGS"); got != want {
		t.Errorf("TEST_FLAGS = %q, want %q", got, want)
	}
}

func TestConfigureBuildInstallE2E(t *testing.T) {
	for _, bin := range []string{"make", "cc", "ar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found in PATH", bin)
		}
	}

	tmp := t.TempDir()
	installDir := filepath.Join(tmp, "install")
	buildDir := filepath.Join(tmp, "build")

	absSource, err := filepath.Abs(filepath.Join("testdata", "project"))
	if err != nil {
		t.Fatal(err)
	}

	a := New(absSource, buildDir, installDir)

	a.Configure("--enable-foo")
	a.Build()
	a.Install()

	data, err := os.ReadFile(filepath.Join(buildDir, "config.log"))
	if err != nil {
		t.Fatalf("read config.log: %v", err)
	}
	if log := string(data); !strings.Contains(log, "PREFIX="+installDir) {
		t.Errorf("config.log missing PREFIX=%s", installDir)
	}

	for _, path := range []string{
		filepath.Join(installDir, "lib", "libdummy.a"),
		filepath.Join(installDir, "include", "dummy.h"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s", path)
		}
	}
}

func TestConfigureBuildInstallWithPipeInPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell metacharacter path is not a valid Windows directory name")
	}
	for _, bin := range []string{"make", "cc", "ar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found in PATH", bin)
		}
	}

	tmp := t.TempDir()
	installDir := filepath.Join(tmp, "install|matrix")
	buildDir := filepath.Join(tmp, "build")

	absSource, err := filepath.Abs(filepath.Join("testdata", "project"))
	if err != nil {
		t.Fatal(err)
	}

	a := New(absSource, buildDir, installDir)
	a.Configure()
	a.Build()
	a.Install()

	for _, path := range []string{
		filepath.Join(installDir, "lib", "libdummy.a"),
		filepath.Join(installDir, "include", "dummy.h"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s: %v", path, err)
		}
	}
}

func TestConfigureNoPrefix(t *testing.T) {
	for _, bin := range []string{"make", "cc", "ar"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found in PATH", bin)
		}
	}

	tmp := t.TempDir()
	buildDir := filepath.Join(tmp, "build")

	absSource, err := filepath.Abs(filepath.Join("testdata", "project"))
	if err != nil {
		t.Fatal(err)
	}

	// No installDir → no --prefix
	a := New(absSource, buildDir, "")
	a.Configure()

	data, err := os.ReadFile(filepath.Join(buildDir, "config.log"))
	if err != nil {
		t.Fatalf("read config.log: %v", err)
	}
	if strings.Contains(string(data), "--prefix") {
		t.Error("config.log should not contain --prefix when installDir is empty")
	}
}
