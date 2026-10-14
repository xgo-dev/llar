// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixgo

import (
	"os/exec"
	"reflect"

	ixgoapi "github.com/goplus/ixgo"
	"github.com/goplus/llar/internal/execbroker"
	"github.com/qiniu/x/gsh"
)

type brokerOS struct {
	gsh.OS
}

func (brokerOS) Run(cmd *exec.Cmd) error {
	return execbroker.Run(cmd)
}

func (brokerOS) Environ() []string {
	return execbroker.Environ()
}

func (brokerOS) ExpandEnv(s string) string {
	return execbroker.ExpandEnv(s)
}

func (brokerOS) Getenv(key string) string {
	return execbroker.Getenv(key)
}

func init() {
	gsh.Sys = brokerOS{OS: gsh.Sys}
	ixgoapi.RegisterPackage(&ixgoapi.Package{
		Name: "os",
		Path: "os",
		Funcs: map[string]reflect.Value{
			"Clearenv":  reflect.ValueOf(execbroker.Clearenv),
			"Environ":   reflect.ValueOf(execbroker.Environ),
			"ExpandEnv": reflect.ValueOf(execbroker.ExpandEnv),
			"Getenv":    reflect.ValueOf(execbroker.Getenv),
			"LookupEnv": reflect.ValueOf(execbroker.LookupEnv),
			"Setenv":    reflect.ValueOf(execbroker.Setenv),
			"Unsetenv":  reflect.ValueOf(execbroker.Unsetenv),
		},
	})
	ixgoapi.RegisterPackage(&ixgoapi.Package{
		Name: "syscall",
		Path: "syscall",
		Funcs: map[string]reflect.Value{
			"Clearenv": reflect.ValueOf(execbroker.Clearenv),
			"Environ":  reflect.ValueOf(execbroker.Environ),
			"Getenv":   reflect.ValueOf(execbroker.LookupEnv),
			"Setenv":   reflect.ValueOf(execbroker.Setenv),
			"Unsetenv": reflect.ValueOf(execbroker.Unsetenv),
		},
	})
}
