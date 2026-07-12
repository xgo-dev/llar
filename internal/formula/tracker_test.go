// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"io/fs"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/goplus/ixgo"
	"github.com/goplus/ixgo/xgobuild"
)

func TestLoadFSMatrixTracker(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantRequire map[string][]string
		wantOptions map[string][]string
	}{
		{
			name: "data flow",
			path: "trackcases_llar.gox",
			wantRequire: map[string][]string{
				"direct-require": nil,
				"helper-require": nil,
				"same":           nil,
			},
			wantOptions: map[string][]string{
				"alias":         nil,
				"closure":       nil,
				"comma-ok":      nil,
				"direct-option": nil,
				"dynamic":       nil,
				"helper-option": nil,
				"interface":     nil,
				"named":         nil,
				"pointer":       nil,
				"returned":      nil,
				"same":          nil,
				"struct":        nil,
				"type-alias":    nil,
			},
		},
		{
			name: "panic isolation",
			path: "trackpanic_llar.gox",
			wantOptions: map[string][]string{
				"after-panic":  nil,
				"before-panic": nil,
			},
		},
		{
			name: "filter is not probed",
			path: "trackfilter_llar.gox",
		},
		{
			name: "no matrix access",
			path: "hello_llar.gox",
		},
	}

	fsys := os.DirFS("testdata/formula").(fs.ReadFileFS)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := LoadFS(fsys, tt.path)
			if err != nil {
				t.Fatalf("LoadFS(%q) failed: %v", tt.path, err)
			}
			if !reflect.DeepEqual(f.Matrix.Require, tt.wantRequire) {
				t.Fatalf("Matrix.Require = %#v, want %#v", f.Matrix.Require, tt.wantRequire)
			}
			if !reflect.DeepEqual(f.Matrix.Options, tt.wantOptions) {
				t.Fatalf("Matrix.Options = %#v, want %#v", f.Matrix.Options, tt.wantOptions)
			}
		})
	}
}

func TestSSAStateRestore(t *testing.T) {
	ctx := ixgo.NewContext(0)
	content, err := os.ReadFile("testdata/formula/matrix_llar.gox")
	if err != nil {
		t.Fatal(err)
	}
	source, err := xgobuild.BuildFile(ctx, "matrix_llar.gox", content)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := ctx.LoadFile("main.go", source)
	if err != nil {
		t.Fatal(err)
	}

	state := saveSSAState(pkg.Prog)
	if !newTracker().track(ctx, pkg) {
		t.Fatal("track returned false")
	}

	changed := false
	for block, instrs := range state.blocks {
		if !slices.Equal(block.Instrs, instrs) {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("tracker did not change SSA instructions")
	}

	state.restore()
	for block, instrs := range state.blocks {
		if !slices.Equal(block.Instrs, instrs) {
			t.Fatalf("block %d instructions were not restored", block.Index)
		}
	}
	for value, refs := range state.referrers {
		if !slices.Equal(*value.Referrers(), refs) {
			t.Fatalf("referrers for %s were not restored", value.Name())
		}
	}
}
