// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixgo

//go:generate qexp -outdir pkg github.com/goplus/llar/build/runtime/formula
//go:generate qexp -outdir pkg github.com/goplus/llar/build/runtime/cmp
//go:generate qexp -outdir pkg github.com/goplus/llar/mod/versions
//go:generate qexp -outdir pkg golang.org/x/mod/semver
//go:generate qexp -outdir pkg github.com/goplus/llar/build/runtime/x/gnu
//go:generate qexp -outdir pkg github.com/goplus/llar/build/runtime/x/autotools
//go:generate qexp -outdir pkg github.com/goplus/llar/build/runtime/x/cmake
//go:generate qexp -outdir pkg github.com/goplus/llar/build/runtime/x/pkgconfig
//go:generate qexp -outdir pkg github.com/goplus/llar/mod/module
//go:generate qexp -outdir pkg github.com/qiniu/x/gsh
import (
	_ "github.com/goplus/ixgo/pkg"
	_ "github.com/goplus/ixgo/pkg/net/rpc"
	_ "github.com/goplus/ixgo/pkg/net/rpc/jsonrpc"

	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/build/runtime/cmp"
	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/build/runtime/formula"
	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/build/runtime/x/autotools"
	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/build/runtime/x/cmake"
	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/build/runtime/x/gnu"
	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/build/runtime/x/pkgconfig"
	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/mod/module"
	_ "github.com/goplus/llar/build/runtime/pkg/github.com/goplus/llar/mod/versions"

	_ "github.com/goplus/llar/build/runtime/pkg/github.com/qiniu/x/gsh"
	_ "github.com/goplus/llar/build/runtime/pkg/golang.org/x/mod/semver"
)
