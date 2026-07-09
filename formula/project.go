// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"io/fs"

	"github.com/goplus/llar/mod/module"
)

// -----------------------------------------------------------------------------

// Project represents a project (module) being built.
type Project struct {
	Deps     []module.Version
	SourceFS fs.ReadFileFS
}

// ReadFile reads the content of a file in the project.
func (p *Project) ReadFile(path string) ([]byte, error) {
	return p.SourceFS.ReadFile(path)
}

// Context represents the build context.
type Context struct {
	SourceDir string

	buildResults map[module.Version]BuildResult

	// filled by build
	installDir   string
	matrixStr    string
	getOutputDir func(matrixStr string, mod module.Version) (string, error)
}

// NewContext creates a Context with build-internal fields.
func NewContext(sourceDir, installDir, matrixStr string, getOutputDir func(string, module.Version) (string, error)) *Context {
	return &Context{
		SourceDir:    sourceDir,
		installDir:   installDir,
		matrixStr:    matrixStr,
		getOutputDir: getOutputDir,
	}
}

// OutputDir__0 returns the current module's output (install) directory.
// In DSL: ctx.outputDir()
func (c *Context) OutputDir__0() (string, error) {
	return c.installDir, nil
}

// OutputDir__1 returns the output (install) directory for the given dependency.
// In DSL: ctx.outputDir(dep)
func (c *Context) OutputDir__1(mod module.Version) (string, error) {
	return c.getOutputDir(c.matrixStr, mod)
}

// BuildResult returns the stored build result for the module, if any.
func (c *Context) BuildResult(mod module.Version) (BuildResult, bool) {
	r, ok := c.buildResults[mod]
	return r, ok
}

// AddBuildResult stores the build result for the given module.
func (c *Context) AddBuildResult(mod module.Version, result BuildResult) {
	if c.buildResults == nil {
		c.buildResults = make(map[module.Version]BuildResult)
	}
	c.buildResults[mod] = result
}

// -----------------------------------------------------------------------------
