// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixgo

import (
	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/mod/modfile"
)

func init() {
	xgobuild.RegisterProject(&modfile.Project{
		Ext:   "_cmp.gox",
		Class: "CmpApp",
		PkgPaths: []string{
			"github.com/goplus/llar/x/runtime/cmp",
		},
		Import: []*modfile.Import{
			{
				Name: "semver",
				Path: "golang.org/x/mod/semver",
			},
			{
				Name: "gnu",
				Path: "github.com/goplus/llar/x/runtime/x/gnu",
			},
		},
	})
	xgobuild.RegisterProject(&modfile.Project{
		Ext:   "_llar.gox",
		Class: "ModuleF",
		PkgPaths: []string{
			"github.com/goplus/llar/x/runtime/formula",
		},
		Import: []*modfile.Import{
			{
				Name: "autotools",
				Path: "github.com/goplus/llar/x/runtime/x/autotools",
			},
			{
				Name: "cmake",
				Path: "github.com/goplus/llar/x/runtime/x/cmake",
			},
			{
				Name: "pkgconfig",
				Path: "github.com/goplus/llar/x/runtime/x/pkgconfig",
			},
		},
	})
}
