// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package install

// Require identifies the operating system, architecture, and C library selected
// for an installation. For example, linux/arm64 with libc set to musl selects
// the corresponding native artifacts.
type Require struct {
	OS   string
	Arch string
	Libc string
}

// Config describes the requirements and option selections for one installation.
// Options retains the LLAR matrix's option semantics; it does not contain
// additional required dimensions.
type Config struct {
	Require Require
	Options map[string]string
}
