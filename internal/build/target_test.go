package build

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"

	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/execbroker"
	internalformula "github.com/goplus/llar/internal/formula"
	"github.com/goplus/llar/internal/modules"
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
	builder := setupBuilder(t, store, "arm64-linux")
	target := new(testTarget)
	builder.target = target

	var commandName string
	root := &modules.Module{
		Formula: &internalformula.Formula{OnBuild: func(*classfile.Context) {
			commandName = execbroker.Command("cc", "-c", "a.c").Args[0]
		}},
		FS:      fstest.MapFS{"README": {Data: []byte("test")}},
		Path:    "test/liba",
		Version: "1.0.0",
	}
	if _, err := builder.Build(context.Background(), []*modules.Module{root}); err != nil {
		t.Fatal(err)
	}
	if commandName != "/toolchain/cc" {
		t.Fatalf("command name = %q, want /toolchain/cc", commandName)
	}
	if target.command.Name != "cc" {
		t.Fatalf("target command = %+v", target.command)
	}
}
