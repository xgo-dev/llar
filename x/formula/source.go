// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/xgo/ast"
	"github.com/goplus/xgo/parser"
)

type formulaMetadata struct {
	modPath   string
	fromVer   string
	onRequire bool
	onBuild   bool
	onTest    bool
	filter    bool
}

// metadataOf uses the AST extraction previously owned by modules to read the
// declarations needed before executing a formula.
func metadataOf(path string, content []byte) (formulaMetadata, error) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseEntry(fset, path, content, parser.Config{
		ClassKind: xgobuild.ClassKind,
	})
	if err != nil {
		return formulaMetadata{}, err
	}
	return metadataFrom(astFile)
}

func metadataFrom(formulaAST *ast.File) (formulaMetadata, error) {
	var metadata formulaMetadata
	var err error

	ast.Inspect(formulaAST, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := c.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch fn.Name {
		case "id":
			metadata.modPath, err = parseCallArg(c, fn.Name)
		case "fromVer":
			metadata.fromVer, err = parseCallArg(c, fn.Name)
		case "onRequire":
			metadata.onRequire = true
		case "onBuild":
			metadata.onBuild = true
		case "onTest":
			metadata.onTest = true
		case "filter":
			metadata.filter = true
		default:
			return true
		}
		return false
	})

	if err != nil {
		return metadata, err
	}
	if metadata.modPath == "" {
		return metadata, fmt.Errorf("failed to parse id from AST: cannot match any id expr")
	}
	if metadata.fromVer == "" {
		return metadata, fmt.Errorf("failed to parse fromVer from AST: cannot match any fromVer expr")
	}
	return metadata, nil
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
