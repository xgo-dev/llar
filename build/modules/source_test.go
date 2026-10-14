package modules

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/goplus/llar/build/formula/repo"
	classfile "github.com/goplus/llar/build/runtime/formula"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
)

func TestNewFormulaModule(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON", classfile.Matrix{}, &mockVCSRepo{})

	if mod == nil {
		t.Fatal("newFormulaModule returned nil")
	}
	if mod.modPath != "DaveGamble/cJSON" {
		t.Errorf("modPath = %q, want %q", mod.modPath, "DaveGamble/cJSON")
	}
	if mod.comparator == nil {
		t.Error("comparator should be initialized")
	}
}

func TestFormulaModule_Comparator(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	reads := 0
	counted := fakeReadFileFS{
		open: fsys.Open,
		readFile: func(name string) ([]byte, error) {
			reads++
			return fs.ReadFile(fsys, name)
		},
	}
	mod := newFormulaModule(counted, "DaveGamble/cJSON", classfile.Matrix{}, &mockVCSRepo{})

	// First call should load comparator
	cmp, err := mod.comparator()
	if err != nil {
		t.Fatalf("comparator() failed: %v", err)
	}
	if cmp == nil {
		t.Fatal("comparator() returned nil")
	}

	// Test comparator works correctly (cJSON uses semver, needs v prefix)
	v1 := module.Version{Path: "DaveGamble/cJSON", Version: "v1.0.0"}
	v2 := module.Version{Path: "DaveGamble/cJSON", Version: "v2.0.0"}
	if result := cmp(v1, v2); result >= 0 {
		t.Errorf("cmp(v1.0.0, v2.0.0) = %d, want < 0", result)
	}
	if result := cmp(v2, v1); result <= 0 {
		t.Errorf("cmp(v2.0.0, v1.0.0) = %d, want > 0", result)
	}
	if result := cmp(v1, v1); result != 0 {
		t.Errorf("cmp(v1.0.0, v1.0.0) = %d, want 0", result)
	}

	// Loading the comparator again must preserve its ordering.
	cmp2, err := mod.comparator()
	if err != nil {
		t.Fatalf("second comparator() failed: %v", err)
	}
	if cmp(v1, v2) != cmp2(v1, v2) {
		t.Error("reloaded comparator produces different results")
	}
	if reads != 2 {
		t.Fatalf("comparator file reads = %d, want 2", reads)
	}
}

func TestFormulaModule_ComparatorDefaultFallback(t *testing.T) {
	fsys := os.DirFS("testdata/madler/zlib")
	// madler/zlib has no comparator file, should use default
	mod := newFormulaModule(fsys, "madler/zlib", classfile.Matrix{}, &mockVCSRepo{})

	cmp, err := mod.comparator()
	if err != nil {
		t.Fatalf("comparator() failed: %v", err)
	}
	if cmp == nil {
		t.Fatal("comparator() returned nil")
	}

	// Default comparator should work with gnu version comparison
	v1 := module.Version{Path: "madler/zlib", Version: "1.0.0"}
	v2 := module.Version{Path: "madler/zlib", Version: "2.0.0"}
	if result := cmp(v1, v2); result >= 0 {
		t.Errorf("default cmp(1.0.0, 2.0.0) = %d, want < 0", result)
	}
}

func TestFormulaModule_At(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON", classfile.Matrix{}, &mockVCSRepo{})

	// Test getting formula for version v1.7.18 (should match fromVer v1.5.0)
	f, err := mod.at("v1.7.18")
	if err != nil {
		t.Fatalf("at() failed: %v", err)
	}
	if f == nil {
		t.Fatal("at() returned nil")
	}
	if f.FromVer != "v1.5.0" {
		t.Errorf("FromVer = %q, want %q", f.FromVer, "v1.5.0")
	}

	// Each selection reads a fresh formula with independent instance state.
	f2, err := mod.at("v1.7.18")
	if err != nil {
		t.Fatalf("second at() failed: %v", err)
	}
	if f == f2 {
		t.Error("at() reused a formula instance")
	}
}

