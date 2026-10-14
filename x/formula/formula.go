// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/goplus/ixgo"
	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/llar/internal/execbroker"
	_ "github.com/goplus/llar/x/runtime"
	"github.com/goplus/llar/x/runtime/formula"
)

const defaultFormulaSuffix = "_llar.gox"

// formulaProgram is the native GC owner for one interpreted formula program.
// The interpreter is the cleanup argument, so it cannot make this owner reachable.
type formulaProgram struct {
	typ reflect.Type
}

// Formula represents a loaded LLAR formula file with its metadata and callbacks.
// It contains module information and build/dependency handling functions.
type Formula struct {
	structElem reflect.Value
	program    *formulaProgram
	load       func() *Formula
	matrix     formula.Matrix

	// FS is rooted at the directory containing this formula and its resources.
	FS fs.FS

	// NOTE(MeteorsLiu): these signatures MUST match with
	// 	the method declaration of ModuleF in formula/classfile.go
	ModPath   string
	FromVer   string
	OnRequire func(proj *formula.Project, deps *formula.ModuleDeps)
	OnBuild   func(ctx *formula.Context)
	OnTest    func(ctx *formula.Context)
	Filter    func() bool
}

// loadFS parses formula metadata immediately and loads the interpreted program
// once, when a declared hook is first called. All hooks use the same instance.
func loadFS(fsys fs.ReadFileFS, filename string) (*Formula, error) {
	content, err := fsys.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	metadata, err := metadataOf(filename, content)
	if err != nil {
		return nil, err
	}
	dirFS, err := fs.Sub(fsys, path.Dir(filename))
	if err != nil {
		return nil, err
	}
	f := &Formula{FS: dirFS, ModPath: metadata.modPath, FromVer: metadata.fromVer}
	var once sync.Once
	var result *Formula
	var panicValue any
	fn := func() {
		defer func() {
			// Once marks a panicking initialization as done. Keep its cause
			// so subsequent hook calls report the same loading failure.
			if result == nil {
				panicValue = recover()
				panic(panicValue)
			}
		}()
		// Formula types remain cached after loading, so later interpreters must not
		// reset the dynamic method slots used by earlier types.
		ctx := ixgo.NewContext(ixgo.SupportMultipleInterp)
		ctx.RegisterExternal("os/exec.Command", execbroker.Command)
		ctx.RegisterExternal("os/exec.CommandContext", execbroker.CommandContext)
		ctx.RegisterExternal("fmt.Println", execbroker.Println)

		// Transform the DSL (.gox) into generated Go source code
		// This uses the registered classfile extensions (_llar.gox -> ModuleF)
		source, err := xgobuild.BuildFile(ctx, filename, content)
		if err != nil {
			panic(err)
		}

		// Load the generated source as a Go package (treated as main.go)
		pkgs, err := ctx.LoadFile("main.go", source)
		if err != nil {
			panic(err)
		}

		// Create a new interpreter for the loaded package
		interp, err := ctx.NewInterp(pkgs)
		if err != nil {
			panic(err)
		}
		program := &formulaProgram{}
		runtime.AddCleanup(program, (*ixgo.Interp).UnsafeRelease, interp)

		// Run package-level init functions
		if err = interp.RunInit(); err != nil {
			panic(err)
		}

		// Extract struct name from filename: "cpu_features_llar.gox" -> "cpu_features"
		// The classfile mechanism generates a struct with this name
		structName := strings.TrimSuffix(filepath.Base(filename), "_llar.gox")

		// Get the generated struct type from the interpreter
		typ, ok := interp.GetType(structName)
		if !ok {
			panic(fmt.Errorf("failed to load formula: struct name not found: %s", structName))
		}
		program.typ = typ

		// Create a new instance of the generated struct (e.g., &hello{})
		val := reflect.New(typ)
		class := val.Elem()

		// Call Main() which triggers: formula.Gopt_ModuleF_Main(this)
		// This in turn calls MainEntry() to execute the DSL code and populate fields:
		// - modPath: set by this.Id(...)
		// - modFromVer: set by this.FromVer(...)
		// - fOnRequire: set by this.OnRequire(...)
		// - fOnBuild: set by this.OnBuild(...)
		val.Interface().(interface{ Main() }).Main()

		// Extract the populated fields from the struct and return the Formula
		loaded := &Formula{
			structElem: class,
			program:    program,
			FS:         f.FS,
			ModPath:    valueOf(class, "modPath").(string),
			FromVer:    valueOf(class, "modFromVer").(string),
			OnBuild:    valueOf(class, "fOnBuild").(func(*formula.Context)),
			OnTest:     valueOf(class, "fOnTest").(func(*formula.Context)),
			OnRequire:  valueOf(class, "fOnRequire").(func(*formula.Project, *formula.ModuleDeps)),
			Filter:     valueOf(class, "fFilter").(func() bool),
		}
		loaded.keepProgramAlive()
		f.structElem = class
		f.program = program
		f.SetMatrix(f.matrix)
		result = loaded
	}
	f.load = func() *Formula {
		once.Do(fn)
		if result == nil {
			panic(panicValue)
		}
		return result
	}
	if metadata.onRequire {
		f.OnRequire = func(proj *formula.Project, deps *formula.ModuleDeps) {
			f.load().OnRequire(proj, deps)
		}
	}
	if metadata.onBuild {
		f.OnBuild = func(ctx *formula.Context) {
			f.load().OnBuild(ctx)
		}
	}
	if metadata.onTest {
		f.OnTest = func(ctx *formula.Context) {
			f.load().OnTest(ctx)
		}
	}
	if metadata.filter {
		f.Filter = func() bool {
			return f.load().Filter()
		}
	}
	return f, nil
}

