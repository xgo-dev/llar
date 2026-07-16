// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package execbroker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/petermattis/goid"
)

// Request describes a command before it is turned into an exec.Cmd.
type Request struct {
	Name   string
	Args   []string
	Env    []string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Middleware may rewrite a command before execution.
type Middleware func(Request) Request

// Scope supplies process-independent defaults for commands created while fn
// runs. Custom fields already set on a command take precedence.
type Scope struct {
	Env        []string
	Dir        string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Middleware Middleware
}

var (
	scopeMu sync.RWMutex
	scopes  = make(map[int64]Scope)
)

// Do runs fn with the supplied command scope.
func Do(scope Scope, fn func() error) error {
	id := goid.Get()
	scope.Env = clone(scope.Env)
	scopeMu.Lock()
	previous, existed := scopes[id]
	scopes[id] = scope
	scopeMu.Unlock()
	defer func() {
		scopeMu.Lock()
		if existed {
			scopes[id] = previous
		} else {
			delete(scopes, id)
		}
		scopeMu.Unlock()
	}()
	return fn()
}

// Println writes to the stdout configured for the active scope.
func Println(a ...any) (int, error) {
	w := io.Writer(os.Stdout)
	scopeMu.RLock()
	if scope, ok := scopes[goid.Get()]; ok && scope.Stdout != nil {
		w = scope.Stdout
	}
	scopeMu.RUnlock()
	return fmt.Fprintln(w, a...)
}

// Command is the brokered equivalent of exec.Command.
func Command(name string, args ...string) *exec.Cmd {
	req := rewrite(Request{Name: name, Args: clone(args)})
	cmd := exec.Command(req.Name, req.Args...)
	apply(cmd, req)
	return cmd
}

// CommandContext is the brokered equivalent of exec.CommandContext.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	req := rewrite(Request{Name: name, Args: clone(args)})
	cmd := exec.CommandContext(ctx, req.Name, req.Args...)
	apply(cmd, req)
	return cmd
}

// Run applies the active scope to an existing command and runs it. This is
// used by runtimes such as gsh that construct exec.Cmd internally.
func Run(cmd *exec.Cmd) error {
	name := cmd.Path
	args := []string(nil)
	if len(cmd.Args) > 0 {
		name = cmd.Args[0]
		args = cmd.Args[1:]
	}
	req := rewrite(Request{
		Name:   name,
		Args:   clone(args),
		Env:    clone(cmd.Env),
		Dir:    cmd.Dir,
		Stdin:  cmd.Stdin,
		Stdout: cmd.Stdout,
		Stderr: cmd.Stderr,
	})
	resolved := exec.Command(req.Name, req.Args...)
	cmd.Path = resolved.Path
	cmd.Args = resolved.Args
	cmd.Err = resolved.Err
	apply(cmd, req)
	return cmd.Run()
}

func rewrite(req Request) Request {
	req.Args = clone(req.Args)
	req.Env = clone(req.Env)

	scopeMu.RLock()
	scope, ok := scopes[goid.Get()]
	if ok {
		if req.Env == nil {
			req.Env = clone(scope.Env)
		}
		if req.Dir == "" {
			req.Dir = scope.Dir
		}
		// gsh initializes commands with the process streams. Treat those as
		// defaults while preserving custom writers used by features like Capout.
		if scope.Stdin != nil && (req.Stdin == nil || req.Stdin == os.Stdin) {
			req.Stdin = scope.Stdin
		}
		if scope.Stdout != nil && (req.Stdout == nil || req.Stdout == os.Stdout) {
			req.Stdout = scope.Stdout
		}
		if scope.Stderr != nil && (req.Stderr == nil || req.Stderr == os.Stderr) {
			req.Stderr = scope.Stderr
		}
		if scope.Middleware != nil {
			req = scope.Middleware(req)
		}
	}
	scopeMu.RUnlock()

	req.Args = clone(req.Args)
	req.Env = clone(req.Env)
	return req
}

func apply(cmd *exec.Cmd, req Request) {
	cmd.Env = clone(req.Env)
	cmd.Dir = req.Dir
	cmd.Stdin = req.Stdin
	cmd.Stdout = req.Stdout
	cmd.Stderr = req.Stderr
}

func clone(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}