func TestFormulaModule_AtReturnsIsolatedClasses(t *testing.T) {
	mod := newFormulaModule(os.DirFS("testdata/load/towner/targetreq"), "towner/targetreq", classfile.Matrix{}, &mockVCSRepo{})

	const workers = 32
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			f, err := mod.at("1.0.0")
			if err != nil {
				errs <- err
				return
			}
			ssl := "securetransport"
			want := false
			wantDeps := 0
			if i%2 == 0 {
				ssl = "openssl"
				want = true
				wantDeps = 1
			}
			f.SetMatrix(classfile.Matrix{
				Require: map[string][]string{"os": {"linux"}},
				Options: map[string][]string{"ssl": {ssl}},
			})
			if got := f.Filter(); got != want {
				errs <- fmt.Errorf("worker %d: Filter() = %v, want %v", i, got, want)
				return
			}

			var deps classfile.ModuleDeps
			f.OnRequire(&classfile.Project{}, &deps)
			if got := len(deps.Deps()); got != wantDeps {
				errs <- fmt.Errorf("worker %d: deps = %d, want %d", i, got, wantDeps)
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestFormulaModule_AtVersionMatching(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON", classfile.Matrix{}, &mockVCSRepo{})

	tests := []struct {
		version     string
		wantFromVer string
	}{
		{"v1.0.0", "v1.0.0"},
		{"v1.2.0", "v1.0.0"},
		{"v1.5.0", "v1.5.0"},
		{"v1.7.18", "v1.5.0"},
		{"v2.0.0", "v2.0.0"},
		{"v2.5.0", "v2.0.0"},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			f, err := mod.at(tt.version)
			if err != nil {
				t.Fatalf("at(%q) failed: %v", tt.version, err)
			}
			if f.FromVer != tt.wantFromVer {
				t.Errorf("at(%q).FromVer = %q, want %q", tt.version, f.FromVer, tt.wantFromVer)
			}
		})
	}
}

func TestFormulaModule_AtNoFormula(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON", classfile.Matrix{}, &mockVCSRepo{})

	// Version lower than all fromVer should fail
	_, err := mod.at("v0.5.0")
	if err == nil {
		t.Error("at() should fail for version lower than all fromVer")
	}
}

func TestFormulaModule_AtNonexistentModule(t *testing.T) {
	fsys := os.DirFS("testdata/nonexistent")
	mod := newFormulaModule(fsys, "nonexistent/module", classfile.Matrix{}, &mockVCSRepo{})

	_, err := mod.at("1.0.0")
	if err == nil {
		t.Error("at() should fail for nonexistent module")
	}
}

func TestFormulaModule_FindMaxFromVer(t *testing.T) {
	cases := []struct {
		path     string
		versions []string
		request  string
		want     string
	}{
		{"DaveGamble/cJSON", []string{"v2.0.0", "v1.5.0", "v1.0.0"}, "v1.7.18", "v1.5.0"},
		{"madler/zlib", []string{"2.0.0", "1.0.0"}, "1.5.0", "1.0.0"},
	}
	for _, tt := range cases {
		t.Run(tt.path, func(t *testing.T) {
			fsys := os.DirFS("testdata/" + tt.path)
			var mu sync.Mutex
			reads := make(map[string]int)
			counted := fakeReadFileFS{
				open: fsys.Open,
				readFile: func(name string) ([]byte, error) {
					mu.Lock()
					reads[name]++
					mu.Unlock()
					return fs.ReadFile(fsys, name)
				},
			}
			mod := newFormulaModule(counted, tt.path, classfile.Matrix{}, &mockVCSRepo{})
			compare, err := mod.comparator()
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					f, err := mod.findMaxFromVer(module.Version{Path: tt.path, Version: tt.request}, compare)
					if err != nil {
						t.Error(err)
						return
					}
					if f.FromVer != tt.want || f.ModPath != tt.path {
						t.Errorf("selected %s@%s, want %s@%s", f.ModPath, f.FromVer, tt.path, tt.want)
					}
				})
			}
			wg.Wait()
			for _, version := range tt.versions {
				selected, err := mod.findMaxFromVer(module.Version{Path: tt.path, Version: version}, compare)
				if err != nil {
					t.Fatal(err)
				}
				if selected.FromVer != version {
					t.Errorf("selected version = %q, want %q", selected.FromVer, version)
				}
			}
			if _, err := mod.findMaxFromVer(module.Version{Path: tt.path, Version: "0.0.0"}, compare); err == nil {
				t.Error("expected an error below the minimum fromVer")
			}
			if len(reads) < len(tt.versions) {
				t.Fatalf("only read %d files, want at least %d", len(reads), len(tt.versions))
			}
			for path, n := range reads {
				wantReads := 1 // The test loads its comparator once.
				if strings.HasSuffix(path, "_llar.gox") {
					wantReads = 8 + len(tt.versions) + 1
				}
				if n != wantReads {
					t.Errorf("read %s %d times, want %d", path, n, wantReads)
				}
			}
		})
	}
}

