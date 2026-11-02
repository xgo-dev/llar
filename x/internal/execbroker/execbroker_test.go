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

func TestPrintlnScope(t *testing.T) {
	var stdout bytes.Buffer
	err := Do(Scope{Stdout: &stdout}, func() error {
		_, err := Println("hello")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "hello\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestCommandMiddleware(t *testing.T) {
	err := Do(Scope{Middleware: func(req Request) (Request, error) {
		req.Name = "replacement"
		req.Args = append([]string{"prefix"}, req.Args...)
		req.Env = []string{"KEY=value"}
		req.Dir = "/work"
		return req, nil
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

func TestMiddlewareErrorStopsCommand(t *testing.T) {
	want := errors.New("command rejected")
	tests := []struct {
		name string
		run  func(*bytes.Buffer) error
	}{
		{
			name: "Command",
			run: func(output *bytes.Buffer) error {
				cmd := Command(os.Args[0], "-test.run=TestExecBrokerHelperProcess")
				cmd.Env = append(os.Environ(), "EXECBROKER_HELPER=executed")
				cmd.Stdout = output
				return cmd.Run()
			},
		},
		{
			name: "CommandContext",
			run: func(output *bytes.Buffer) error {
				cmd := CommandContext(context.Background(), os.Args[0], "-test.run=TestExecBrokerHelperProcess")
				cmd.Env = append(os.Environ(), "EXECBROKER_HELPER=executed")
				cmd.Stdout = output
				return cmd.Run()
			},
		},
		{
			name: "Run",
			run: func(output *bytes.Buffer) error {
				cmd := exec.Command(os.Args[0], "-test.run=TestExecBrokerHelperProcess")
				cmd.Env = append(os.Environ(), "EXECBROKER_HELPER=executed")
				cmd.Stdout = output
				return Run(cmd)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := Do(Scope{Middleware: func(req Request) (Request, error) {
				return req, want
			}}, func() error {
				return tt.run(&output)
			})
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if output.Len() != 0 {
				t.Fatalf("command executed with output %q", output.String())
			}
		})
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

func TestRunReplacesProcessStdout(t *testing.T) {
	var stdout bytes.Buffer
	err := Do(Scope{Stdout: &stdout}, func() error {
		cmd := exec.Command(os.Args[0], "-test.run=TestExecBrokerHelperProcess")
		cmd.Env = append(os.Environ(), "EXECBROKER_HELPER=scoped")
		cmd.Stdout = os.Stdout
		return Run(cmd)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "scoped" {
		t.Fatalf("output = %q, want scoped", got)
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

func TestScopedGetenvAndSetenv(t *testing.T) {
	key := "EXECBROKER_SCOPED_ENV_TEST"
	if err := os.Setenv(key, "process"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })

	err := Do(Scope{}, func() error {
		if got := Getenv(key); got != "process" {
			t.Fatalf("Getenv before Setenv = %q, want process", got)
		}
		if err := Setenv(key, "scoped"); err != nil {
			return err
		}
		if got := Getenv(key); got != "scoped" {
			t.Fatalf("Getenv after Setenv = %q, want scoped", got)
		}
		if got := os.Getenv(key); got != "process" {
			t.Fatalf("process environment = %q, want process", got)
		}
		var got string
		prefix := key + "="
		for _, entry := range Command("command").Env {
			if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
				got = entry[len(prefix):]
			}
		}
		if got != "scoped" {
			t.Fatalf("command environment = %q, want scoped", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := Getenv(key); got != "process" {
		t.Fatalf("Getenv after scope = %q, want process", got)
	}
}

func TestScopedEnvironmentAPIs(t *testing.T) {
	key := "EXECBROKER_ENV_APIS_TEST"
	if err := os.Setenv(key, "process"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })

	err := Do(Scope{}, func() error {
		if got, ok := LookupEnv(key); got != "process" || !ok {
			t.Fatalf("LookupEnv before Setenv = %q, %v; want process, true", got, ok)
		}
		if got := ExpandEnv("$" + key); got != "process" {
			t.Fatalf("ExpandEnv before Setenv = %q, want process", got)
		}
		if err := Setenv(key, "scoped"); err != nil {
			return err
		}
		if got, ok := LookupEnv(key); got != "scoped" || !ok {
			t.Fatalf("LookupEnv after Setenv = %q, %v; want scoped, true", got, ok)
		}
		if got := ExpandEnv("${" + key + "}"); got != "scoped" {
			t.Fatalf("ExpandEnv after Setenv = %q, want scoped", got)
		}
		if got := envValue(Environ(), key); got != "scoped" {
			t.Fatalf("Environ value = %q, want scoped", got)
		}
		if err := Unsetenv(key); err != nil {
			return err
		}
		if _, ok := LookupEnv(key); ok {
			t.Fatal("LookupEnv after Unsetenv = present, want absent")
		}
		if err := Setenv(key, "scoped-again"); err != nil {
			return err
		}
		Clearenv()
		if got := len(Environ()); got != 0 {
			t.Fatalf("Environ after Clearenv = %d entries, want zero", got)
		}
		if got := Getenv(key); got != "" {
			t.Fatalf("Getenv after Clearenv = %q, want empty", got)
		}
		if got := os.Getenv(key); got != "process" {
			t.Fatalf("process environment = %q, want process", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "process" {
		t.Fatalf("process environment after scope = %q, want process", got)
	}
}

func TestScopedEnvironmentRestoresNestedScope(t *testing.T) {
	key := "EXECBROKER_NESTED_ENV_TEST"
	err := Do(Scope{Env: []string{key + "=outer"}}, func() error {
		if got := Getenv(key); got != "outer" {
			t.Fatalf("outer Getenv = %q, want outer", got)
		}
		if err := Do(Scope{Env: []string{key + "=inner"}}, func() error {
			if got := Getenv(key); got != "inner" {
				t.Fatalf("inner Getenv = %q, want inner", got)
			}
			return Setenv(key, "inner-updated")
		}); err != nil {
			return err
		}
		if got := Getenv(key); got != "outer" {
			t.Fatalf("restored Getenv = %q, want outer", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestScopedEnvironmentIsGoroutineLocal(t *testing.T) {
	key := "EXECBROKER_GOROUTINE_ENV_TEST"
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan string, 2)

	for _, value := range []string{"one", "two"} {
		value := value
		go func() {
			_ = Do(Scope{}, func() error {
				if err := Setenv(key, value); err != nil {
					return err
				}
				ready <- struct{}{}
				<-start
				results <- Getenv(key)
				return nil
			})
		}()
	}
	for range 2 {
		<-ready
	}
	close(start)

	got := map[string]bool{<-results: true, <-results: true}
	if !got["one"] || !got["two"] {
		t.Fatalf("goroutine-scoped values = %v, want one and two", got)
	}
}

func TestScopedSetenvRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		label string
		name  string
		value string
	}{
		{label: "empty name", name: "", value: "value"},
		{label: "equals in name", name: "bad=name", value: "value"},
		{label: "nul in name", name: "bad\x00name", value: "value"},
		{label: "nul in value", name: "name", value: "bad\x00value"},
	} {
		t.Run(test.label, func(t *testing.T) {
			if err := Do(Scope{}, func() error { return Setenv(test.name, test.value) }); err == nil {
				t.Fatalf("Setenv(%q, %q) error = nil", test.name, test.value)
			}
		})
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
