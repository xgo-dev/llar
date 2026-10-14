package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	"github.com/goplus/llar/x/formula"
	"github.com/goplus/llar/x/formula/repo"
	classfile "github.com/goplus/llar/x/runtime/formula"
)

// mockVCSRepo implements vcs.Repo for testing without network access.
// Sync is a no-op because the testdata is pre-populated in the store directory.
type mockVCSRepo struct{}

type mockRefs struct{}

func (mockRefs) CompareFunc(a, b string, compareTag func(a, b string) int) int {
	return compareTag(a, b)
}

var _ vcs.Repo = (*mockVCSRepo)(nil)

func (m *mockVCSRepo) Tags(ctx context.Context) ([]vcs.Tag, error) { return nil, nil }
func (m *mockVCSRepo) Latest(ctx context.Context) (string, error)  { return "", nil }
func (m *mockVCSRepo) Refs() vcs.Refs                              { return mockRefs{} }
func (m *mockVCSRepo) At(ref, localDir string) fs.FS               { return os.DirFS(localDir) }
func (m *mockVCSRepo) Sync(ctx context.Context, ref, path, localDir string) error {
	return nil
}

type pinnedRepo struct {
	vcs.Repo
	ref string
}

func (r *pinnedRepo) Sync(ctx context.Context, _ string, path, localDir string) error {
	return r.Repo.Sync(ctx, r.ref, path, localDir)
}

// setupTestStore creates a repo.Store backed by a copy of testdataDir.
func setupTestStore(t *testing.T, testdataDir string) repo.Store {
	t.Helper()
	tmpDir := t.TempDir()
	if err := os.CopyFS(tmpDir, os.DirFS(testdataDir)); err != nil {
		t.Fatalf("failed to copy testdata: %v", err)
	}
	return repo.New(tmpDir, &mockVCSRepo{})
}

// loadTestFormula loads a formula from a local testdata directory.
func loadTestFormula(t *testing.T, moduleDir, modPath, version string) *formula.Formula {
	t.Helper()
	fsys := os.DirFS(moduleDir)
	mod := newFormulaModule(fsys, modPath, classfile.Matrix{}, &mockVCSRepo{})
	f, err := mod.at(version)
	if err != nil {
		t.Fatalf("failed to load formula for %s@%s: %v", modPath, version, err)
	}
	return f
}

func findModule(modules []*Module, path string) *Module {
	for _, mod := range modules {
		if mod.Path == path {
			return mod
		}
	}
	return nil
}

func depVersions(mod *Module) []module.Version {
	vers := make([]module.Version, 0, len(mod.Deps))
	for _, dep := range mod.Deps {
		vers = append(vers, module.Version{
			Path:    dep.Path,
			Version: dep.Version,
		})
	}
	return vers
}

// =============================================
// resolveDeps tests
// =============================================

func TestResolveDeps_NoOnRequire_DepsFromVersionsJson(t *testing.T) {
	modFS := os.DirFS("testdata/load/towner/mainmod").(fs.ReadFileFS)
	frla := loadTestFormula(t, "testdata/load/towner/mainmod", "towner/mainmod", "1.0.0")
	mod := module.Version{Path: "towner/mainmod", Version: "1.0.0"}

	deps, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{})
	if err != nil {
		t.Fatalf("resolveDeps failed: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d: %v", len(deps), deps)
	}
	if deps[0].Path != "towner/depmod" || deps[0].Version != "1.0.0" {
		t.Errorf("dep = %v, want {towner/depmod 1.0.0}", deps[0])
	}
}

func TestResolveDeps_NoOnRequire_NoDeps(t *testing.T) {
	modFS := os.DirFS("testdata/load/towner/leafmod").(fs.ReadFileFS)
	frla := loadTestFormula(t, "testdata/load/towner/leafmod", "towner/leafmod", "1.0.0")
	mod := module.Version{Path: "towner/leafmod", Version: "1.0.0"}

	deps, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{})
	if err != nil {
		t.Fatalf("resolveDeps failed: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected 0 deps, got %d: %v", len(deps), deps)
	}
}

func TestRunFormulaHookRecoversPanic(t *testing.T) {
	wantErr := errors.New("hook failed")
	tests := []struct {
		name       string
		panicValue any
		wantError  string
		wantCause  error
	}{
		{
			name:       "error panic",
			panicValue: wantErr,
			wantError:  "hook failed",
			wantCause:  wantErr,
		},
		{
			name:       "non-error panic",
			panicValue: "unexpected failure",
			wantError:  "formula hook panic: unexpected failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := func() {
				panic(tt.panicValue)
			}

			err := runFormulaHook(fn)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("runFormulaHook error = %v, want error containing %q", err, tt.wantError)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("runFormulaHook error does not wrap hook error %v: %v", tt.wantCause, err)
			}
		})
	}
}