func TestIntegration_FormulaModuleToFormula(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON", classfile.Matrix{}, &mockVCSRepo{})

	// Get formula
	f, err := mod.at("v1.7.18")
	if err != nil {
		t.Fatalf("at() failed: %v", err)
	}

	// Verify formula
	if f.ModPath != "DaveGamble/cJSON" {
		t.Errorf("ModPath = %q, want %q", f.ModPath, "DaveGamble/cJSON")
	}
	if f.FromVer != "v1.5.0" {
		t.Errorf("FromVer = %q, want %q", f.FromVer, "v1.5.0")
	}
}

func TestIntegration_MultipleModules(t *testing.T) {
	modules := []struct {
		dir     string
		path    string
		version string
	}{
		{"testdata/DaveGamble/cJSON", "DaveGamble/cJSON", "v1.7.18"},
		{"testdata/madler/zlib", "madler/zlib", "1.5.0"},
	}

	for _, m := range modules {
		fsys := os.DirFS(m.dir)
		mod := newFormulaModule(fsys, m.path, classfile.Matrix{}, &mockVCSRepo{})

		f, err := mod.at(m.version)
		if err != nil {
			t.Errorf("at(%q) for %q failed: %v", m.version, m.path, err)
			continue
		}

		if f == nil {
			t.Errorf("at(%q) for %q returned nil", m.version, m.path)
		}
	}
}

func TestIntegration_RealRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real repo test in short mode")
	}

	tmpDir := t.TempDir()

	// Use real vcs.Repo with llarmvp-formula repository
	vcsRepo, err := vcs.NewRepo("github.com/MeteorsLiu/llarmvp-formula")
	if err != nil {
		t.Fatalf("failed to create vcs.Repo: %v", err)
	}

	store := repo.New(tmpDir, vcsRepo)

	// Test madler/zlib module
	ctx := context.Background()
	fsys, err := store.ModuleFS(ctx, "madler/zlib")
	if err != nil {
		t.Fatalf("ModuleFS() failed: %v", err)
	}

	mod := newFormulaModule(fsys, "madler/zlib", classfile.Matrix{}, &mockVCSRepo{})

	// Test comparator (should fall back to GNU version comparison)
	cmp, err := mod.comparator()
	if err != nil {
		t.Fatalf("comparator() failed: %v", err)
	}

	v1 := module.Version{Path: "madler/zlib", Version: "1.0.0"}
	v2 := module.Version{Path: "madler/zlib", Version: "2.0.0"}
	if result := cmp(v1, v2); result >= 0 {
		t.Errorf("cmp(1.0.0, 2.0.0) = %d, want < 0", result)
	}

	// Test findMaxFromVer (version matching without loading full formula)
	target := module.Version{Path: "madler/zlib", Version: "1.3.0"}
	f, err := mod.findMaxFromVer(target, cmp)
	if err != nil {
		t.Fatalf("findMaxFromVer() failed: %v", err)
	}

	if f.FromVer == "" {
		t.Error("fromVer is empty")
	}
	if f.ModPath != "madler/zlib" {
		t.Errorf("module path = %q, want madler/zlib", f.ModPath)
	}
	t.Logf("found formula: fromVer=%s, module=%s", f.FromVer, f.ModPath)
}
