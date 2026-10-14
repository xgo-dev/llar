package modules

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goplus/llar/mod/module"
	"github.com/goplus/llar/x/formula"
	"github.com/goplus/llar/x/formula/repo"
	classfile "github.com/goplus/llar/x/runtime/formula"
)

func TestLoadFormulaFS(t *testing.T) {
	for _, tt := range []struct {
		version string
		fromVer string
	}{
		{version: "1.5.0", fromVer: "1.0.0"},
		{version: "2.5.0", fromVer: "2.0.0"},
	} {
		t.Run(tt.version, func(t *testing.T) {
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS("testdata/load")); err != nil {
				t.Fatal(err)
			}
			// The physical directory need not have the same name as fromVer.
			moduleDir := filepath.Join(root, "towner", "leafmod")
			if err := os.Rename(filepath.Join(moduleDir, "2.0.0"), filepath.Join(moduleDir, "recipe")); err != nil {
				t.Fatal(err)
			}
			store := repo.New(root, &mockVCSRepo{})
			main := module.Version{Path: "towner/leafmod", Version: tt.version}
			mods, err := Load(context.Background(), main, Options{FormulaStore: store})
			if err != nil {
				t.Fatal(err)
			}
			if len(mods) != 1 || mods[0].FS == nil {
				t.Fatalf("got modules %+v, want one module with a formula FS", mods)
			}
			if mods[0].Version != tt.version {
				t.Fatalf("module version = %q, want %q", mods[0].Version, tt.version)
			}
			formulas, err := formula.LoadFS(mods[0].FS, ".")
			if err != nil {
				t.Fatal(err)
			}
			if len(formulas) != 1 || formulas[0].ModPath != main.Path || formulas[0].FromVer != tt.fromVer {
				t.Fatalf("reloaded formulas = %+v, want %s from %s", formulas, main.Path, tt.fromVer)
			}
			if _, err := fs.ReadFile(mods[0].FS, "Leafmod_llar.gox"); err != nil {
				t.Fatalf("selected formula is not at the FS root: %v", err)
			}
			if _, err := fs.ReadFile(mods[0].FS, "versions.json"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("module-root versions.json is visible in the formula FS: %v", err)
			}
		})
	}
}

func TestLoadDependencyFormulaFS(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	mods, err := Load(context.Background(), module.Version{Path: "towner/diamond", Version: "1.0.0"}, Options{FormulaStore: store})
	if err != nil {
		t.Fatal(err)
	}
	for _, mod := range mods {
		for _, entry := range append([]*Module{mod}, mod.Deps...) {
			if entry.FS == nil {
				t.Fatalf("missing formula FS for %s@%s", entry.Path, entry.Version)
			}
			formulas, err := formula.LoadFS(entry.FS, ".")
			if err != nil {
				t.Fatal(err)
			}
			if len(formulas) != 1 || formulas[0].ModPath != entry.Path || formulas[0].FromVer != entry.Version {
				t.Fatalf("wrong formula FS for %s@%s: %+v", entry.Path, entry.Version, formulas)
			}
		}
	}
}

func TestLoadFormulaResources(t *testing.T) {
	store := setupTestStore(t, "../build/testdata/formulas")
	mods, err := Load(context.Background(), module.Version{Path: "test/readcfg", Version: "1.0.0"}, Options{FormulaStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 1 {
		t.Fatalf("module count = %d, want 1", len(mods))
	}
	formulas, err := formula.LoadFS(mods[0].FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(formulas) != 1 {
		t.Fatalf("formula count = %d, want 1", len(formulas))
	}
	ctx := classfile.NewContext(&classfile.Project{SourceFS: mods[0].FS.(fs.ReadFileFS)}, "", "", "", nil)
	if err := runFormulaHook(func() { formulas[0].OnBuild(ctx) }); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(ctx.Out.Metadata()); got != "-lreadcfg" {
		t.Fatalf("metadata = %q, want -lreadcfg", got)
	}
}
