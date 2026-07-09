// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"errors"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/goplus/llar/mod/module"
)

func TestModuleDeps_Require(t *testing.T) {
	deps := &ModuleDeps{}

	deps.Require("owner/repo", "1.2.3")
	deps.Require("foo/bar", "0.9.0")

	want := []module.Version{
		{Path: "owner/repo", Version: "1.2.3"},
		{Path: "foo/bar", Version: "0.9.0"},
	}
	if got := deps.Deps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModuleDeps.Deps() = %#v, want %#v", got, want)
	}
}

func TestBuildResult_ErrsAndMetadata(t *testing.T) {
	result := &BuildResult{}
	errA := errors.New("first")
	errB := errors.New("second")

	result.AddErr(errA)
	result.AddErr(errB)

	if got := result.Errs(); len(got) != 2 || got[0] != errA || got[1] != errB {
		t.Fatalf("BuildResult.Errs() = %#v, want [%v %v]", got, errA, errB)
	}

	if result.Metadata() != "" {
		t.Fatalf("BuildResult.Metadata() = %q, want empty string", result.Metadata())
	}
	result.SetMetadata("-lssl")
	if result.Metadata() != "-lssl" {
		t.Fatalf("BuildResult.Metadata() = %q, want %q", result.Metadata(), "-lssl")
	}
}

func TestProject_ReadFile(t *testing.T) {
	proj := &Project{
		SourceFS: fstest.MapFS{
			"hello.txt": {Data: []byte("hello")},
		},
	}

	t.Run("existing file", func(t *testing.T) {
		got, err := proj.ReadFile("hello.txt")
		if err != nil {
			t.Fatalf("Project.ReadFile() error = %v", err)
		}
		if string(got) != "hello" {
			t.Fatalf("Project.ReadFile() = %q, want %q", string(got), "hello")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := proj.ReadFile("missing.txt"); err == nil {
			t.Fatalf("Project.ReadFile() error = nil, want error")
		}
	})
}

func TestContext_DoesNotExposeCurrentMatrix(t *testing.T) {
	if _, ok := reflect.TypeOf((*Context)(nil)).MethodByName("CurrentMatrix"); ok {
		t.Fatal("Context should not expose CurrentMatrix; formula DSL should use target instead")
	}
}

// TestNewContext covers the public constructor and asserts that each
// argument is threaded into the expected internal field.
func TestNewContext(t *testing.T) {
	getOutputDir := func(_ string, _ module.Version) (string, error) { return "", nil }

	ctx := NewContext("/src", "/install", "amd64-linux", getOutputDir)

	if ctx.SourceDir != "/src" {
		t.Errorf("SourceDir = %q, want %q", ctx.SourceDir, "/src")
	}
	if ctx.installDir != "/install" {
		t.Errorf("installDir = %q, want %q", ctx.installDir, "/install")
	}
	if ctx.matrixStr != "amd64-linux" {
		t.Errorf("matrixStr = %q, want %q", ctx.matrixStr, "amd64-linux")
	}
	if ctx.getOutputDir == nil {
		t.Error("getOutputDir was not stored on Context")
	}
}

// TestContext_OutputDir covers both overloads: OutputDir__0 returns the
// module's own install dir; OutputDir__1 routes through the getOutputDir
// lookup with the active matrix so dep install dirs can be resolved.
func TestContext_OutputDir(t *testing.T) {
	dep := module.Version{Path: "owner/dep", Version: "1.0.0"}

	var gotMatrix string
	var gotMod module.Version
	getOutputDir := func(matrixStr string, m module.Version) (string, error) {
		gotMatrix = matrixStr
		gotMod = m
		return "/out/" + m.Path, nil
	}

	ctx := NewContext("/src", "/install", "amd64-linux", getOutputDir)

	t.Run("OutputDir__0 returns own installDir", func(t *testing.T) {
		got, err := ctx.OutputDir__0()
		if err != nil {
			t.Fatalf("OutputDir__0() error = %v", err)
		}
		if got != "/install" {
			t.Errorf("OutputDir__0() = %q, want %q", got, "/install")
		}
	})

	t.Run("OutputDir__1 dispatches through getOutputDir", func(t *testing.T) {
		got, err := ctx.OutputDir__1(dep)
		if err != nil {
			t.Fatalf("OutputDir__1() error = %v", err)
		}
		if got != "/out/owner/dep" {
			t.Errorf("OutputDir__1() = %q, want %q", got, "/out/owner/dep")
		}
		if gotMatrix != "amd64-linux" {
			t.Errorf("getOutputDir matrix = %q, want %q", gotMatrix, "amd64-linux")
		}
		if gotMod != dep {
			t.Errorf("getOutputDir mod = %+v, want %+v", gotMod, dep)
		}
	})
}

func TestContext_BuildResult(t *testing.T) {
	ctx := &Context{}
	mod := module.Version{Path: "owner/repo", Version: "1.0.0"}

	if _, ok := ctx.BuildResult(mod); ok {
		t.Fatalf("Context.BuildResult() ok = true, want false")
	}

	result := BuildResult{}
	result.SetMetadata("metadata")

	ctx.AddBuildResult(mod, result)
	got, ok := ctx.BuildResult(mod)
	if !ok {
		t.Fatalf("Context.BuildResult() ok = false, want true")
	}
	if got.Metadata() != "metadata" {
		t.Fatalf("Context.BuildResult() metadata = %q, want %q", got.Metadata(), "metadata")
	}
}
