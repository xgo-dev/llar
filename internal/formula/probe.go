// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"io/fs"

	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/mod/module"
)

type probeFS struct{}

func (probeFS) Open(string) (fs.File, error) {
	return nil, fs.ErrNotExist
}

func (probeFS) ReadFile(string) ([]byte, error) {
	return nil, fs.ErrNotExist
}

func probeFormula(f *Formula) {
	originalTarget := valueOf(f.structElem, "target").(classfile.Matrix)
	// Probe with empty maps because discovery does not require a valid matrix.
	// A formula may read several independent options while configuring a build:
	//
	//	if has(target.options["zlib"], "ON") { ... }
	//	shared := has(target.options["shared"], "ON")
	//	debug := has(target.options["debug"], "ON")
	//
	// Each lookup returns an empty value, but the SSA tracker still records zlib,
	// shared, and debug before the formula eventually succeeds or fails. The maps
	// must be non-nil because their runtime identities also let the tracker follow
	// aliases and helper arguments.
	fakeTarget := classfile.Matrix{
		Require: make(map[string][]string),
		Options: make(map[string][]string),
	}
	setValue(f.structElem, "target", fakeTarget)
	defer setValue(f.structElem, "target", originalTarget)

	project := &classfile.Project{SourceFS: probeFS{}}
	if f.OnRequire != nil {
		var deps classfile.ModuleDeps
		safeProbeCall(func() {
			f.OnRequire(project, &deps)
		})
	}
	if f.OnBuild != nil {
		ctx := classfile.NewContext("", "", "", func(string, module.Version) (string, error) {
			return "", nil
		})
		var out classfile.BuildResult
		safeProbeCall(func() {
			f.OnBuild(ctx, project, &out)
		})
	}
}

func safeProbeCall(call func()) {
	defer func() {
		_ = recover()
	}()
	call()
}
