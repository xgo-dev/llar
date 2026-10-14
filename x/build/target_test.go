package build

import (
	"context"
	"reflect"
	"testing"

	"github.com/goplus/llar/internal/execbroker"
	"github.com/goplus/llar/mod/module"
	"github.com/goplus/llar/x/modules"
)

type testTarget struct {
	command Command
}

func (t *testTarget) Use(command Command) Patch {
	t.command = command
	return Patch{
		Name:       "/toolchain/cc",
		PrependArg: []string{"--target=aarch64-linux-gnu"},
		AppendArg:  []string{"--sysroot=/sdk"},
		Env:        []string{"CC=/toolchain/cc"},
	}
}

func TestTargetMiddleware(t *testing.T) {
	target := new(testTarget)
	got, err := targetMiddleware(target)(execbroker.Request{
		Name: "cc",
		Args: []string{"-c", "a.c"},
		Env:  []string{"CFLAGS=-O2"},
		Dir:  "/src",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "/toolchain/cc" {
		t.Fatalf("Name = %q", got.Name)
	}
	if want := []string{"--target=aarch64-linux-gnu", "-c", "a.c", "--sysroot=/sdk"}; !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("Args = %q, want %q", got.Args, want)
	}
	if want := []string{"CC=/toolchain/cc"}; !reflect.DeepEqual(got.Env, want) {
		t.Fatalf("Env = %q, want %q", got.Env, want)
	}
	if target.command.Name != "cc" || target.command.Dir != "/src" {
		t.Fatalf("Command = %+v", target.command)
	}
	if want := []string{"CFLAGS=-O2"}; !reflect.DeepEqual(target.command.Env, want) {
		t.Fatalf("Command.Env = %q, want %q", target.command.Env, want)
	}
}

func TestBuildAppliesTargetToFormulaCommands(t *testing.T) {
	store := setupTestStore(t)
	builder := setupBuilder(t, store, matrixForTest("arm64", "linux"))
	target := new(testTarget)
	builder.target = target

	mods, err := modules.Load(context.Background(), module.Version{Path: "test/targetcmd", Version: "1.0.0"}, modules.Options{
		FormulaStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := builder.Build(context.Background(), mods)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Metadata != "/toolchain/cc" {
		t.Fatalf("results = %+v, want patched command name /toolchain/cc", results)
	}
	if target.command.Name != "cc" {
		t.Fatalf("target command = %+v", target.command)
	}
}
