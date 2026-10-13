// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goplus/ixgo"
	"github.com/goplus/llar/internal/execbroker"
	formulapkg "github.com/goplus/llar/runtime/formula"
)

func TestLoadFS(t *testing.T) {
	t.Run("ValidFormula", func(t *testing.T) {
		fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
		f, err := loadFS(fsys, "hello_llar.gox")
		if err != nil {
			t.Fatalf("LoadFS failed: %v", err)
		}
		// Verify metadata
		if f.ModPath != "DaveGamble/cJSON" {
			t.Errorf("Unexpected ModPath: want %s got %s", "DaveGamble/cJSON", f.ModPath)
		}
		if f.FromVer != "v1.0.0" {
			t.Errorf("Unexpected FromVer: want %s got %s", "v1.0.0", f.FromVer)
		}
		if f.OnBuild == nil {
			t.Error("OnBuild is nil")
		}
		if f.OnRequire == nil {
			t.Error("OnRequire is nil")
		}
		if f.OnTest == nil {
			t.Error("OnTest is nil")
		}

		// Functional test: verify callbacks can be invoked without panic
		f.OnRequire(&formulapkg.Project{}, &formulapkg.ModuleDeps{})
		f.OnBuild(&formulapkg.Context{})
		f.OnTest(&formulapkg.Context{})
	})

	t.Run("UnderscoreInStructName", func(t *testing.T) {
		fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
		f, err := loadFS(fsys, "cpu_features_llar.gox")
		if err != nil {
			t.Fatalf("LoadFS failed: %v", err)
		}
		if f.ModPath != "google/cpu_features" {
			t.Fatalf("ModPath = %q, want %q", f.ModPath, "google/cpu_features")
		}
	})

	t.Run("NonExistentFile", func(t *testing.T) {
		fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
		_, err := loadFS(fsys, "nonexistent.gox")
		if err == nil {
			t.Error("LoadFS should return error for non-existent file")
		}
	})

	t.Run("InvalidSyntax", func(t *testing.T) {
		tmpDir := t.TempDir()
		os.WriteFile(tmpDir+"/invalid_llar.gox", []byte("this is not valid gox code !!!@@@"), 0644)
		fsys := os.DirFS(tmpDir).(fs.ReadFileFS)
		_, err := loadFS(fsys, "invalid_llar.gox")
		if err == nil {
			t.Error("LoadFS should return error for invalid syntax")
		}
	})
}

func TestLoadFS_TargetSurface(t *testing.T) {
	fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
	f, err := loadFS(fsys, "targetsurface_llar.gox")
	if err != nil {
		t.Fatalf("LoadFS failed: %v", err)
	}
	if f.OnRequire == nil {
		t.Fatal("OnRequire is nil")
	}
	if f.OnBuild == nil {
		t.Fatal("OnBuild is nil")
	}
	if f.Filter == nil {
		t.Fatal("Filter is nil")
	}
	if !f.Filter() {
		t.Fatal("Filter() = false, want true")
	}

	var deps formulapkg.ModuleDeps
	f.OnRequire(&formulapkg.Project{}, &deps)
	gotDeps := deps.Deps()
	if len(gotDeps) != 1 || gotDeps[0].Path != "madler/zlib" || gotDeps[0].Version != "v1.3.1" {
		t.Fatalf("deps = %+v, want [madler/zlib@v1.3.1]", gotDeps)
	}
	f.OnBuild(&formulapkg.Context{})
}