func TestResolveDeps_VersionNotInDepsTable(t *testing.T) {
	modFS := os.DirFS("testdata/load/towner/mainmod").(fs.ReadFileFS)
	frla := loadTestFormula(t, "testdata/load/towner/mainmod", "towner/mainmod", "1.0.0")
	// Version 9.9.9 doesn't exist in versions.json deps table
	mod := module.Version{Path: "towner/mainmod", Version: "9.9.9"}

	deps, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{})
	if err != nil {
		t.Fatalf("resolveDeps failed: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected 0 deps for unknown version, got %d: %v", len(deps), deps)
	}
}

func TestResolveDeps_WithOnRequire_EchoOnly_FallbackToVersionsJson(t *testing.T) {
	frla := loadTestFormula(t, "testdata/load/towner/withreq", "towner/withreq", "1.0.0")
	modFS := os.DirFS("testdata/load/towner/withreq").(fs.ReadFileFS)
	mod := module.Version{Path: "towner/withreq", Version: "1.0.0"}

	deps, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{})
	if err != nil {
		t.Fatalf("resolveDeps failed: %v", err)
	}
	// OnRequire echoes but doesn't add deps, so fallback to versions.json
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep (from versions.json fallback), got %d: %v", len(deps), deps)
	}
	if deps[0].Path != "towner/depmod" {
		t.Errorf("dep path = %q, want %q (from versions.json)", deps[0].Path, "towner/depmod")
	}
}

func TestResolveDeps_OnRequire_SlashInVersion(t *testing.T) {
	frla := loadTestFormula(t, "testdata/load/towner/withreq", "towner/withreq", "1.0.0")
	modFS := os.DirFS("testdata/load/towner/withreq").(fs.ReadFileFS)
	mod := module.Version{Path: "towner/withreq", Version: "refs/heads/feature/foo"}

	if _, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{}); err != nil {
		t.Fatalf("resolveDeps failed for slash-containing version: %v", err)
	}
}

func TestResolveDeps_WithOnRequire_AddsDeps(t *testing.T) {
	frla := loadTestFormula(t, "testdata/load/towner/withdeps", "towner/withdeps", "1.0.0")
	modFS := os.DirFS("testdata/load/towner/withdeps").(fs.ReadFileFS)
	mod := module.Version{Path: "towner/withdeps", Version: "1.0.0"}

	deps, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{})
	if err != nil {
		t.Fatalf("resolveDeps failed: %v", err)
	}
	// OnRequire adds depmod@1.0.0; versions.json has leafmod@1.0.0 (shouldn't be used)
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep (from OnRequire), got %d: %v", len(deps), deps)
	}
	if deps[0].Path != "towner/depmod" {
		t.Errorf("dep path = %q, want %q (from OnRequire, not versions.json)", deps[0].Path, "towner/depmod")
	}
}

func TestResolveDeps_WithOnRequire_EmptyVersionFallback(t *testing.T) {
	// OnRequire adds dep with empty version; resolveDeps resolves from versions.json
	frla := loadTestFormula(t, "testdata/load/towner/reqnover", "towner/reqnover", "1.0.0")
	modFS := os.DirFS("testdata/load/towner/reqnover").(fs.ReadFileFS)
	mod := module.Version{Path: "towner/reqnover", Version: "1.0.0"}

	deps, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{})
	if err != nil {
		t.Fatalf("resolveDeps failed: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d: %v", len(deps), deps)
	}
	// OnRequire adds "towner/depmod" with empty version
	// versions.json has "towner/depmod" @ "2.0.0"
	// resolveDeps should resolve the version from versions.json
	if deps[0].Path != "towner/depmod" {
		t.Errorf("dep path = %q, want %q", deps[0].Path, "towner/depmod")
	}
	if deps[0].Version != "2.0.0" {
		t.Errorf("dep version = %q, want %q (resolved from versions.json)", deps[0].Version, "2.0.0")
	}
}

func TestResolveDeps_WithOnRequire_UnknownDepDropped(t *testing.T) {
	// OnRequire adds dep with empty version that doesn't exist in versions.json
	// This dep should be dropped (idx < 0 path)
	frla := loadTestFormula(t, "testdata/load/towner/reqdrop", "towner/reqdrop", "1.0.0")
	modFS := os.DirFS("testdata/load/towner/reqdrop").(fs.ReadFileFS)
	mod := module.Version{Path: "towner/reqdrop", Version: "1.0.0"}

	deps, err := resolveDeps(mod, modFS, frla, &mockVCSRepo{})
	if err != nil {
		t.Fatalf("resolveDeps failed: %v", err)
	}
	// OnRequire adds "towner/unknown" with empty version, which isn't in versions.json
	// so it gets dropped. Since OnRequire returned deps (even though dropped),
	// len(vers) == 0, so we fall back to versions.json which has depmod@1.0.0
	// Wait - actually the vers would be empty after dropping, so fallback kicks in
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep (from versions.json fallback), got %d: %v", len(deps), deps)
	}
	if deps[0].Path != "towner/depmod" {
		t.Errorf("dep path = %q, want %q", deps[0].Path, "towner/depmod")
	}
}

