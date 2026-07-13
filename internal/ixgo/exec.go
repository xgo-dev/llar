// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixgo

import (
	"os/exec"

	"github.com/goplus/llar/internal/execbroker"
	"github.com/qiniu/x/gsh"
)

type brokerOS struct {
	gsh.OS
}

func (brokerOS) Run(cmd *exec.Cmd) error {
	return execbroker.Run(cmd)
}

func init() {
	gsh.Sys = brokerOS{OS: gsh.Sys}
}
