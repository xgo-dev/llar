package modules

import (
	"context"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"sync"
	"testing"

	"github.com/goplus/ixgo/xgobuild"
	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	"github.com/goplus/xgo/ast"
	"github.com/goplus/xgo/parser"
)

func TestNewFormulaModule(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON")

	if mod == nil {
		t.Fatal("newFormulaModule returned nil")
	}
	if mod.modPath != "DaveGamble/cJSON" {
		t.Errorf("modPath = %q, want %q", mod.modPath, "DaveGamble/cJSON")
	}
	if mod.formulas == nil {
		t.Error("formulas map is nil")
	}
	if mod.comparator == nil {
		t.Error("comparator should be initialized")
	}
}

func TestFormulaModule_Comparator(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON")

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

	// Second call should return cached comparator
	cmp2, err := mod.comparator()
	if err != nil {
		t.Fatalf("second comparator() failed: %v", err)
	}
	// Verify caching by checking the same function produces same results
	if cmp(v1, v2) != cmp2(v1, v2) {
		t.Error("cached comparator produces different results")
	}
}

func TestFormulaModule_ComparatorDefaultFallback(t *testing.T) {
	fsys := os.DirFS("testdata/madler/zlib")
	// madler/zlib has no comparator file, should use default
	mod := newFormulaModule(fsys, "madler/zlib")

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
	mod := newFormulaModule(fsys, "DaveGamble/cJSON")

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

	// The compiled formula is cached, while callers receive independent classes.
	f2, err := mod.at("v1.7.18")
	if err != nil {
		t.Fatalf("second at() failed: %v", err)
	}
	if f == f2 {
		t.Error("at() returned the cached formula class")
	}
	if len(mod.formulas) != 1 {
		t.Fatalf("cached formulas = %d, want 1", len(mod.formulas))
	}
}