func TestResolveDeps_WithOnRequire_ReadsCMakeListsDeps(t *testing.T) {
	// Load the formula whose OnRequire reads CMakeLists.txt and parses find_package() directives
	frla := loadTestFormula(t, "testdata/load/towner/cmakereq", "towner/cmakereq", "1.0.0")
	if frla.OnRequire == nil {
		t.Fatal("expected OnRequire to be non-nil")
	}

	// Create a source directory with a sample CMakeLists.txt
	sourceDir := t.TempDir()
	cmakeContent := `cmake_minimum_required(VERSION 3.10)
project(cmakereq)

find_package(depmod REQUIRED)
find_package(leafmod REQUIRED)
`
	if err := os.WriteFile(sourceDir+"/CMakeLists.txt", []byte(cmakeContent), 0644); err != nil {
		t.Fatal(err)
	}

	proj := &classfile.Project{
		SourceFS: os.DirFS(sourceDir).(fs.ReadFileFS),
	}
	var deps classfile.ModuleDeps

	frla.OnRequire(proj, &deps)

	got := deps.Deps()
	if len(got) != 2 {
		t.Fatalf("expected 2 deps from CMakeLists.txt, got %d: %v", len(got), got)
	}
	// OnRequire should extract package names from find_package() directives
	// and map them to module paths with empty versions (resolved later from versions.json)
	expected := []module.Version{
		{Path: "towner/depmod", Version: ""},
		{Path: "towner/leafmod", Version: ""},
	}
	if !slices.Equal(got, expected) {
		t.Errorf("deps mismatch:\n  got:  %v\n  want: %v", got, expected)
	}
}

func TestResolveDeps_WithOnRequire_CMakeListsNoCMakeFile(t *testing.T) {
	// When CMakeLists.txt is missing, OnRequire should return with no deps
	frla := loadTestFormula(t, "testdata/load/towner/cmakereq", "towner/cmakereq", "1.0.0")

	// Create a Project with an empty source directory (no CMakeLists.txt)
	emptyDir := t.TempDir()
	proj := &classfile.Project{
		SourceFS: os.DirFS(emptyDir).(fs.ReadFileFS),
	}
	var deps classfile.ModuleDeps

	frla.OnRequire(proj, &deps)

	if len(deps.Deps()) != 0 {
		t.Fatalf("expected 0 deps when CMakeLists.txt missing, got %d: %v", len(deps.Deps()), deps.Deps())
	}
}

// =============================================
// Load tests (unit, no network)
// =============================================

func TestLoad_SingleModuleNoDeps(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	main := module.Version{Path: "towner/standalone", Version: "1.0.0"}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if modules[0].Path != "towner/standalone" {
		t.Errorf("module path = %q, want %q", modules[0].Path, "towner/standalone")
	}
	if modules[0].Version != "1.0.0" {
		t.Errorf("module version = %q, want %q", modules[0].Version, "1.0.0")
	}
	if len(modules[0].Deps) != 0 {
		t.Errorf("expected 0 deps, got %d", len(modules[0].Deps))
	}
}

func TestLoad_ChainDeps(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	main := module.Version{Path: "towner/mainmod", Version: "1.0.0"}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Expect 3 modules: mainmod, depmod, leafmod
	if len(modules) != 3 {
		for _, m := range modules {
			t.Logf("  %s@%s", m.Path, m.Version)
		}
		t.Fatalf("expected 3 modules, got %d", len(modules))
	}

	// First module must be the main module
	if modules[0].Path != "towner/mainmod" {
		t.Errorf("modules[0].Path = %q, want %q", modules[0].Path, "towner/mainmod")
	}

	// Build path -> module map
	modMap := make(map[string]*Module)
	for _, m := range modules {
		modMap[m.Path] = m
	}

	// Verify all 3 modules exist
	for _, path := range []string{"towner/mainmod", "towner/depmod", "towner/leafmod"} {
		if _, ok := modMap[path]; !ok {
			t.Errorf("missing module %q in build list", path)
		}
	}

	// Verify dependency structure
	mainMod := modMap["towner/mainmod"]
	if len(mainMod.Deps) != 2 {
		t.Errorf("mainmod deps count = %d, want 2", len(mainMod.Deps))
	}

	depMod := modMap["towner/depmod"]
	if len(depMod.Deps) != 1 {
		t.Errorf("depmod deps count = %d, want 1", len(depMod.Deps))
	} else if depMod.Deps[0].Path != "towner/leafmod" {
		t.Errorf("depmod dep = %q, want %q", depMod.Deps[0].Path, "towner/leafmod")
	}

	leafMod := modMap["towner/leafmod"]
	if len(leafMod.Deps) != 0 {
		t.Errorf("leafmod deps count = %d, want 0", len(leafMod.Deps))
	}

	// Remaining modules should be sorted by path (MVS guarantee)
	for i := 2; i < len(modules); i++ {
		if modules[i].Path < modules[i-1].Path {
			t.Errorf("modules not sorted: %q before %q", modules[i-1].Path, modules[i].Path)
		}
	}
}

