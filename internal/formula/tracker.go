// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"go/token"
	"go/types"
	"reflect"
	"sync"
	"unsafe"

	"github.com/goplus/ixgo"
	classfile "github.com/goplus/llar/formula"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const (
	optionsTrackerFunc = "__internalOptionsTracker"
	requireTrackerFunc = "__internalRequireTracker"
	lookupTrackerFunc  = "__internalLookupTracker"
	formulaPackagePath = "github.com/goplus/llar/formula"
)

type matrixKind uint8

const (
	unknownMatrix matrixKind = iota
	requireMatrix
	optionsMatrix
)

var matrixMapType = types.NewMap(types.Typ[types.String], types.NewSlice(types.Typ[types.String]))

type trackedMap struct {
	kind matrixKind
	// Keep the map alive so its runtime identity cannot be reused during probe.
	values map[string][]string
}

// tracker discovers matrix reads by instrumenting the completed Go SSA program.
// A target accessor registers the returned map, and each map lookup reports its
// map and key before the original lookup executes. For example:
//
//	options := target.Options() // register options by map identity
//	lookup(options, "shared")  // report the lookup inside lookup
//
// Map identity survives assignment and argument passing, so lookups in helpers
// remain associated with Options or Require. Lookups on maps that were never
// returned by a target accessor are ignored at runtime.
type tracker struct {
	mu sync.Mutex

	active  bool
	maps    map[uintptr]trackedMap
	require map[string]struct{}
	options map[string]struct{}
}

func newTracker() *tracker {
	return &tracker{
		active:  true,
		maps:    make(map[uintptr]trackedMap),
		require: make(map[string]struct{}),
		options: make(map[string]struct{}),
	}
}

// track instruments the completed SSA program before ixgo translates it.
func (t *tracker) track(ctx *ixgo.Context, pkg *ssa.Package) bool {
	functions := ssautil.AllFunctions(pkg.Prog)

	valuesParam := types.NewVar(token.NoPos, nil, "values", matrixMapType)
	registerSignature := types.NewSignatureType(
		nil, nil, nil,
		types.NewTuple(valuesParam),
		types.NewTuple(),
		false,
	)
	keyParam := types.NewVar(token.NoPos, nil, "key", types.Typ[types.String])
	lookupSignature := types.NewSignatureType(
		nil, nil, nil,
		types.NewTuple(valuesParam, keyParam),
		types.NewTuple(),
		false,
	)
	optionsTracker := pkg.Prog.NewFunction(optionsTrackerFunc, registerSignature, "llar matrix tracker")
	requireTracker := pkg.Prog.NewFunction(requireTrackerFunc, registerSignature, "llar matrix tracker")
	lookupTracker := pkg.Prog.NewFunction(lookupTrackerFunc, lookupSignature, "llar matrix tracker")

	ctx.RegisterExternal(optionsTracker.String(), func(values map[string][]string) {
		t.trackMap(optionsMatrix, values)
	})
	ctx.RegisterExternal(requireTracker.String(), func(values map[string][]string) {
		t.trackMap(requireMatrix, values)
	})
	ctx.RegisterExternal(lookupTracker.String(), t.trackLookup)

	tracked := false
	for fn := range functions {
		if fn.Blocks == nil {
			continue
		}
		for _, block := range fn.Blocks {
			instrs := make([]ssa.Instruction, 0, len(block.Instrs))
			for _, instr := range block.Instrs {
				// Observe every matching lookup. trackLookup filters unrelated maps
				// using the identities registered by Options or Require.
				if lookup, ok := instr.(*ssa.Lookup); ok && isMatrixMap(lookup.X.Type()) {
					instrs = append(instrs, trackerCall(block, lookupTracker, lookup.X, lookup.Index))
				}
				instrs = append(instrs, instr)
				if call, ok := instr.(*ssa.Call); ok {
					// Imported wrappers may call the same target methods. Only
					// accessors called by the formula package start matrix tracking.
					switch matrixTargetCall(call) {
					case optionsMatrix:
						if fn.Pkg == pkg {
							tracked = true
							instrs = append(instrs, trackerCall(block, optionsTracker, call))
						}
					case requireMatrix:
						if fn.Pkg == pkg {
							tracked = true
							instrs = append(instrs, trackerCall(block, requireTracker, call))
						}
					}
				}
			}
			block.Instrs = instrs
		}
	}
	return tracked
}

func (t *tracker) matrix() classfile.Matrix {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.active = false
	t.maps = nil
	return classfile.Matrix{
		Require: cloneKeys(t.require),
		Options: cloneKeys(t.options),
	}
}

func (t *tracker) trackMap(kind matrixKind, values map[string][]string) {
	if values == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return
	}
	t.maps[reflect.ValueOf(values).Pointer()] = trackedMap{kind: kind, values: values}
}

func (t *tracker) trackLookup(values map[string][]string, key string) {
	if values == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return
	}

	tracked, ok := t.maps[reflect.ValueOf(values).Pointer()]
	if !ok {
		return
	}
	if tracked.kind == requireMatrix {
		t.require[key] = struct{}{}
	} else {
		t.options[key] = struct{}{}
	}
}

func matrixTargetCall(call *ssa.Call) matrixKind {
	callee := call.Call.StaticCallee()
	if callee == nil || callee.Signature.Recv() == nil {
		return unknownMatrix
	}

	typ := types.Unalias(callee.Signature.Recv().Type())
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(ptr.Elem())
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return unknownMatrix
	}
	obj := named.Obj()
	if obj.Pkg() == nil || obj.Name() != "matrixTarget" || obj.Pkg().Path() != formulaPackagePath {
		return unknownMatrix
	}

	switch callee.Name() {
	case "Options":
		return optionsMatrix
	case "Require":
		return requireMatrix
	default:
		return unknownMatrix
	}
}

func isMatrixMap(typ types.Type) bool {
	return types.Identical(types.Unalias(typ).Underlying(), matrixMapType)
}

func trackerCall(block *ssa.BasicBlock, fn *ssa.Function, args ...ssa.Value) *ssa.Call {
	call := &ssa.Call{Call: ssa.CallCommon{Value: fn, Args: args}}
	// x/tools does not expose constructors for post-build instructions.
	// Populate the metadata ixgo reads before appending the call to the block.
	callValue := reflect.ValueOf(call).Elem()
	register := callValue.FieldByName("register")
	setUnexported(register.FieldByName("typ"), reflect.ValueOf(fn.Signature.Results()))
	instruction := register.FieldByName("anInstruction")
	setUnexported(instruction.FieldByName("block"), reflect.ValueOf(block))

	for _, arg := range args {
		if refs := arg.Referrers(); refs != nil {
			*refs = append(*refs, call)
		}
	}
	return call
}

func setUnexported(dst, src reflect.Value) {
	reflect.NewAt(dst.Type(), unsafe.Pointer(dst.UnsafeAddr())).Elem().Set(src)
}

func cloneKeys(keys map[string]struct{}) map[string][]string {
	if len(keys) == 0 {
		return nil
	}
	matrix := make(map[string][]string, len(keys))
	for key := range keys {
		matrix[key] = nil
	}
	return matrix
}
