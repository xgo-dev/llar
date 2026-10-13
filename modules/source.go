// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package modules

import (
	"fmt"
	"io/fs"
	"slices"
	"sort"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	classfile "github.com/goplus/llar/runtime/formula"
	"github.com/goplus/llar/runtime/x/gnu"
)

const defaultComparatorSuffix = "_cmp.gox"

// formulaModule represents a single module's formula collection.
// It provides access to the module's version comparator and formulas.
// The fsys should be rooted at the module's directory within the formula repository.
type formulaModule struct {
	fsys       fs.FS
	modPath    string
	matrix     classfile.Matrix
	repo       vcs.Repo
	comparator func() (func(v1, v2 module.Version) int, error)
}

// newFormulaModule creates a new formulaModule for the given module.
// The fsys should be rooted at the module's directory (already positioned by the caller).
// The modPath is used for constructing module.Version in version comparisons.
func newFormulaModule(fsys fs.FS, modPath string, matrix classfile.Matrix, repo vcs.Repo) *formulaModule {
	m := &formulaModule{
		fsys:    fsys,
		modPath: modPath,
		matrix:  matrix,
		repo:    repo,
	}
	m.comparator = func() (func(v1, v2 module.Version) int, error) {
		return loadOrDefaultComparator(m.fsys, m.repo)
	}
	return m
}

// loadOrDefaultComparator searches for a _cmp.gox comparator file in fsys.
// If found, it loads the custom tag comparator.
// If no comparator file exists, it falls back to GNU tag comparison.
// Git commit comparison is added to either comparator by default.
// If a comparator file exists but fails to load, the error is returned.
func loadOrDefaultComparator(fsys fs.FS, repo vcs.Repo) (func(v1, v2 module.Version) int, error) {
	matches, _ := fs.Glob(fsys, "*"+defaultComparatorSuffix)
	var compareTag func(v1, v2 module.Version) int
	if len(matches) == 0 {
		compareTag = func(v1, v2 module.Version) int {
			return gnu.Compare(v1.Version, v2.Version)
		}
	} else {
		var err error
		compareTag, err = loadComparatorFS(fsys.(fs.ReadFileFS), matches[0])
		if err != nil {
			return nil, err
		}
	}
	return func(v1, v2 module.Version) int {
		return repo.Refs().CompareFunc(v1.Version, v2.Version, func(a, b string) int {
			return compareTag(
				module.Version{Path: v1.Path, Version: a},
				module.Version{Path: v2.Path, Version: b},
			)
		})
	}, nil
}

// at returns the formula for the specified version.
// It reads metadata from the source and configures the selected lazy instance.
func (m *formulaModule) at(version string) (*formula.Formula, error) {
	cmp, err := m.comparator()
	if err != nil {
		return nil, err
	}

	mod := module.Version{Path: m.modPath, Version: version}
	f, err := m.findMaxFromVer(mod, cmp)
	if err != nil {
		return nil, err
	}

	f.SetMatrix(m.matrix)
	return f, nil
}

// findMaxFromVer selects the first formula whose fromVer is <= the target
// from the descending metadata list, without executing any formula hooks.
func (m *formulaModule) findMaxFromVer(mod module.Version, compare func(v1, v2 module.Version) int) (*formula.Formula, error) {
	formulas, err := formula.LoadFS(m.fsys, ".")
	if err != nil {
		return nil, err
	}
	// Highest fromVer comes first. Preserve directory order for equivalent
	// versions so selection keeps the previous first-match behavior.
	slices.SortStableFunc(formulas, func(a, b *formula.Formula) int {
		return compare(
			module.Version{Path: mod.Path, Version: b.FromVer},
			module.Version{Path: mod.Path, Version: a.FromVer},
		)
	})
	i := sort.Search(len(formulas), func(i int) bool {
		return compare(module.Version{Path: mod.Path, Version: formulas[i].FromVer}, mod) <= 0
	})
	if i == len(formulas) {
		return nil, fmt.Errorf("no formula found for %s", mod.Path)
	}
	return formulas[i], nil
}