func TestLoad_DependencyInfo(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	main := module.Version{Path: "towner/standalone", Version: "1.0.0"}
	modules, err := Load(context.Background(), main, Options{FormulaStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 1 {
		t.Fatalf("module count = %d, want 1", len(modules))
	}
	if modules[0].Path != main.Path || modules[0].Version != main.Version || len(modules[0].Deps) != 0 {
		t.Fatalf("module = %+v, want standalone@1.0.0 without dependencies", modules[0])
	}
}

func TestLoad_InjectsTargetBeforeFilterAndOnRequire(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	main := module.Version{Path: "towner/targetreq", Version: "1.0.0"}

	mods, err := Load(ctx, main, Options{
		FormulaStore: store,
		Matrix: classfile.Matrix{
			Require: map[string][]string{"os": {"linux"}},
		},
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(mods) != 3 {
		t.Fatalf("modules len = %d, want 3", len(mods))
	}
	if mods[0].Path != "towner/targetreq" {
		t.Fatalf("main module = %q, want %q", mods[0].Path, "towner/targetreq")
	}
	if findModule(mods, "towner/depmod") == nil {
		t.Fatalf("missing towner/depmod in build list")
	}

	if !slices.Equal(depVersions(mods[0]), []module.Version{
		{Path: "towner/depmod", Version: "1.0.0"},
		{Path: "towner/leafmod", Version: "1.0.0"},
	}) {
		t.Fatalf("selected dependencies = %+v", depVersions(mods[0]))
	}

}

func TestFormulaContext_AtInjectsMatrix(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	formulaCtx := newFormulaContext(store.ModuleFS, classfile.Matrix{
		Require: map[string][]string{"os": {"linux"}},
	})

	mod, err := formulaCtx.moduleOf(context.Background(), "towner/targetreq")
	if err != nil {
		t.Fatalf("moduleOf failed: %v", err)
	}
	f, err := mod.at("1.0.0")
	if err != nil {
		t.Fatalf("at failed: %v", err)
	}

	var deps classfile.ModuleDeps
	f.OnRequire(&classfile.Project{}, &deps)
	if got := len(deps.Deps()); got != 1 {
		t.Fatalf("OnRequire deps = %d, want 1", got)
	}
	buildCtx := classfile.NewContext(&classfile.Project{}, "", "", "", nil)
	f.OnBuild(buildCtx)
	if got := buildCtx.Out.Metadata(); got != "-ltargetreq-linux-openssl" {
		t.Fatalf("OnBuild metadata = %q, want -ltargetreq-linux-openssl", got)
	}
}

func TestFormulaContext_AtInjectsSelectedMatrix(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	matrix := classfile.Matrix{
		Require: map[string][]string{"os": {"linux"}},
		Options: map[string][]string{"ssl": {"securetransport"}},
	}
	formulaCtx := newFormulaContext(store.ModuleFS, matrix)
	mod, err := formulaCtx.moduleOf(context.Background(), "towner/targetreq")
	if err != nil {
		t.Fatal(err)
	}
	f, err := mod.at("1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if f.Filter() {
		t.Fatal("Filter accepted securetransport, want rejection")
	}
	formulaElem := reflect.ValueOf(f).Elem()
	structElem := valueOf(formulaElem, "structElem").(reflect.Value)
	target := valueOf(structElem, "target").(classfile.Matrix)
	if got := target.Require["os"]; !slices.Equal(got, []string{"linux"}) {
		t.Errorf("target.require[os] = %v, want [linux]", got)
	}
	if got := target.Options["ssl"]; !slices.Equal(got, []string{"securetransport"}) {
		t.Errorf("target.options[ssl] = %v, want [securetransport]", got)
	}
}

func TestConvertToModulesDoesNotLoadFormulas(t *testing.T) {
	formulaCtx := newFormulaContext(func(context.Context, string) (fs.FS, error) {
		t.Fatal("converting dependency records must not access formula sources")
		return nil, nil
	}, classfile.Matrix{})
	versions := []module.Version{
		{Path: "towner/mainmod", Version: "1.0.0"},
		{Path: "towner/leafmod", Version: "2.0.0"},
	}
	sources := map[module.Version]fs.FS{
		versions[0]: os.DirFS("testdata/load/towner/mainmod/1.0.0"),
		versions[1]: os.DirFS("testdata/load/towner/leafmod/2.0.0"),
	}
	mods := formulaCtx.convertToModules(versions, sources)
	if len(mods) != len(versions) {
		t.Fatalf("module count = %d, want %d", len(mods), len(versions))
	}
	for i, mod := range mods {
		if mod.Path != versions[i].Path || mod.Version != versions[i].Version || len(mod.Deps) != 0 {
			t.Errorf("record %d = %+v, want %+v without edges", i, mod, versions[i])
		}
		if mod.FS != sources[versions[i]] {
			t.Errorf("record %d did not retain its selected formula directory", i)
		}
	}
}

func TestLoad_FilterRejectsSelectedMatrix(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	main := module.Version{Path: "towner/targetreq", Version: "1.0.0"}

	_, err := Load(ctx, main, Options{
		FormulaStore: store,
		Matrix: classfile.Matrix{
			Require: map[string][]string{"os": {"linux"}},
			Options: map[string][]string{"ssl": {"securetransport"}},
		},
	})
	if err == nil {
		t.Fatal("expected filter rejection")
	}
	if !strings.Contains(err.Error(), "does not support selected matrix") {
		t.Fatalf("error = %v, want selected matrix rejection", err)
	}
}

func TestLoad_ErrorNoFormulaForVersion(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	// Version 0.1.0 is lower than all fromVer values (1.0.0)
	main := module.Version{Path: "towner/standalone", Version: "0.1.0"}

	_, err := Load(ctx, main, Options{FormulaStore: store})
	if err == nil {
		t.Error("expected error for version lower than all fromVer")
	}
}

func TestFormulaContextReloadsSource(t *testing.T) {
	wantErr := errors.New("formula source unavailable")
	reads := 0
	formulaCtx := newFormulaContext(func(context.Context, string) (fs.FS, error) {
		reads++
		if reads == 1 {
			return os.DirFS("testdata/load/towner/standalone"), nil
		}
		return nil, wantErr
	}, classfile.Matrix{})
	if _, err := formulaCtx.moduleOf(context.Background(), "towner/standalone"); err != nil {
		t.Fatal(err)
	}
	if _, err := formulaCtx.moduleOf(context.Background(), "towner/standalone"); !errors.Is(err, wantErr) {
		t.Fatalf("second source lookup error = %v, want %v", err, wantErr)
	}
	if reads != 2 {
		t.Fatalf("source lookups = %d, want 2", reads)
	}
}

func TestLoad_RepeatResolution(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	main := module.Version{Path: "towner/mainmod", Version: "1.0.0"}

	// Separate resolutions must produce the same module versions.
	modules1, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("first Load failed: %v", err)
	}
	modules2, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("second Load failed: %v", err)
	}

	if len(modules1) != len(modules2) {
		t.Errorf("module counts differ: %d vs %d", len(modules1), len(modules2))
	}
	for i := range modules1 {
		if modules1[i].Path != modules2[i].Path || modules1[i].Version != modules2[i].Version {
			t.Errorf("module %d differs: %s@%s vs %s@%s",
				i, modules1[i].Path, modules1[i].Version,
				modules2[i].Path, modules2[i].Version)
		}
	}
}

func TestLoad_DiamondDeps(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	// diamond depends on depmod@1.0.0 and altdep@1.0.0
	// depmod@1.0.0 depends on leafmod@1.0.0
	// altdep@1.0.0 depends on leafmod@2.0.0
	// MVS should select leafmod@2.0.0 (max of 1.0.0 and 2.0.0)
	main := module.Version{Path: "towner/diamond", Version: "1.0.0"}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Expect 4 modules: diamond, depmod, altdep, leafmod
	for _, m := range modules {
		t.Logf("  %s@%s", m.Path, m.Version)
	}
	if len(modules) != 4 {
		for _, m := range modules {
			t.Logf("  %s@%s", m.Path, m.Version)
		}
		t.Fatalf("expected 4 modules, got %d", len(modules))
	}

	// First module must be the main module
	if modules[0].Path != "towner/diamond" {
		t.Errorf("modules[0].Path = %q, want %q", modules[0].Path, "towner/diamond")
	}

	// Verify leafmod was resolved to 2.0.0 (max version from diamond dependency)
	leafMod := findModule(modules, "towner/leafmod")
	if leafMod == nil {
		t.Fatal("missing towner/leafmod in build list")
	}
	if leafMod.Version != "2.0.0" {
		t.Errorf("leafmod version = %q, want %q (MVS should select max)", leafMod.Version, "2.0.0")
	}

	depMod := findModule(modules, "towner/depmod")
	if depMod == nil {
		t.Fatal("missing towner/depmod in build list")
	}
	// Deps now holds direct (declared) deps only, not MVS-selected versions.
	if !slices.Equal(depVersions(depMod), []module.Version{
		{Path: "towner/leafmod", Version: "1.0.0"},
	}) {
		t.Errorf("depmod deps mismatch, got: %+v", depVersions(depMod))
	}

	altDep := findModule(modules, "towner/altdep")
	if altDep == nil {
		t.Fatal("missing towner/altdep in build list")
	}
	if !slices.Equal(depVersions(altDep), []module.Version{
		{Path: "towner/leafmod", Version: "2.0.0"},
	}) {
		t.Errorf("altdep deps mismatch, got: %+v", depVersions(altDep))
	}
}

func TestLoad_DeepDiamondDeps_ResolvedSubgraph(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()

	// deepmain depends on deepb@1.0.0 and deepd@1.0.0.
	// deepb@1.0.0 depends on deepc@1.0.0 -> deepe@1.2.0
	// deepd@1.0.0 depends on deepc@1.1.0 -> deepe@1.3.0
	// MVS should select deepc@1.1.0 and deepe@1.3.0.
	main := module.Version{Path: "towner/deepmain", Version: "1.0.0"}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(modules) != 5 {
		for _, m := range modules {
			t.Logf("  %s@%s", m.Path, m.Version)
		}
		t.Fatalf("expected 5 modules, got %d", len(modules))
	}

	deepC := findModule(modules, "towner/deepc")
	if deepC == nil {
		t.Fatal("missing towner/deepc in build list")
	}
	if deepC.Version != "1.1.0" {
		t.Fatalf("deepc version = %q, want %q", deepC.Version, "1.1.0")
	}

	deepE := findModule(modules, "towner/deepe")
	if deepE == nil {
		t.Fatal("missing towner/deepe in build list")
	}
	if deepE.Version != "1.3.0" {
		t.Fatalf("deepe version = %q, want %q", deepE.Version, "1.3.0")
	}

	deepB := findModule(modules, "towner/deepb")
	if deepB == nil {
		t.Fatal("missing towner/deepb in build list")
	}
	// Deps now holds direct (declared) deps only.
	if !slices.Equal(depVersions(deepB), []module.Version{
		{Path: "towner/deepc", Version: "1.0.0"},
	}) {
		t.Errorf("deepb deps mismatch, got: %+v", depVersions(deepB))
	}

	deepD := findModule(modules, "towner/deepd")
	if deepD == nil {
		t.Fatal("missing towner/deepd in build list")
	}
	if !slices.Equal(depVersions(deepD), []module.Version{
		{Path: "towner/deepc", Version: "1.1.0"},
	}) {
		t.Errorf("deepd deps mismatch, got: %+v", depVersions(deepD))
	}
}

func TestLoad_ErrorDepFormulaNotFound(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	// depbad depends on baddep@1.0.0, but baddep has no formula files
	main := module.Version{Path: "towner/depbad", Version: "1.0.0"}

	_, err := Load(ctx, main, Options{FormulaStore: store})
	if err == nil {
		t.Error("expected error when dependency has no formula")
	}
}

// failingSyncRepo is a mock VCS repo that fails Sync for specific module paths.
type failingSyncRepo struct {
	failPaths map[string]bool
}

func (m *failingSyncRepo) Tags(ctx context.Context) ([]vcs.Tag, error) { return nil, nil }
func (m *failingSyncRepo) Latest(ctx context.Context) (string, error)  { return "", nil }
func (m *failingSyncRepo) Refs() vcs.Refs                              { return mockRefs{} }
func (m *failingSyncRepo) At(ref, localDir string) fs.FS               { return os.DirFS(localDir) }
func (m *failingSyncRepo) Sync(ctx context.Context, ref, path, localDir string) error {
	if m.failPaths[path] {
		return fmt.Errorf("sync failed for %s", path)
	}
	return nil
}

func TestLoad_ErrorModuleOfFails(t *testing.T) {
	// Use a custom VCS repo that fails Sync for "towner/baddep"
	tmpDir := t.TempDir()
	if err := os.CopyFS(tmpDir, os.DirFS("testdata/load")); err != nil {
		t.Fatalf("failed to copy testdata: %v", err)
	}
	store := repo.New(tmpDir, &failingSyncRepo{
		failPaths: map[string]bool{"towner/baddep": true},
	})
	ctx := context.Background()

	// depbad depends on baddep@1.0.0, Sync for baddep will fail
	main := module.Version{Path: "towner/depbad", Version: "1.0.0"}

	_, err := Load(ctx, main, Options{FormulaStore: store})
	if err == nil {
		t.Error("expected error when dependency module sync fails")
	}
}

func TestLoad_ErrorResolveDepsMainModule(t *testing.T) {
	store := setupTestStore(t, "testdata/load")
	ctx := context.Background()
	// brokenver has a valid formula but broken versions.json,
	// so resolveDeps will fail for the main module
	main := module.Version{Path: "towner/brokenver", Version: "1.0.0"}

	_, err := Load(ctx, main, Options{FormulaStore: store})
	if err == nil {
		t.Error("expected error when main module has broken versions.json")
	}
}

func TestLoad_ErrorMainModuleNotFound(t *testing.T) {
	// Use a custom VCS repo that fails for the main module itself
	tmpDir := t.TempDir()
	store := repo.New(tmpDir, &failingSyncRepo{
		failPaths: map[string]bool{"towner/nonexistent": true},
	})
	ctx := context.Background()

	main := module.Version{Path: "towner/nonexistent", Version: "1.0.0"}

	_, err := Load(ctx, main, Options{FormulaStore: store})
	if err == nil {
		t.Error("expected error when main module sync fails")
	}
}

// =============================================
// Integration tests (require network)
// =============================================

func TestIntegration_LoadCMakeListsDeps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Pin the context-hook formulas while llarmvp-formula/main remains
	// compatible with the currently released LLAR parser.
	const formulaRef = "2400bc64d30180ef3f6012823c8aec5701b4d015"

	tmpDir := t.TempDir()
	vcsRepo, err := vcs.NewRepo("github.com/MeteorsLiu/llarmvp-formula")
	if err != nil {
		t.Fatalf("failed to create vcs.Repo: %v", err)
	}

	store := repo.New(tmpDir, &pinnedRepo{Repo: vcsRepo, ref: formulaRef})
	ctx := context.Background()

	// Load MeteorsLiu/cmaketest@1.0.0 whose OnRequire reads CMakeLists.txt
	// from the source repo, parses find_package(testdep REQUIRED),
	// and maps it to towner/testdep with version resolved from versions.json.
	main := module.Version{Path: "MeteorsLiu/cmaketest", Version: "1.0.0"}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Expect 2 modules: cmaketest and testdep
	if len(modules) != 2 {
		for _, m := range modules {
			t.Logf("  %s@%s", m.Path, m.Version)
		}
		t.Fatalf("expected 2 modules, got %d", len(modules))
	}

	if modules[0].Path != "MeteorsLiu/cmaketest" {
		t.Errorf("main module = %q, want %q", modules[0].Path, "MeteorsLiu/cmaketest")
	}
	if modules[0].Version != "1.0.0" {
		t.Errorf("main version = %q, want %q", modules[0].Version, "1.0.0")
	}

	testdep := findModule(modules, "towner/testdep")
	if testdep == nil {
		t.Fatal("missing towner/testdep in build list (should be resolved from CMakeLists.txt find_package)")
	}
	if testdep.Version != "1.0.0" {
		t.Errorf("testdep version = %q, want %q", testdep.Version, "1.0.0")
	}

	// Verify main module deps
	if len(modules[0].Deps) != 1 {
		t.Errorf("main module deps count = %d, want 1", len(modules[0].Deps))
	} else if modules[0].Deps[0].Path != "towner/testdep" {
		t.Errorf("main module dep = %q, want %q", modules[0].Deps[0].Path, "towner/testdep")
	}

}

func TestIntegration_LoadFromRealRepo_Zlib(t *testing.T) {
	t.Skip("skipping: real formulas use autotools which is not yet implemented")

	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	vcsRepo, err := vcs.NewRepo("github.com/MeteorsLiu/llarmvp-formula")
	if err != nil {
		t.Fatalf("failed to create vcs.Repo: %v", err)
	}

	store := repo.New(tmpDir, vcsRepo)
	ctx := context.Background()

	// Load madler/zlib (no OnRequire deps, no transitive deps)
	main := module.Version{Path: "madler/zlib", Version: "1.3.1"}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(modules) < 1 {
		t.Fatal("expected at least 1 module")
	}
	if modules[0].Path != "madler/zlib" {
		t.Errorf("main module = %q, want %q", modules[0].Path, "madler/zlib")
	}
	if modules[0].Version != "1.3.1" {
		t.Errorf("main version = %q, want %q", modules[0].Version, "1.3.1")
	}

	t.Logf("loaded %d modules", len(modules))
	for _, m := range modules {
		t.Logf("  %s@%s", m.Path, m.Version)
	}
}

func TestIntegration_LoadWithDeps_Libpng(t *testing.T) {
	t.Skip("skipping: real formulas use autotools which is not yet implemented")

	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	vcsRepo, err := vcs.NewRepo("github.com/MeteorsLiu/llarmvp-formula")
	if err != nil {
		t.Fatalf("failed to create vcs.Repo: %v", err)
	}

	store := repo.New(tmpDir, vcsRepo)
	ctx := context.Background()

	// Load pnggroup/libpng which depends on madler/zlib via OnRequire
	main := module.Version{Path: "pnggroup/libpng", Version: "1.6.44"}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Expect at least 2 modules: libpng and zlib
	if len(modules) < 2 {
		t.Errorf("expected at least 2 modules, got %d", len(modules))
	}

	// Main module should be libpng
	if modules[0].Path != "pnggroup/libpng" {
		t.Errorf("main module = %q, want %q", modules[0].Path, "pnggroup/libpng")
	}

	// Verify zlib is in the build list
	var zlibFound bool
	for _, m := range modules {
		if m.Path == "madler/zlib" {
			zlibFound = true
			break
		}
	}
	if !zlibFound {
		t.Error("expected madler/zlib in build list")
	}

	// Verify main module's deps include zlib
	if len(modules[0].Deps) == 0 {
		t.Error("main module has no deps, expected at least zlib")
	}

	t.Logf("loaded %d modules:", len(modules))
	for _, m := range modules {
		t.Logf("  %s@%s (deps=%d)", m.Path, m.Version, len(m.Deps))
	}
}

func TestIntegration_LatestVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	cmp := func(v1, v2 module.Version) int {
		if v1.Version < v2.Version {
			return -1
		}
		if v1.Version > v2.Version {
			return 1
		}
		return 0
	}

	latestRepo, err := vcs.NewRepo("github.com/madler/zlib")
	if err != nil {
		t.Fatalf("create latest version repo failed: %v", err)
	}

	version, err := latestVersion(context.Background(), "madler/zlib", latestRepo, cmp)
	if err != nil {
		t.Fatalf("latestVersion failed: %v", err)
	}
	if version == "" {
		t.Error("latestVersion returned empty string")
	}
	t.Logf("latest version of madler/zlib: %s", version)
}

