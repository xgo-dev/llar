// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixgo

import (
	"os"
	"reflect"
	"testing"

	ixgoapi "github.com/goplus/ixgo"
	"github.com/goplus/llar/internal/execbroker"
	"github.com/qiniu/x/gsh"
)

func TestBrokerOSPackageMergePreservesExports(t *testing.T) {
	pkg, ok := ixgoapi.LookupPackage("os")
	if !ok {
		t.Fatal("os package is not registered")
	}
	for _, name := range []string{"ReadFile", "WriteFile", "MkdirAll"} {
		if _, ok := pkg.Funcs[name]; !ok {
			t.Fatalf("os package lost existing function %q", name)
		}
	}
	fn, ok := pkg.Funcs["Getenv"]
	if !ok || fn.Pointer() != reflect.ValueOf(execbroker.Getenv).Pointer() {
		t.Fatal("os.Getenv was not replaced by the broker implementation")
	}
}

func TestBrokerSyscallPackageMergePreservesExports(t *testing.T) {
	pkg, ok := ixgoapi.LookupPackage("syscall")
	if !ok {
		t.Fatal("syscall package is not registered")
	}
	if _, ok := pkg.Funcs["Read"]; !ok {
		t.Fatal("syscall package lost existing function Read")
	}
	for name, want := range map[string]uintptr{
		"Getenv":   reflect.ValueOf(execbroker.LookupEnv).Pointer(),
		"Setenv":   reflect.ValueOf(execbroker.Setenv).Pointer(),
		"Unsetenv": reflect.ValueOf(execbroker.Unsetenv).Pointer(),
	} {
		fn, ok := pkg.Funcs[name]
		if !ok || fn.Pointer() != want {
			t.Fatalf("syscall.%s was not replaced by the broker implementation", name)
		}
	}
}

func TestBrokerOSEnvironmentUsesScope(t *testing.T) {
	key := "IXGO_BROKER_ENV_TEST"
	if err := os.Setenv(key, "process"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })

	err := execbroker.Do(execbroker.Scope{}, func() error {
		if err := execbroker.Setenv(key, "scoped"); err != nil {
			return err
		}
		if got := gsh.Sys.Getenv(key); got != "scoped" {
			t.Fatalf("gsh.Sys.Getenv = %q, want scoped", got)
		}
		if got := gsh.Sys.ExpandEnv("$" + key); got != "scoped" {
			t.Fatalf("gsh.Sys.ExpandEnv = %q, want scoped", got)
		}
		if got := envValue(gsh.Sys.Environ(), key); got != "scoped" {
			t.Fatalf("gsh.Sys.Environ = %q, want scoped", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "process" {
		t.Fatalf("process environment = %q, want process", got)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if len(env[i]) >= len(prefix) && env[i][:len(prefix)] == prefix {
			return env[i][len(prefix):]
		}
	}
	return ""
}
