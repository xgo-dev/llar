// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixgo

//go:generate qexp -outdir pkg github.com/goplus/llar/formula
//go:generate qexp -outdir pkg github.com/goplus/llar/mod/versions
//go:generate qexp -outdir pkg golang.org/x/mod/semver
//go:generate qexp -outdir pkg github.com/goplus/llar/x/gnu
//go:generate qexp -outdir pkg github.com/goplus/llar/x/autotools
//go:generate qexp -outdir pkg github.com/goplus/llar/x/cmake
//go:generate qexp -outdir pkg github.com/goplus/llar/mod/module
//go:generate qexp -outdir pkg github.com/qiniu/x/gsh
import (
	_ "github.com/goplus/ixgo/pkg"
	_ "github.com/goplus/ixgo/pkg/net/rpc"
	_ "github.com/goplus/ixgo/pkg/net/rpc/jsonrpc"

	_ "github.com/goplus/llar/internal/ixgo/pkg/github.com/goplus/llar/formula"
	_ "github.com/goplus/llar/internal/ixgo/pkg/github.com/goplus/llar/mod/module"
	_ "github.com/goplus/llar/internal/ixgo/pkg/github.com/goplus/llar/mod/versions"
	_ "github.com/goplus/llar/internal/ixgo/pkg/github.com/goplus/llar/x/autotools"
	_ "github.com/goplus/llar/internal/ixgo/pkg/github.com/goplus/llar/x/cmake"
	_ "github.com/goplus/llar/internal/ixgo/pkg/github.com/goplus/llar/x/gnu"

	_ "github.com/goplus/llar/internal/ixgo/pkg/github.com/qiniu/x/gsh"
	_ "github.com/goplus/llar/internal/ixgo/pkg/golang.org/x/mod/semver"
)
