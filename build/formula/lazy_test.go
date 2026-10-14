package formula

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/goplus/ixgo"
	classfile "github.com/goplus/llar/build/runtime/formula"
	"github.com/goplus/llar/internal/execbroker"
)

func TestConcurrentLazyLoad(t *testing.T) {
	for _, mode := range []string{"load", "clone"} {
		t.Run(mode, func(t *testing.T) {
			f, err := loadFS(os.DirFS("testdata/formula").(fs.ReadFileFS), "targetsurface_llar.gox")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "clone" {
				f = Clone(f)
			}
			results := make([]*Formula, 8)
			var wg sync.WaitGroup
			for i := range results {
				wg.Go(func() { results[i] = f.load() })
			}
			wg.Wait()
			for _, result := range results {
				if result == nil || result != results[0] {
					t.Fatal("concurrent calls did not share one initialized instance")
				}
			}
		})
	}
}

func TestLazyLoadRepeatsPanic(t *testing.T) {
	for _, mode := range []string{"load", "clone"} {
		t.Run(mode, func(t *testing.T) {
			f, err := loadFS(os.DirFS("testdata/lazy").(fs.ReadFileFS), "2.0.0/Lazy_llar.gox")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "clone" {
				f = Clone(f)
			}
			var stdout bytes.Buffer
			var firstPanic any
			if err := execbroker.Do(execbroker.Scope{Stdout: &stdout}, func() error {
				for i := range 3 {
					func() {
						defer func() {
							got := recover()
							if got == nil {
								t.Error("expected initialization panic")
							} else if i == 0 {
								firstPanic = got
							} else if !reflect.DeepEqual(got, firstPanic) {
								t.Errorf("panic = %v, want original panic %v", got, firstPanic)
							}
						}()
						f.OnBuild(&classfile.Context{})
					}()
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != "failed initialization\n" {
				t.Fatalf("initialization output = %q, want one initialization", got)
			}
		})
	}
}

func TestLoadFSMetadata(t *testing.T) {
	_, before, _ := ixgo.IcallStat()
	formulas, err := LoadFS(os.DirFS("testdata/formula"), ".")
	if err != nil {
		t.Fatal(err)
	}
	_, after, _ := ixgo.IcallStat()
	if after != before {
		t.Fatalf("LoadFS allocated interpreter slots: before=%d, after=%d", before, after)
	}
	if len(formulas) != 5 {
		t.Fatalf("got %d formulas, want 5", len(formulas))
	}
	for _, f := range formulas {
		if f.ModPath == "" || f.FromVer == "" {
			t.Fatalf("missing metadata: %q@%q", f.ModPath, f.FromVer)
		}
		if f.ModPath == "google/cpu_features" {
			if f.FromVer != "v0.9.0" {
				t.Fatalf("FromVer = %q, want v0.9.0", f.FromVer)
			}
			if f.OnRequire != nil || f.OnBuild != nil || f.OnTest != nil || f.Filter != nil {
				t.Fatal("undeclared hooks must stay nil")
			}
		}
		if f.ModPath == "DaveGamble/cJSON" {
			if f.OnRequire == nil || f.OnBuild == nil || f.OnTest == nil || f.Filter != nil {
				t.Fatal("hook wrappers do not match hello's declarations")
			}
		}
	}
}

func TestLoadFSMissingPath(t *testing.T) {
	_, err := LoadFS(os.DirFS("testdata/formula"), "missing")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadFS error = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadFSExecutesSelectedFormula(t *testing.T) {
	for _, first := range []string{"onRequire", "onBuild", "onTest", "filter"} {
		t.Run(first, func(t *testing.T) {
			defer func() {
				if err := recover(); err != nil {
					t.Fatalf("hook %s failed: %v", first, err)
				}
			}()
			var stdout bytes.Buffer
			err := execbroker.Do(execbroker.Scope{Stdout: &stdout}, func() error {
				formulas, err := LoadFS(os.DirFS("testdata/lazy"), ".")
				if err != nil {
					return err
				}
				if stdout.Len() != 0 {
					t.Fatal("LoadFS executed formula initialization")
				}
				var selected *Formula
				for _, f := range formulas {
					if f.FromVer == "1.0.0" {
						selected = f
					}
				}
				if selected == nil {
					t.Fatal("formula 1.0.0 not found")
				}
				var deps classfile.ModuleDeps
				ctx := &classfile.Context{}
				switch first {
				case "onRequire":
					selected.OnRequire(&classfile.Project{}, &deps)
				case "onBuild":
					selected.OnBuild(ctx)
				case "onTest":
					selected.OnTest(ctx)
				case "filter":
					selected.Filter()
				}
				selected.OnRequire(&classfile.Project{}, &deps)
				selected.OnBuild(ctx)
				selected.OnTest(ctx)
				if !selected.Filter() || ctx.Out.Metadata() != "built" || len(deps.Deps()) == 0 {
					t.Fatal("loaded hooks did not execute")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(stdout.String(), "initialized\n"); got != 1 {
				t.Fatalf("formula initialized %d times, want 1; output=%q", got, stdout.String())
			}
		})
	}
}
