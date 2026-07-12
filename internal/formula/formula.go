// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/goplus/ixgo"
	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/llar/formula"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	_ "github.com/goplus/llar/internal/ixgo"
)

// Formula represents a loaded LLAR formula file with its metadata and callbacks.
// It contains module information and build/dependency handling functions.
type Formula struct {
	structElem reflect.Value

	// NOTE(MeteorsLiu): these signatures MUST match with
	// 	the method declaration of ModuleF in formula/classfile.go
	ModPath   string
	FromVer   string
	Matrix    formula.Matrix
	OnRequire func(proj *formula.Project, deps *formula.ModuleDeps)
	OnBuild   func(ctx *formula.Context, proj *formula.Project, out *formula.BuildResult)
	OnTest    func(ctx *formula.Context, proj *formula.Project, out *formula.TestResult)
	Filter    func() bool
}

type ssaState struct {
	blocks    map[*ssa.BasicBlock][]ssa.Instruction
	referrers map[ssa.Value][]ssa.Instruction
}

func saveSSAState(prog *ssa.Program) ssaState {
	state := ssaState{
		blocks:    make(map[*ssa.BasicBlock][]ssa.Instruction),
		referrers: make(map[ssa.Value][]ssa.Instruction),
	}
	for fn := range ssautil.AllFunctions(prog) {
		for _, block := range fn.Blocks {
			state.blocks[block] = slices.Clone(block.Instrs)
			for _, instr := range block.Instrs {
				if value, ok := instr.(ssa.Value); ok {
					state.saveReferrers(value)
				}
				for _, operand := range instr.Operands(nil) {
					if operand != nil && *operand != nil {
						state.saveReferrers(*operand)
					}
				}
			}
		}
	}
	return state
}

func (s ssaState) saveReferrers(value ssa.Value) {
	if _, ok := s.referrers[value]; ok {
		return
	}
	if refs := value.Referrers(); refs != nil {
		s.referrers[value] = slices.Clone(*refs)
	}
}

func (s ssaState) restore() {
	for block, instrs := range s.blocks {
		block.Instrs = instrs
	}
	for value, refs := range s.referrers {
		*value.Referrers() = refs
	}
}

// loadFS is the internal implementation for loading a formula from a filesystem.
// It builds and interprets the formula file using the xgo classfile mechanism,
// then extracts the struct fields containing module metadata and callbacks.
//
// The xgo classfile mechanism transforms a DSL file (e.g., hello_llar.gox) into
// generated Go code. For example, given this DSL:
//
//	id "DaveGamble/cJSON"
//	fromVer "v1.0.0"
//	onRequire (proj, deps) => { echo "hello" }
//	onBuild (ctx, proj, out) => { echo "hello" }
//
// xgobuild.BuildFile generates:
//
//	package main
//	import "github.com/goplus/llar/formula"
//
//	type hello struct {
//	    formula.ModuleF
//	}
//
//	func (this *hello) MainEntry() {
//	    this.Id("DaveGamble/cJSON")
//	    this.FromVer("v1.0.0")
//	    this.OnRequire(func(proj *formula.Project, deps *formula.ModuleDeps) { ... })
//	    this.OnBuild(func(ctx *formula.Context, proj *formula.Project, out *formula.BuildResult) { ... })
//	}
//
//	func (this *hello) Main() {
//	    formula.Gopt_ModuleF_Main(this)
//	}
//
//	func main() {
//	    new(hello).Main()
//	}
//
// The struct name is derived from the filename prefix before "_" (e.g., "hello" from "hello_llar.gox").
// Calling Main() triggers Gopt_ModuleF_Main which invokes MainEntry() to populate the struct fields.
func loadFS(fs fs.ReadFileFS, path string) (*Formula, error) {
	// Create a new ixgo interpreter context
	ctx := ixgo.NewContext(0)

	// Read the raw DSL content from the .gox file
	content, err := fs.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Transform the DSL (.gox) into generated Go source code
	// This uses the registered classfile extensions (_llar.gox -> ModuleF)
	source, err := xgobuild.BuildFile(ctx, path, content)
	if err != nil {
		return nil, err
	}

	// Load the generated source as a Go package (treated as main.go)
	pkgs, err := ctx.LoadFile("main.go", source)
	if err != nil {
		return nil, err
	}
	state := saveSSAState(pkgs.Prog)
	tr := newTracker()
	tracked := tr.track(ctx, pkgs)

	// Extract struct name from filename: "hello_llar.gox" -> "hello"
	// The classfile mechanism generates a struct with this name
	structName, _, ok := strings.Cut(filepath.Base(path), "_")
	if !ok {
		return nil, fmt.Errorf("failed to load formula: file name is not valid: %s", path)
	}

	var matrix formula.Matrix
	if tracked {
		matrix, err = probeFormula(ctx, pkgs, structName, tr)
		if err != nil {
			return nil, err
		}
	}

	// NewInterp translates SSA eagerly. The probe interpreter retains the
	// instrumented instructions, while the production interpreter below sees
	// the original SSA restored from this snapshot.
	state.restore()

	interp, err := ctx.NewInterp(pkgs)
	if err != nil {
		return nil, err
	}

	// Run package-level init functions
	if err = interp.RunInit(); err != nil {
		return nil, err
	}

	// Get the generated struct type from the interpreter
	typ, ok := interp.GetType(structName)
	if !ok {
		return nil, fmt.Errorf("failed to load formula: struct name not found: %s", structName)
	}

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
	return &Formula{
		structElem: class,
		ModPath:    valueOf(class, "modPath").(string),
		FromVer:    valueOf(class, "modFromVer").(string),
		Matrix:     matrix,
		OnBuild:    valueOf(class, "fOnBuild").(func(*formula.Context, *formula.Project, *formula.BuildResult)),
		OnTest:     valueOf(class, "fOnTest").(func(*formula.Context, *formula.Project, *formula.TestResult)),
		OnRequire:  valueOf(class, "fOnRequire").(func(*formula.Project, *formula.ModuleDeps)),
		Filter:     valueOf(class, "fFilter").(func() bool),
	}, nil
}

// LoadFS loads a formula from a filesystem interface.
// This allows loading formulas from remote repositories or mock filesystems.
// The path should be relative to the filesystem root.
func LoadFS(fsys fs.ReadFileFS, path string) (*Formula, error) {
	return loadFS(fsys, path)
}

// SetStdout sets the stdout writer for the formula's gsh.App.
// This is used to control build output verbosity.
func (f *Formula) SetStdout(w io.Writer) {
	if f.structElem.IsValid() {
		setValue(f.structElem, "fout", w)
	}
}

// SetStderr sets the stderr writer for the formula's gsh.App.
func (f *Formula) SetStderr(w io.Writer) {
	if f.structElem.IsValid() {
		setValue(f.structElem, "ferr", w)
	}
}