func TestFormulaModule_AtReturnsIsolatedClasses(t *testing.T) {
	mod := newFormulaModule(os.DirFS("testdata/load/towner/targetreq"), "towner/targetreq")

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
			injectMatrix(f, classfile.Matrix{
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
	mod := newFormulaModule(fsys, "DaveGamble/cJSON")

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
	mod := newFormulaModule(fsys, "DaveGamble/cJSON")

	// Version lower than all fromVer should fail
	_, err := mod.at("v0.5.0")
	if err == nil {
		t.Error("at() should fail for version lower than all fromVer")
	}
}

func TestFormulaModule_AtNonexistentModule(t *testing.T) {
	fsys := os.DirFS("testdata/nonexistent")
	mod := newFormulaModule(fsys, "nonexistent/module")

	_, err := mod.at("1.0.0")
	if err == nil {
		t.Error("at() should fail for nonexistent module")
	}
}

func TestFormulaModule_FindMaxFromVer(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON")

	cmp, _ := mod.comparator()
	target := module.Version{Path: "DaveGamble/cJSON", Version: "v1.7.18"}

	fromVer, path, err := mod.findMaxFromVer(target, cmp)
	if err != nil {
		t.Fatalf("findMaxFromVer() failed: %v", err)
	}
	if fromVer != "v1.5.0" {
		t.Errorf("fromVer = %q, want %q", fromVer, "v1.5.0")
	}
	if path == "" {
		t.Error("path is empty")
	}
}

func TestFromVerOf(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON").(fs.ReadFileFS)

	tests := []struct {
		name        string
		path        string
		wantFromVer string
		wantErr     bool
	}{
		{
			name:        "cJSON 1.0.0",
			path:        "1.0.0/CJSON_llar.gox",
			wantFromVer: "v1.0.0",
		},
		{
			name:        "cJSON 1.5.0",
			path:        "1.5.0/CJSON_llar.gox",
			wantFromVer: "v1.5.0",
		},
		{
			name:        "cJSON 2.0.0",
			path:        "2.0.0/CJSON_llar.gox",
			wantFromVer: "v2.0.0",
		},
		{
			name:    "nonexistent file",
			path:    "nonexistent/formula_llar.gox",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fromVerOf(fsys, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("fromVerOf() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantFromVer {
				t.Errorf("fromVerOf() = %q, want %q", got, tt.wantFromVer)
			}
		})
	}
}

func TestFromVerFrom(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		wantFromVer string
		wantErr     bool
	}{
		{
			name: "valid fromVer call",
			source: `
id "test/pkg"
fromVer "1.2.3"
`,
			wantFromVer: "1.2.3",
		},
		{
			name:        "fromVer with backticks",
			source:      "id `test/pkg`\nfromVer `2.0.0`\n",
			wantFromVer: "2.0.0",
		},
		{
			name: "no fromVer call",
			source: `
id "test/pkg"
onBuild (ctx, proj, out) => {
    echo "hello"
}
`,
			wantErr: true,
		},
		{
			name:    "empty source",
			source:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			astFile, err := parser.ParseEntry(fset, "test_llar.gox", []byte(tt.source), parser.Config{
				ClassKind: xgobuild.ClassKind,
			})
			if err != nil {
				t.Fatalf("failed to parse source: %v", err)
			}

			got, err := fromVerFrom(astFile)
			if (err != nil) != tt.wantErr {
				t.Errorf("fromVerFrom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantFromVer {
				t.Errorf("fromVerFrom() = %q, want %q", got, tt.wantFromVer)
			}
		})
	}
}

func TestParseCallArg(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		fnName  string
		want    string
		wantErr bool
	}{
		{
			name:   "string literal with double quotes",
			source: `fromVer "1.0.0"`,
			fnName: "fromVer",
			want:   "1.0.0",
		},
		{
			name:   "string literal with backticks",
			source: "fromVer `2.0.0`",
			fnName: "fromVer",
			want:   "2.0.0",
		},
		{
			name:    "empty argument",
			source:  `fromVer ""`,
			fnName:  "fromVer",
			want:    "",
			wantErr: true,
		},
		{
			name:   "id function call",
			source: `id "test/pkg"`,
			fnName: "id",
			want:   "test/pkg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			astFile, err := parser.ParseEntry(fset, "test_llar.gox", []byte(tt.source), parser.Config{
				ClassKind: xgobuild.ClassKind,
			})
			if err != nil {
				t.Fatalf("failed to parse source: %v", err)
			}

			var callExpr *ast.CallExpr
			ast.Inspect(astFile, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if ident, ok := c.Fun.(*ast.Ident); ok && ident.Name == tt.fnName {
						callExpr = c
						return false
					}
				}
				return true
			})

			if callExpr == nil {
				t.Fatalf("failed to find %s call in AST", tt.fnName)
			}

			got, err := parseCallArg(callExpr, tt.fnName)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCallArg() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseCallArg() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCallArg_NoArgument(t *testing.T) {
	callExpr := &ast.CallExpr{
		Fun:  &ast.Ident{Name: "testFunc"},
		Args: []ast.Expr{},
	}

	_, err := parseCallArg(callExpr, "testFunc")
	if err == nil {
		t.Error("parseCallArg() expected error for no arguments")
	}
}

func TestParseCallArg_NonStringArg(t *testing.T) {
	callExpr := &ast.CallExpr{
		Fun: &ast.Ident{Name: "testFunc"},
		Args: []ast.Expr{
			&ast.Ident{Name: "someVariable"},
		},
	}

	_, err := parseCallArg(callExpr, "testFunc")
	if err == nil {
		t.Error("parseCallArg() expected error for non-string argument")
	}
}

func TestIntegration_FormulaModuleToFormula(t *testing.T) {
	fsys := os.DirFS("testdata/DaveGamble/cJSON")
	mod := newFormulaModule(fsys, "DaveGamble/cJSON")

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
		mod := newFormulaModule(fsys, m.path)

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

	mod := newFormulaModule(fsys, "madler/zlib")

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
	fromVer, formulaPath, err := mod.findMaxFromVer(target, cmp)
	if err != nil {
		t.Fatalf("findMaxFromVer() failed: %v", err)
	}

	if fromVer == "" {
		t.Error("fromVer is empty")
	}
	if formulaPath == "" {
		t.Error("formulaPath is empty")
	}
	t.Logf("found formula: fromVer=%s, path=%s", fromVer, formulaPath)
}
