// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package modules

import (
	"fmt"
	"go/token"
	"io/fs"
	"strings"
	"sync"

	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/llar/internal/formula"
	"github.com/goplus/llar/mod/module"
	"github.com/goplus/llar/x/gnu"
	"github.com/goplus/xgo/ast"
	"github.com/goplus/xgo/parser"
)

const defaultFormulaSuffix = "_llar.gox"
const defaultComparatorSuffix = "_cmp.gox"

// formulaModule represents a single module's formula collection.
// It provides access to the module's version comparator and formulas.
// The fsys should be rooted at the module's directory within the formula repository.
type formulaModule struct {
	fsys       fs.FS
	modPath    string
	comparator func() (func(v1, v2 module.Version) int, error)

	mu       sync.Mutex
	formulas map[string]*formula.Formula
}

// newFormulaModule creates a new formulaModule for the given module.
// The fsys should be rooted at the module's directory (already positioned by the caller).
// The modPath is used for constructing module.Version in version comparisons.
func newFormulaModule(fsys fs.FS, modPath string) *formulaModule {
	m := &formulaModule{
		fsys:     fsys,
		modPath:  modPath,
		formulas: make(map[string]*formula.Formula),
	}
	m.comparator = sync.OnceValues(func() (func(v1, v2 module.Version) int, error) {
		return loadOrDefaultComparator(m.fsys)
	})
	return m
}

// loadOrDefaultComparator searches for a _cmp.gox comparator file in fsys.
// If found, it loads and returns the custom comparator.
// If no comparator file exists, it falls back to GNU version comparison.
// If a comparator file exists but fails to load, the error is returned.
func loadOrDefaultComparator(fsys fs.FS) (func(v1, v2 module.Version) int, error) {
	matches, _ := fs.Glob(fsys, "*"+defaultComparatorSuffix)
	if len(matches) == 0 {
		return func(v1, v2 module.Version) int {
			return gnu.Compare(v1.Version, v2.Version)
		}, nil
	}
	return loadComparatorFS(fsys.(fs.ReadFileFS), matches[0])
}

// at returns the formula for the specified version.
// It finds the appropriate formula based on version matching and caches the result.
func (m *formulaModule) at(version string) (*formula.Formula, error) {
	cmp, err := m.comparator()
	if err != nil {
		return nil, err
	}

	mod := module.Version{Path: m.modPath, Version: version}
	// TODO(MeteorsLiu): Optimize the max fromVer searching with the trie tree
	fromVer, formulaPath, err := m.findMaxFromVer(mod, cmp)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if f, ok := m.formulas[fromVer]; ok {
		return formula.Clone(f), nil
	}
	f, err := formula.LoadFS(m.fsys.(fs.ReadFileFS), formulaPath)

	if err != nil {
		return nil, err
	}
	m.formulas[fromVer] = f
	return formula.Clone(f), nil
}

// findMaxFromVer finds the formula file with the highest fromVer that is <= the target version.
func (m *formulaModule) findMaxFromVer(mod module.Version, compare func(v1, v2 module.Version) int) (maxFromVer, formulaPath string, err error) {
	err = fs.WalkDir(m.fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, defaultFormulaSuffix) {
			return nil
		}

		fromVer, err := fromVerOf(m.fsys.(fs.ReadFileFS), path)
		if err != nil {
			return err
		}
		fromVerMod := module.Version{Path: mod.Path, Version: fromVer}

		if compare(fromVerMod, mod) > 0 {
			return nil
		}
		if maxFromVer == "" || compare(fromVerMod, module.Version{Path: mod.Path, Version: maxFromVer}) > 0 {
			maxFromVer = fromVer
			formulaPath = path
		}
		return nil
	})

	if err != nil {
		return "", "", err
	}

	if formulaPath == "" {
		return "", "", fmt.Errorf("no formula found for %s", mod.Path)
	}

	return maxFromVer, formulaPath, nil
}

// fromVerOf extracts the fromVer value from a formula file by parsing its AST.
func fromVerOf(fsys fs.ReadFileFS, formulaPath string) (string, error) {
	content, err := fsys.ReadFile(formulaPath)
	if err != nil {
		return "", err
	}

	fset := token.NewFileSet()
	astFile, err := parser.ParseEntry(fset, formulaPath, content, parser.Config{
		ClassKind: xgobuild.ClassKind,
	})
	if err != nil {
		return "", err
	}
	return fromVerFrom(astFile)
}

// fromVerFrom extracts the fromVer value from a formula AST.
func fromVerFrom(formulaAST *ast.File) (string, error) {
	var fromVer string
	var err error

	ast.Inspect(formulaAST, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := c.Fun.(*ast.Ident); ok && fn.Name == "fromVer" {
			fromVer, err = parseCallArg(c, fn.Name)
			return false
		}
		return true
	})

	if fromVer == "" {
		return "", fmt.Errorf("failed to parse fromVer from AST: cannot match any fromVer expr")
	}

	return fromVer, err
}

// parseCallArg extracts the first string argument from a function call expression.
func parseCallArg(c *ast.CallExpr, fnName string) (string, error) {
	if len(c.Args) == 0 {
		return "", fmt.Errorf("failed to parse %s from AST: no argument", fnName)
	}
	var argResult string
	switch arg := c.Args[0].(type) {
	case *ast.BasicLit:
		argResult = strings.Trim(strings.Trim(arg.Value, `"`), "`")
		if argResult == "" {
			return "", fmt.Errorf("failed to parse %s from AST: empty argument", fnName)
		}
	default:
		return "", fmt.Errorf("failed to parse %s from AST: argument is not a string literal", fnName)
	}
	return argResult, nil
}
