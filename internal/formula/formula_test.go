// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"io/fs"
	"os"
	"reflect"
	"testing"

	formulapkg "github.com/goplus/llar/formula"
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
	wantRequire := map[string][]string{"os": nil}
	if !reflect.DeepEqual(f.Matrix.Require, wantRequire) {
		t.Fatalf("Matrix.Require = %#v, want %#v", f.Matrix.Require, wantRequire)
	}
	wantOptions := map[string][]string{"debug": nil, "zlib": nil}
	if !reflect.DeepEqual(f.Matrix.Options, wantOptions) {
		t.Fatalf("Matrix.Options = %#v, want %#v", f.Matrix.Options, wantOptions)
	}

	var deps formulapkg.ModuleDeps
	f.OnRequire(&formulapkg.Project{}, &deps)
	gotDeps := deps.Deps()
	if len(gotDeps) != 1 || gotDeps[0].Path != "madler/zlib" || gotDeps[0].Version != "v1.3.1" {
		t.Fatalf("deps = %+v, want [madler/zlib@v1.3.1]", gotDeps)
	}
	f.OnBuild(&formulapkg.Context{}, &formulapkg.Project{}, &formulapkg.BuildResult{})
}

func TestLoadFSProbesMatrixKeys(t *testing.T) {
	fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
	f, err := LoadFS(fsys, "matrix_llar.gox")
	if err != nil {
		t.Fatalf("LoadFS failed: %v", err)
	}

	wantRequire := map[string][]string{"os": nil}
	if !reflect.DeepEqual(f.Matrix.Require, wantRequire) {
		t.Fatalf("Matrix.Require = %#v, want %#v", f.Matrix.Require, wantRequire)
	}
	wantOptions := map[string][]string{"debug": nil, "ssl": nil}
	if !reflect.DeepEqual(f.Matrix.Options, wantOptions) {
		t.Fatalf("Matrix.Options = %#v, want %#v", f.Matrix.Options, wantOptions)
	}
}

func TestFormula_SetStdout(t *testing.T) {
	formula, err := loadFS(os.DirFS("testdata/formula").(fs.ReadFileFS), "hello_llar.gox")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	var buf []byte
	formula.SetStdout(&mockWriter{buf: &buf})
	formula.SetStdout(nil)
}

func TestFormula_SetStderr(t *testing.T) {
	formula, err := loadFS(os.DirFS("testdata/formula").(fs.ReadFileFS), "hello_llar.gox")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	var buf []byte
	formula.SetStderr(&mockWriter{buf: &buf})
	formula.SetStderr(nil)
}

type mockWriter struct {
	buf *[]byte
}

func (m *mockWriter) Write(p []byte) (n int, err error) {
	*m.buf = append(*m.buf, p...)
	return len(p), nil
}