func TestFormulaPkgConfigFileAutoImport(t *testing.T) {
	f, err := loadFS(os.DirFS("testdata/formula").(fs.ReadFileFS), "pkgconfigusage_llar.gox")
	if err != nil {
		t.Fatalf("LoadFS failed: %v", err)
	}
	if _, err := exec.LookPath("pkg-config"); err != nil {
		t.Skip("pkg-config is not installed")
	}

	installDir := t.TempDir()
	ctx := formulapkg.NewContext(&formulapkg.Project{}, "", installDir, "", nil)
	env := append(os.Environ(), "PKG_CONFIG_PATH=")
	if err := execbroker.Do(execbroker.Scope{Env: env}, func() error {
		f.OnBuild(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	pcPath := filepath.Join(installDir, "lib", "pkgconfig", "llar-formula-pkgconfig-test.pc")
	data, err := os.ReadFile(pcPath)
	if err != nil {
		t.Fatalf("generated pkg-config file: %v", err)
	}
	wantProperties := "Requires: llar-formula-pkgconfig-dependency >= 1.0.0\n" +
		"Libs: -L${libdir} -lllar_formula_pkgconfig_test\n" +
		"Libs.private: -lm\n" +
		"Libs.shared: -lllar_formula_shared\n" +
		"Cflags: -I${includedir} -DLLAR_FORMULA_PKGCONFIG_TEST\n" +
		"Cflags.private: -DLLAR_FORMULA_STATIC\n" +
		"Cflags.shared: -DLLAR_FORMULA_SHARED\n"
	if !strings.HasSuffix(string(data), wantProperties) {
		t.Fatalf("generated properties:\n%s\nwant suffix:\n%s", data, wantProperties)
	}
	got := strings.Fields(ctx.Out.Metadata())
	for i, flag := range got {
		if strings.HasPrefix(flag, "-I") || strings.HasPrefix(flag, "-L") {
			got[i] = flag[:2] + filepath.Clean(flag[2:])
		}
	}
	wantPublic := []string{
		"-I" + filepath.Join(installDir, "include"),
		"-DLLAR_FORMULA_PKGCONFIG_TEST",
		"-L" + filepath.Join(installDir, "lib"),
		"-l" + "llar_formula_pkgconfig_test",
	}
	wantShared := []string{
		"-I" + filepath.Join(installDir, "include"),
		"-DLLAR_FORMULA_PKGCONFIG_TEST",
		"-DLLAR_FORMULA_SHARED",
		"-L" + filepath.Join(installDir, "lib"),
		"-l" + "llar_formula_pkgconfig_test",
		"-lllar_formula_shared",
	}
	if !slices.Equal(got, wantPublic) && !slices.Equal(got, wantShared) {
		t.Fatalf("metadata = %q, want public %q or pkgconf shared %q", got, wantPublic, wantShared)
	}
}

func TestClone(t *testing.T) {
	fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
	_, before, _ := ixgo.IcallStat()
	template, err := loadFS(fsys, "targetsurface_llar.gox")
	if err != nil {
		t.Fatalf("LoadFS failed: %v", err)
	}
	first := Clone(template)
	second := Clone(template)
	for _, f := range []*Formula{template, first, second} {
		if _, err := fs.ReadFile(f.FS, "targetsurface_llar.gox"); err != nil {
			t.Fatalf("formula source directory was not preserved: %v", err)
		}
	}
	_, after, _ := ixgo.IcallStat()
	if after != before || template.program != nil || first.program != nil || second.program != nil {
		t.Fatal("loading metadata or cloning created an interpreter before a hook call")
	}
	if !first.Filter() {
		t.Fatal("first Filter did not load the default options")
	}
	// A later interpreter must not invalidate the shared program's Main method.
	other, err := loadFS(fsys, "hello_llar.gox")
	if err != nil {
		t.Fatalf("second LoadFS failed: %v", err)
	}
	other.OnBuild(&formulapkg.Context{})
	if !second.Filter() {
		t.Fatal("second Filter did not load the default options")
	}
	if first.program != template.program || second.program != template.program {
		t.Fatal("clones did not reuse the source formula program")
	}
	if first.structElem.Addr().Pointer() == second.structElem.Addr().Pointer() {
		t.Fatal("clones share a class instance")
	}
	first.SetMatrix(formulapkg.Matrix{
		Options: map[string][]string{"zlib": {"ON"}},
	})
	second.SetMatrix(formulapkg.Matrix{
		Options: map[string][]string{"zlib": {"OFF"}},
	})

	if !first.Filter() {
		t.Fatal("first Filter() = false, want true")
	}
	if second.Filter() {
		t.Fatal("second Filter() = true, want false")
	}

	var firstDeps, secondDeps formulapkg.ModuleDeps
	first.OnRequire(&formulapkg.Project{}, &firstDeps)
	second.OnRequire(&formulapkg.Project{}, &secondDeps)
	if got := firstDeps.Deps(); len(got) != 1 || got[0].Path != "madler/zlib" {
		t.Fatalf("first deps = %+v", got)
	}
	if got := secondDeps.Deps(); len(got) != 0 {
		t.Fatalf("second deps = %+v, want none", got)
	}
}

func TestFormulaProgramCleanup(t *testing.T) {
	runtime.GC()
	time.Sleep(10 * time.Millisecond)

	_, before, _ := ixgo.IcallStat()

	var loaded int
	var onBuild func(*formulapkg.Context)
	func() {
		fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
		f, err := loadFS(fsys, "targetsurface_llar.gox")
		if err != nil {
			t.Fatalf("LoadFS failed: %v", err)
		}
		for range 16 {
			Clone(f).Filter()
		}

		_, loaded, _ = ixgo.IcallStat()
		onBuild = f.OnBuild
	}()
	if loaded <= before {
		t.Fatalf("allocated icall slots after load = %d, want more than %d", loaded, before)
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	_, withHook, _ := ixgo.IcallStat()
	if withHook <= before {
		t.Fatal("formula program was released while OnBuild remained reachable")
	}
	onBuild(&formulapkg.Context{})
	onBuild = nil

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)

		_, allocated, _ := ixgo.IcallStat()
		if allocated <= before {
			return
		}
	}
	t.Fatalf("allocated icall slots did not return to baseline %d", before)
}

func TestFormulaPrintUsesBrokerScope(t *testing.T) {
	f, err := loadFS(os.DirFS("testdata/formula").(fs.ReadFileFS), "hello_llar.gox")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	var stdout bytes.Buffer
	err = execbroker.Do(execbroker.Scope{Stdout: &stdout}, func() error {
		f.OnBuild(&formulapkg.Context{})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "hello\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestFormulaEnvironmentUsesBrokerScope(t *testing.T) {
	const key = "FORMULA_ENV_TEST"
	t.Setenv(key, "process")

	f, err := loadFS(os.DirFS("testdata/formula").(fs.ReadFileFS), "env_llar.gox")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	var stdout bytes.Buffer
	err = execbroker.Do(execbroker.Scope{Stdout: &stdout}, func() error {
		f.OnBuild(&formulapkg.Context{})
		if got := os.Getenv(key); got != "process" {
			t.Fatalf("process environment = %q, want process", got)
		}
		var got string
		prefix := key + "="
		for _, entry := range execbroker.Command("command").Env {
			if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
				got = entry[len(prefix):]
			}
		}
		if got != "formula-again" {
			t.Fatalf("command environment = %q, want formula-again", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "process\nprocess\ntrue\nformula\nformula\nenvironment\nsyscall\ntrue\n\n\nformula-again\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got := os.Getenv(key); got != "process" {
		t.Fatalf("process environment after scope = %q, want process", got)
	}
}
