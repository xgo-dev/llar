// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"bytes"
	"io/fs"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/goplus/ixgo"
	formulapkg "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/execbroker"
	llarixgo "github.com/goplus/llar/internal/ixgo"
)

func TestLoadFS(t *testing.T) {
	t.Run("ValidFormula", func(t *testing.T) {
		fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
		f, err := LoadFS(fsys, "hello_llar.gox")
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
		f.OnBuild(&formulapkg.Context{}, &formulapkg.Project{}, &formulapkg.BuildResult{})
		f.OnTest(&formulapkg.Context{}, &formulapkg.Project{}, &formulapkg.TestResult{})
	})

	t.Run("NonExistentFile", func(t *testing.T) {
		fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
		_, err := LoadFS(fsys, "nonexistent.gox")
		if err == nil {
			t.Error("LoadFS should return error for non-existent file")
		}
	})

	t.Run("InvalidSyntax", func(t *testing.T) {
		tmpDir := t.TempDir()
		os.WriteFile(tmpDir+"/invalid_llar.gox", []byte("this is not valid gox code !!!@@@"), 0644)
		fsys := os.DirFS(tmpDir).(fs.ReadFileFS)
		_, err := LoadFS(fsys, "invalid_llar.gox")
		if err == nil {
			t.Error("LoadFS should return error for invalid syntax")
		}
	})
}

func TestLoadFS_TargetSurface(t *testing.T) {
	fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
	f, err := LoadFS(fsys, "targetsurface_llar.gox")
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
	f.OnBuild(&formulapkg.Context{}, &formulapkg.Project{}, &formulapkg.BuildResult{})
}

func TestClone(t *testing.T) {
	fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
	template, err := LoadFS(fsys, "targetsurface_llar.gox")
	if err != nil {
		t.Fatalf("LoadFS failed: %v", err)
	}
	// A later interpreter must not invalidate the template's Main method.
	if _, err := LoadFS(fsys, "hello_llar.gox"); err != nil {
		t.Fatalf("second LoadFS failed: %v", err)
	}

	first := Clone(template)
	second := Clone(template)
	setValue(first.structElem, "target", formulapkg.Matrix{
		Options: map[string][]string{"zlib": {"ON"}},
	})
	setValue(second.structElem, "target", formulapkg.Matrix{
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

	llarixgo.LockInterp()
	_, before, _ := ixgo.IcallStat()
	llarixgo.UnlockInterp()

	var loaded int
	var onBuild func(*formulapkg.Context, *formulapkg.Project, *formulapkg.BuildResult)
	func() {
		fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
		f, err := LoadFS(fsys, "targetsurface_llar.gox")
		if err != nil {
			t.Fatalf("LoadFS failed: %v", err)
		}
		for range 16 {
			_ = Clone(f)
		}

		llarixgo.LockInterp()
		_, loaded, _ = ixgo.IcallStat()
		llarixgo.UnlockInterp()
		onBuild = f.OnBuild
	}()
	if loaded <= before {
		t.Fatalf("allocated icall slots after load = %d, want more than %d", loaded, before)
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	llarixgo.LockInterp()
	_, withHook, _ := ixgo.IcallStat()
	llarixgo.UnlockInterp()
	if withHook <= before {
		t.Fatal("formula program was released while OnBuild remained reachable")
	}
	onBuild(&formulapkg.Context{}, &formulapkg.Project{}, &formulapkg.BuildResult{})
	onBuild = nil

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)

		llarixgo.LockInterp()
		_, allocated, _ := ixgo.IcallStat()
		llarixgo.UnlockInterp()
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
		f.OnBuild(&formulapkg.Context{}, &formulapkg.Project{}, &formulapkg.BuildResult{})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "hello\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