func loadDir(fsys fs.FS, path string) ([]*Formula, error) {
	var formulas []*Formula

	err := fs.WalkDir(fsys, path, func(path string, d fs.DirEntry, err error) error {
		// is it a formula?
		if !strings.HasSuffix(path, defaultFormulaSuffix) {
			return err
		}
		f, err := loadFS(fsys.(fs.ReadFileFS), path)
		if err == nil {
			formulas = append(formulas, f)
		}
		return err
	})

	return formulas, err
}

// Clone defers creating an independent class until its first hook call.
// It shares the source formula's program, but reruns Main so hooks capture
// the new instance rather than the source instance.
func Clone(f *Formula) *Formula {
	cloned := &Formula{
		FS:      f.FS,
		ModPath: f.ModPath,
		FromVer: f.FromVer,
	}
	var once sync.Once
	var result *Formula
	var panicValue any
	fn := func() {
		defer func() {
			if result == nil {
				panicValue = recover()
				panic(panicValue)
			}
		}()
		program := f.load().program
		val := reflect.New(program.typ)
		class := val.Elem()
		val.Interface().(interface{ Main() }).Main()

		loaded := &Formula{
			structElem: class,
			program:    program,
			FS:         cloned.FS,
			ModPath:    valueOf(class, "modPath").(string),
			FromVer:    valueOf(class, "modFromVer").(string),
			OnBuild:    valueOf(class, "fOnBuild").(func(*formula.Context)),
			OnTest:     valueOf(class, "fOnTest").(func(*formula.Context)),
			OnRequire:  valueOf(class, "fOnRequire").(func(*formula.Project, *formula.ModuleDeps)),
			Filter:     valueOf(class, "fFilter").(func() bool),
		}
		loaded.keepProgramAlive()
		cloned.structElem = class
		cloned.program = program
		cloned.SetMatrix(cloned.matrix)
		result = loaded
	}
	cloned.load = func() *Formula {
		once.Do(fn)
		if result == nil {
			panic(panicValue)
		}
		return result
	}
	if f.OnRequire != nil {
		cloned.OnRequire = func(proj *formula.Project, deps *formula.ModuleDeps) {
			cloned.load().OnRequire(proj, deps)
		}
	}
	if f.OnBuild != nil {
		cloned.OnBuild = func(ctx *formula.Context) {
			cloned.load().OnBuild(ctx)
		}
	}
	if f.OnTest != nil {
		cloned.OnTest = func(ctx *formula.Context) {
			cloned.load().OnTest(ctx)
		}
	}
	if f.Filter != nil {
		cloned.Filter = func() bool {
			return cloned.load().Filter()
		}
	}
	return cloned
}

// keepProgramAlive makes extracted hooks retain the cleanup owner. Without the
// wrapper, the owner could become unreachable while the interpreted hook is
// still callable:
//
//	fn := f.OnBuild
//	f = nil
//	runtime.GC()
//	fn(...) // the interpreter may already have been released
//
// The original hook retains the interpreter, but not formulaProgram, which is
// the AddCleanup target. KeepAlive also prevents cleanup during the hook call.
func (f *Formula) keepProgramAlive() {
	program := f.program
	if fn := f.OnBuild; fn != nil {
		f.OnBuild = func(ctx *formula.Context) {
			fn(ctx)
			runtime.KeepAlive(program)
		}
	}
	if fn := f.OnTest; fn != nil {
		f.OnTest = func(ctx *formula.Context) {
			fn(ctx)
			runtime.KeepAlive(program)
		}
	}
	if fn := f.OnRequire; fn != nil {
		f.OnRequire = func(proj *formula.Project, deps *formula.ModuleDeps) {
			fn(proj, deps)
			runtime.KeepAlive(program)
		}
	}
	if fn := f.Filter; fn != nil {
		f.Filter = func() bool {
			ok := fn()
			runtime.KeepAlive(program)
			return ok
		}
	}
}

// LoadFS reads formula metadata from a _llar.gox file or recursively from a
// directory. The path is relative to fsys; use "." to scan its root directory.
//
// Metadata is parsed immediately. Each formula is compiled and initialized only
// on its first hook call, including Filter. Deferred loading errors panic and
// are handled by the caller's hook recovery boundary.
func LoadFS(fsys fs.FS, path string) ([]*Formula, error) {
	return loadDir(fsys, path)
}
