// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"reflect"
	"testing"
)

// testFormula embeds ModuleF and provides a MainEntry that exercises every
// setter on the classfile DSL surface. This mirrors the struct layout the
// xgo interpreter generates for a real .gox formula.
type testFormula struct {
	ModuleF
	mainCalled bool
}

func (f *testFormula) MainEntry() {
	f.mainCalled = true
	f.Id("foo/bar")
	f.FromVer("1.0.0")
	f.Defaults(map[string]string{"debug": "OFF"})
	f.Filter(func() bool { return true })
	f.OnRequire(func(*Project, *ModuleDeps) {})
	f.OnBuild(func(*Context, *Project, *BuildResult) {})
	f.OnTest(func(*Context, *Project, *TestResult) {})
}

// TestGopt_ModuleF_Main exercises the classfile entry point together with
// every DSL setter on ModuleF. Going through Gopt_ModuleF_Main also covers
// the unexported app() helper it dispatches through.
func TestGopt_ModuleF_Main(t *testing.T) {
	f := &testFormula{}
	Gopt_ModuleF_Main(f)

	if !f.mainCalled {
		t.Fatal("MainEntry was not invoked by Gopt_ModuleF_Main")
	}
	if f.modPath != "foo/bar" {
		t.Errorf("Id: modPath = %q, want %q", f.modPath, "foo/bar")
	}
	if f.modFromVer != "1.0.0" {
		t.Errorf("FromVer: modFromVer = %q, want %q", f.modFromVer, "1.0.0")
	}
	wantDefaults := map[string][]string{"debug": {"OFF"}}
	if !reflect.DeepEqual(f.target.Defaults, wantDefaults) {
		t.Errorf("Defaults: defaults = %+v, want %+v", f.target.Defaults, wantDefaults)
	}
	if f.fFilter == nil {
		t.Fatal("Filter: fFilter is nil")
	}
	if !f.fFilter() {
		t.Fatal("Filter: fFilter() = false, want true")
	}
	if f.fOnRequire == nil {
		t.Error("OnRequire: fOnRequire is nil")
	}
	if f.fOnBuild == nil {
		t.Error("OnBuild: fOnBuild is nil")
	}
	if f.fOnTest == nil {
		t.Error("OnTest: fOnTest is nil")
	}
}

func TestModuleF_TargetReturnsMatrixCopy(t *testing.T) {
	f := &ModuleF{}
	f.target = Matrix{
		Require: map[string][]string{
			"os": {"linux"},
		},
		Options: map[string][]string{
			"debug": {"off"},
		},
	}

	target := f.Target()
	target.Require()["os"] = []string{"darwin"}
	target.Require()["arch"] = []string{"arm64"}
	target.Options()["debug"] = []string{"on"}

	if got := f.target.Require["os"][0]; got != "linux" {
		t.Fatalf("Target mutated internal require map: got %q", got)
	}
	if _, ok := f.target.Require["arch"]; ok {
		t.Fatal("Target mutated internal require map")
	}
	if got := f.target.Options["debug"][0]; got != "off" {
		t.Fatalf("Target mutated internal options map: got %q", got)
	}
}

func TestModuleF_DefaultsSetsTargetOptions(t *testing.T) {
	f := &ModuleF{}
	f.Defaults(map[string]string{"zlib": "ON"})

	want := []string{"ON"}
	if got := f.target.Defaults["zlib"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("Defaults[zlib] = %+v, want %+v", got, want)
	}
	if got := f.Target().Options()["zlib"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("target.options[zlib] = %+v, want %+v", got, want)
	}
}

// TestModuleF_app verifies that app() returns the pointer to the embedded
// gsh.App, which is the contract Gopt_ModuleF_Main relies on.
func TestModuleF_app(t *testing.T) {
	m := &ModuleF{}
	if got := m.app(); got != &m.App {
		t.Errorf("app() = %p, want %p (embedded App)", got, &m.App)
	}
}
