// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package execbroker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestCommandScope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	wantEnv := append(os.Environ(), "EXECBROKER_TEST=value")
	err := Do(Scope{
		Dir:    t.TempDir(),
		Env:    wantEnv,
		Stdin:  bytes.NewBufferString("input"),
		Stdout: &stdout,
		Stderr: &stderr,
	}, func() error {
		cmd := Command("command", "one", "two")
		if cmd.Args[0] != "command" || !reflect.DeepEqual(cmd.Args[1:], []string{"one", "two"}) {
			t.Fatalf("Args = %q", cmd.Args)
		}
		if cmd.Dir == "" || !reflect.DeepEqual(cmd.Env, wantEnv) {
			t.Fatalf("scope not applied: Dir=%q Env=%q", cmd.Dir, cmd.Env)
		}
		if cmd.Stdin == nil || cmd.Stdout != &stdout || cmd.Stderr != &stderr {
			t.Fatalf("command I/O does not match scope")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command("command")
	if cmd.Dir != "" || cmd.Env != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatalf("scope leaked after Do: %+v", cmd)
	}
}

func TestCommandMiddleware(t *testing.T) {
	err := Do(Scope{Middleware: func(req Request) Request {
		req.Name = "replacement"
		req.Args = append([]string{"prefix"}, req.Args...)
		req.Env = []string{"KEY=value"}
		req.Dir = "/work"
		return req
	}}, func() error {
		cmd := Command("original", "arg")
		if got, want := cmd.Args, []string{"replacement", "prefix", "arg"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Args = %q, want %q", got, want)
		}
		if !reflect.DeepEqual(cmd.Env, []string{"KEY=value"}) || cmd.Dir != "/work" {
			t.Fatalf("middleware result not applied: %+v", cmd)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommandContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := CommandContext(ctx, os.Args[0], "-test.run=TestExecBrokerHelperProcess")
	if err := cmd.Run(); err == nil {
		t.Fatal("CommandContext succeeded with canceled context")
	}
}

func TestRunPreservesExplicitFields(t *testing.T) {
	var explicit bytes.Buffer
	err := Do(Scope{
		Dir:    t.TempDir(),
		Env:    append(os.Environ(), "EXECBROKER_HELPER=scope"),
		Stdout: bytes.NewBuffer(nil),
	}, func() error {
		cmd := exec.Command(os.Args[0], "-test.run=TestExecBrokerHelperProcess")
		cmd.Env = append(os.Environ(), "EXECBROKER_HELPER=explicit")
		cmd.Stdout = &explicit
		if err := Run(cmd); err != nil {
			return err
		}
		if got := explicit.String(); got != "explicit" {
			t.Fatalf("output = %q, want explicit", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDoScopesAreGoroutineLocal(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir()}
	ready := make(chan struct{}, len(dirs))
	start := make(chan struct{})
	results := make(chan string, len(dirs))
	for _, dir := range dirs {
		go func() {
			_ = Do(Scope{Dir: dir}, func() error {
				ready <- struct{}{}
				<-start
				results <- Command("command").Dir
				return nil
			})
		}()
	}
	for range dirs {
		<-ready
	}
	if cmd := Command("command"); cmd.Dir != "" {
		t.Fatalf("scope leaked to another goroutine: Dir=%q", cmd.Dir)
	}
	close(start)

	got := map[string]bool{<-results: true, <-results: true}
	for _, dir := range dirs {
		if !got[dir] {
			t.Fatalf("missing command scope %q, got %v", dir, got)
		}
	}
}

func TestDoRestoresNestedScope(t *testing.T) {
	outer := t.TempDir()
	inner := t.TempDir()
	if err := Do(Scope{Dir: outer}, func() error {
		if got := Command("command").Dir; got != outer {
			t.Fatalf("outer Dir = %q, want %q", got, outer)
		}
		if err := Do(Scope{Dir: inner}, func() error {
			if got := Command("command").Dir; got != inner {
				t.Fatalf("inner Dir = %q, want %q", got, inner)
			}
			return nil
		}); err != nil {
			return err
		}
		if got := Command("command").Dir; got != outer {
			t.Fatalf("restored Dir = %q, want %q", got, outer)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDoReturnsError(t *testing.T) {
	want := errors.New("failed")
	if err := Do(Scope{}, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("Do error = %v, want %v", err, want)
	}
}

func TestExecBrokerHelperProcess(t *testing.T) {
	value := os.Getenv("EXECBROKER_HELPER")
	if value == "" {
		return
	}
	_, _ = os.Stdout.WriteString(value)
	os.Exit(0)
}
