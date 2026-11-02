// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmp

import "github.com/goplus/llar/mod/module"

const (
	XGoPackage = true
)

type CmpApp struct {
	fCompareVer func(a, b module.Version) int
}

// The provided function fn will be used to compare version strings
// when resolving dependencies for those versions which are not in Debian-style.
func (app *CmpApp) CompareVer(fn func(a, b module.Version) int) {
	app.fCompareVer = fn
}

// XGot_CmpApp_Main is main entry of this classfile.
func XGot_CmpApp_Main(this interface{ MainEntry() }) {
	this.MainEntry()
}
