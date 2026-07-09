// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"maps"
	"slices"
	"sort"

	"github.com/goplus/llar/mod/module"
	"github.com/qiniu/x/gsh"
)

const GopPackage = true

// -----------------------------------------------------------------------------

// ModuleF represents the build formula of a module.
type ModuleF struct {
	gsh.App

	fOnRequire func(proj *Project, deps *ModuleDeps)
	fOnBuild   func(ctx *Context, proj *Project, out *BuildResult)
	fOnTest    func(ctx *Context, proj *Project, out *TestResult)
	fFilter    func() bool

	modPath    string
	modFromVer string
	target     Matrix
}

type Matrix struct {
	Require        map[string][]string
	Options        map[string][]string
	DefaultOptions map[string][]string
}

type matrixTarget struct {
	m Matrix
}

// Combinations returns all cartesian product combinations of the matrix.
// Keys are sorted alphabetically, and combinations are built layer by layer.
// Require fields are joined with "-", then combined with options using "|".
func (m *Matrix) Combinations() []string {
	// Helper function to compute cartesian product for a map
	cartesian := func(kvs map[string][]string) []string {
		if len(kvs) == 0 {
			return nil
		}

		// Sort keys alphabetically
		keys := make([]string, 0, len(kvs))
		for k := range kvs {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Start with first key's values
		result := make([]string, len(kvs[keys[0]]))
		copy(result, kvs[keys[0]])

		// Combine with subsequent layers using "-"
		for i := 1; i < len(keys); i++ {
			values := kvs[keys[i]]
			newResult := make([]string, 0, len(result)*len(values))
			for _, prev := range result {
				for _, v := range values {
					newResult = append(newResult, prev+"-"+v)
				}
			}
			result = newResult
		}
		return result
	}

	// Compute require combinations
	requireCombos := cartesian(m.Require)

	// Compute options combinations
	optionsCombos := cartesian(m.Options)

	// If no require, just return options
	if len(requireCombos) == 0 {
		return optionsCombos
	}

	// If no options, just return require
	if len(optionsCombos) == 0 {
		return requireCombos
	}

	// Combine require with options using "|"
	result := make([]string, 0, len(requireCombos)*len(optionsCombos))
	for _, req := range requireCombos {
		for _, opt := range optionsCombos {
			result = append(result, req+"|"+opt)
		}
	}

	return result
}

// CombinationCount returns the total number of cartesian product combinations.
func (m *Matrix) CombinationCount() int {
	countPart := func(kvs map[string][]string) int {
		if len(kvs) == 0 {
			return 0
		}
		count := 1
		for _, v := range kvs {
			count *= len(v)
		}
		return count
	}

	requireCount := countPart(m.Require)
	optionsCount := countPart(m.Options)

	if requireCount == 0 {
		return optionsCount
	}
	if optionsCount == 0 {
		return requireCount
	}
	return requireCount * optionsCount
}

func (p *ModuleF) app() *gsh.App {
	return &p.App
}

func (p *ModuleF) Target() matrixTarget {
	return matrixTarget{m: Matrix{
		Require: maps.Clone(p.target.Require),
		Options: maps.Clone(p.target.Options),
	}}
}

// Require backs the XGo auto-property `target.require`.
// In formula DSL, target.require["xxx"] maps to Target().Require()["xxx"].
func (m matrixTarget) Require() map[string][]string {
	return m.m.Require
}

// Options backs the XGo auto-property `target.options`.
// In formula DSL, target.options["xxx"] maps to Target().Options()["xxx"].
func (m matrixTarget) Options() map[string][]string {
	return m.m.Options
}

// Defaults sets the formula's default option selections and initializes
// the active options exposed through target.options.
func (p *ModuleF) Defaults(options map[string]string) {
	if p.target.DefaultOptions == nil {
		p.target.DefaultOptions = make(map[string][]string, len(options))
	}
	if p.target.Options == nil {
		p.target.Options = make(map[string][]string, len(options))
	}
	for key, value := range options {
		values := []string{value}
		p.target.DefaultOptions[key] = values
		p.target.Options[key] = values
	}
}

// Filter records a predicate used to reject unsupported matrix selections.
func (p *ModuleF) Filter(f func() bool) {
	p.fFilter = f
}

// Id sets the module path that this formula serves.
// path should be in the form of "owner/repo".
func (p *ModuleF) Id(path string) {
	p.modPath = path
}

// FromVer sets the minimum version of the module that this formula serves.
func (p *ModuleF) FromVer(ver string) {
	p.modFromVer = ver
}

// -----------------------------------------------------------------------------

// ModuleDeps represents the dependencies of a module.
type ModuleDeps struct {
	deps []module.Version
}

// Deps returns the collected module dependencies.
func (p *ModuleDeps) Deps() []module.Version {
	return slices.Clone(p.deps)
}

// Require declares that the module being built depends on the specified
// module (by its path and version).
func (p *ModuleDeps) Require(path, ver string) {
	p.deps = append(p.deps, module.Version{Path: path, Version: ver})
}

// OnRequire event is used to retrieve all direct dependencies of a
// project (module). proj is the project being built, deps is used to
// declare dependencies.
func (p *ModuleF) OnRequire(f func(proj *Project, deps *ModuleDeps)) {
	p.fOnRequire = f
}

// -----------------------------------------------------------------------------

// BuildResult represents the result of building a project.
type BuildResult struct {
	errs     []error
	metadata string // build output metadata, for C/C++ it's the result of pkg-config.
}

// AddErr records a build error.
func (b *BuildResult) AddErr(err error) {
	b.errs = append(b.errs, err)
}

// Errs returns all errors collected during build.
func (b *BuildResult) Errs() []error {
	return b.errs
}

// Metadata returns the build output metadata.
func (b *BuildResult) Metadata() string {
	return b.metadata
}

// SetMetadata sets the build output metadata.
func (b *BuildResult) SetMetadata(metadata string) {
	b.metadata = metadata
}

// OnBuild event is used to instruct the Formula to compile a project.
func (p *ModuleF) OnBuild(f func(ctx *Context, proj *Project, out *BuildResult)) {
	p.fOnBuild = f
}

// TestResult represents the outcome of a formula's onTest hook.
// Unlike BuildResult it has no metadata field: a test's job is to verify
// the build, not to emit additional pkg-config-style flags. Future
// extensions (pass/fail counts, skip markers, captured logs) should be
// added here without polluting BuildResult.
type TestResult struct {
	errs []error
}

// AddErr records a test failure.
func (t *TestResult) AddErr(err error) {
	t.errs = append(t.errs, err)
}

// Errs returns all errors collected during the test hook.
func (t *TestResult) Errs() []error {
	return t.errs
}

// OnTest event is used to run post-build verification for a project.
// It fires after OnBuild has completed successfully, reusing the same build
// context so tests can locate built artifacts via ctx.OutputDir.
func (p *ModuleF) OnTest(f func(ctx *Context, proj *Project, out *TestResult)) {
	p.fOnTest = f
}

// -----------------------------------------------------------------------------

// Gopt_ModuleF_Main is main entry of this classfile.
func Gopt_ModuleF_Main(this interface {
	app() *gsh.App
	MainEntry()
}) {
	this.MainEntry()
	gsh.InitApp(this.app())
}