func TestIntegration_LoadWithEmptyVersion(t *testing.T) {
	t.Skip("skipping: real formulas use autotools which is not yet implemented")

	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	vcsRepo, err := vcs.NewRepo("github.com/MeteorsLiu/llarmvp-formula")
	if err != nil {
		t.Fatalf("failed to create vcs.Repo: %v", err)
	}

	store := repo.New(tmpDir, vcsRepo)
	ctx := context.Background()

	// Load with empty version - should resolve to latest
	main := module.Version{Path: "madler/zlib", Version: ""}

	modules, err := Load(ctx, main, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load with empty version failed: %v", err)
	}

	if len(modules) < 1 {
		t.Fatal("expected at least 1 module")
	}
	if modules[0].Path != "madler/zlib" {
		t.Errorf("main module = %q, want %q", modules[0].Path, "madler/zlib")
	}
	if modules[0].Version == "" {
		t.Error("resolved version is still empty")
	}
	t.Logf("resolved version: %s", modules[0].Version)
}

func TestIntegration_LoadTaglessRepositoryAtHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	const formulaRef = "57f92f7d1ac9673cd4c2e81d6d9777bd9340f7c9"
	ctx := context.Background()

	sourceRepo, err := vcs.NewRepo("github.com/rxi/log.c")
	if err != nil {
		t.Fatalf("create source repo failed: %v", err)
	}
	tags, err := sourceRepo.Tags(ctx)
	if err != nil {
		t.Fatalf("list source tags failed: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("source repository has tags %v, want no tags", tags)
	}
	head, err := sourceRepo.Latest(ctx)
	if err != nil {
		t.Fatalf("resolve source HEAD failed: %v", err)
	}

	formulaRepo, err := vcs.NewRepo("github.com/MeteorsLiu/llarhub")
	if err != nil {
		t.Fatalf("create formula repo failed: %v", err)
	}
	store := repo.New(t.TempDir(), &pinnedRepo{Repo: formulaRepo, ref: formulaRef})
	modules, err := Load(ctx, module.Version{Path: "rxi/log.c"}, Options{FormulaStore: store})
	if err != nil {
		t.Fatalf("Load tagless repository failed: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("loaded modules = %d, want 1", len(modules))
	}
	if modules[0].Version != head {
		t.Fatalf("resolved version = %q, want HEAD %q", modules[0].Version, head)
	}
}
