package modules

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/goplus/llar/internal/formula"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/mvs"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	"github.com/goplus/llar/mod/versions"

	classfile "github.com/goplus/llar/formula"
)

func validateModulePath(modPath string) error {
	if modPath == "" {
		return fmt.Errorf("invalid module path %q: path is empty", modPath)
	}
	if modPath == "." || strings.HasPrefix(modPath, "./") {
		return fmt.Errorf("invalid module path %q: local path pattern is not supported", modPath)
	}
	if strings.Contains(modPath, "...") {
		return fmt.Errorf("invalid module path %q: \"...\" wildcard is not supported", modPath)
	}
	for _, part := range strings.Split(modPath, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("invalid module path %q: path traversal is not supported", modPath)
		}
	}
	if _, err := module.EscapePath(modPath); err != nil {
		return fmt.Errorf("invalid module path %q: %w", modPath, err)
	}
	return nil
}

// Module represents a loaded module with its formula, filesystem, and resolved dependencies.
type Module struct {
	*formula.Formula

	FS      fs.FS
	Path    string
	Version string

	// Deps holds direct dependencies only (not transitive).
	// For the main module, Deps contains all modules in the build list.
	// For non-main modules, Deps contains only the declared dependencies
	// from versions.json. The build module can reconstruct the full
	// dependency graph from these adjacency edges.
	Deps []*Module
}

// Options contains options for Load.
type Options struct {
	// FormulaStore is the store for downloading and caching formulas.
	FormulaStore repo.Store
	Matrix       classfile.Matrix
}

func latestVersion(ctx context.Context, modPath string, repo vcs.Repo, comparator func(v1, v2 module.Version) int) (version string, err error) {
	tags, err := repo.Tags(ctx)
	if err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("failed to retrieve the latest version: no tags found")
	}
	max := slices.MaxFunc(tags, func(a, b string) int {
		return comparator(module.Version{modPath, a}, module.Version{modPath, b})
	})
	return max, nil
}

// formulaContext groups helper functions used throughout the Load process,
// sharing a module cache and a filesystem provider across all steps.
type formulaContext struct {
	moduleCache sync.Map
	moduleFS    func(ctx context.Context, modPath string) (fs.FS, error)
	matrix      classfile.Matrix
}

func newFormulaContext(moduleFS func(ctx context.Context, modPath string) (fs.FS, error), matrix classfile.Matrix) *formulaContext {
	return &formulaContext{moduleFS: moduleFS, matrix: matrix}
}

// compareModuleVersion compares two versions of the same module path
// using the module's formula-defined comparator.
func (c *formulaContext) compareModuleVersion(ctx context.Context, p, v1, v2 string) int {
	mod, err := c.moduleOf(ctx, p)
	if err != nil {
		// should not have errors
		panic(err)
	}
	compare, err := mod.comparator()
	if err != nil {
		// should not have errors
		panic(err)
	}
	return compare(module.Version{p, v1}, module.Version{p, v2})
}

// moduleOf returns the formulaModule for the given path, fetching and caching it if needed.
func (c *formulaContext) moduleOf(ctx context.Context, modPath string) (*formulaModule, error) {
	if fm, ok := c.moduleCache.Load(modPath); ok {
		return fm.(*formulaModule), nil
	}
	// ModuleFS always fetches the formula from the latest commit.
	fs, err := c.moduleFS(ctx, modPath)
	if err != nil {
		return nil, err
	}
	fm := newFormulaModule(fs, modPath)
	actual, _ := c.moduleCache.LoadOrStore(modPath, fm)
	return actual.(*formulaModule), nil
}

// loadDeps loads the declared dependencies for a specific module version.
func (c *formulaContext) loadDeps(ctx context.Context, mod module.Version) (deps []module.Version, err error) {
	thisMod, err := c.moduleOf(ctx, mod.Path)
	if err != nil {
		return nil, err
	}
	f, err := thisMod.at(mod.Version)
	if err != nil {
		return nil, err
	}
	return resolveDeps(mod, thisMod.fsys.(fs.ReadFileFS), f, c.matrix)
}

// convertToModules converts a list of module.Version into loaded Module structs.
func (c *formulaContext) convertToModules(ctx context.Context, modList []module.Version) ([]*Module, error) {
	var modules []*Module

	for _, mod := range modList {
		thisMod, err := c.moduleOf(ctx, mod.Path)
		if err != nil {
			return nil, err
		}
		f, err := thisMod.at(mod.Version)
		if err != nil {
			return nil, err
		}
		module := &Module{
			Formula: f,
			FS:      thisMod.fsys,
			Path:    mod.Path,
			Version: mod.Version,
		}
		modules = append(modules, module)
	}

	return modules, nil
}

// Load loads all packages required by the main module and resolves
// their dependencies using the MVS algorithm. It returns modules for all
// packages in the computed build list.
func Load(ctx context.Context, main module.Version, opts Options) ([]*Module, error) {
	if err := validateModulePath(main.Path); err != nil {
		return nil, err
	}

	context := newFormulaContext(opts.FormulaStore.ModuleFS, opts.Matrix)

	mainMod, err := context.moduleOf(ctx, main.Path)
	if err != nil {
		return nil, err
	}
	if main.Version == "" {
		cmp, err := mainMod.comparator()
		if err != nil {
			return nil, err
		}
		// TODO(MeteorsLiu): Support different code host sites
		latestRepo, err := vcs.NewRepo(fmt.Sprintf("github.com/%s", main.Path))
		if err != nil {
			return nil, err
		}
		latest, err := latestVersion(ctx, main.Path, latestRepo, cmp)
		if err != nil {
			return nil, err
		}
		main.Version = latest
	}
	mainFormula, err := mainMod.at(main.Version)
	if err != nil {
		return nil, err
	}
	mainDeps, err := resolveDeps(main, mainMod.fsys.(fs.ReadFileFS), mainFormula, context.matrix)
	if err != nil {
		return nil, err
	}
	cmp := func(p, v1, v2 string) int {
		// none is an internal version for MVS, which means the smallest
		if v1 == "none" && v2 != "none" {
			return -1
		} else if v1 != "none" && v2 == "none" {
			return +1
		} else if v1 == "none" && v2 == "none" {
			return 0
		}
		return context.compareModuleVersion(ctx, p, v1, v2)
	}

	var depCache sync.Map
	var graphMu sync.Mutex
	graph := mvs.NewGraph(cmp, mainDeps)

	onLoad := func(mod module.Version) ([]module.Version, error) {
		if deps, ok := depCache.Load(mod); ok {
			return deps.([]module.Version), nil
		}
		deps, err := context.loadDeps(ctx, mod)
		if err != nil {
			return nil, err
		}
		graphMu.Lock()
		graph.Require(mod, deps)
		graphMu.Unlock()

		depCache.Store(mod, deps)
		return deps, nil
	}

	reqs := &mvsReqs{
		roots: mainDeps,
		isMain: func(v module.Version) bool {
			return v.Path == main.Path && v.Version == main.Version
		},
		cmp:    cmp,
		onLoad: onLoad,
	}

	buildList, err := mvs.BuildList([]module.Version{main}, reqs)
	if err != nil {
		return nil, err
	}

	modules, err := context.convertToModules(ctx, buildList)
	if err != nil {
		return nil, err
	}

	// fill the deps
	for _, mod := range modules {
		var deps []*Module

		if mod.Path == main.Path && mod.Version == main.Version {
			deps = modules[1:]
		} else {
			// only direct deps, this is because we don't know how to build
			// so only keep necessary information to allow build compute the dependencies more flexibly
			// However, the Deps in `project` must contain all dependencies,
			// we will resolve all transitive dependencies in the build module.
			reqs, _ := graph.RequiredBy(module.Version{mod.Path, mod.Version})
			deps, err = context.convertToModules(ctx, reqs)
			if err != nil {
				return nil, err
			}
		}
		mod.Deps = deps
	}

	return modules, nil
}

// resolveDeps resolves the dependencies for a formula.
// It first tries to get dependencies from the OnRequire callback,
// then falls back to parsing versions.json if no dependencies are found.
func resolveDeps(mod module.Version, modFS fs.ReadFileFS, frla *formula.Formula, matrix classfile.Matrix) ([]module.Version, error) {
	if err := validateModulePath(mod.Path); err != nil {
		return nil, err
	}

	// XGo formulas read the selected matrix through target.require/options.
	// Inject before filter and onRequire so both hooks see the same target.
	injectMatrix(frla, matrix)
	if frla.Filter != nil && !frla.Filter() {
		return nil, fmt.Errorf("%s@%s does not support selected matrix", mod.Path, mod.Version)
	}

	var deps classfile.ModuleDeps

	// TODO(MeteorsLiu): Support different code host sites.
	repo, err := vcs.NewRepo(fmt.Sprintf("github.com/%s", mod.Path))
	if err != nil {
		return nil, err
	}
	// onRequire is optional
	if frla.OnRequire != nil {
		// TODO(MeteorsLiu): Design source cache dir
		// In the most common case, onRequire only read one file like CMakelist.txt, etc.
		// So missing cache here is acceptable.
		tmpSourceDir, err := os.MkdirTemp("", fmt.Sprintf("source-%s-%s*", strings.ReplaceAll(mod.Path, "/", "-"), mod.Version))
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpSourceDir)

		repoFS := repo.At(mod.Version, tmpSourceDir)
		proj := &classfile.Project{
			SourceFS: repoFS.(fs.ReadFileFS),
		}
		frla.OnRequire(proj, &deps)
	}

	content, err := modFS.ReadFile("versions.json")
	if err != nil {
		return nil, err
	}
	depTable, err := versions.Parse("", content)
	if err != nil {
		return nil, err
	}
	versionedDeps := depTable.Dependencies[mod.Version]

	var vers []module.Version

	// Reconcile onRequire deps with versions.json: fill in missing versions
	// from the pinned table; unknown deps are safe to skip since MVS resolves
	// them recursively through other paths in the dependency graph.
	for _, dep := range deps.Deps() {
		if err := validateModulePath(dep.Path); err != nil {
			return nil, err
		}
		if dep.Version == "" {
			// if a version of a dep input by onRequire is empty, try our best to resolve it.
			idx := slices.IndexFunc(versionedDeps, func(depInTable module.Version) bool {
				return depInTable.Path == dep.Path
			})
			if idx < 0 {
				// It seems safe to drop deps here, because we resolve deps recursively and finally we will find that dep.
				continue
			}
			dep.Version = versionedDeps[idx].Version
		}

		vers = append(vers, module.Version{
			Path:    dep.Path,
			Version: dep.Version,
		})
	}

	if len(vers) > 0 {
		return vers, nil
	}

	for _, dep := range versionedDeps {
		if err := validateModulePath(dep.Path); err != nil {
			return nil, err
		}
		if dep.Version != "" {
			vers = append(vers, module.Version{
				Path:    dep.Path,
				Version: dep.Version,
			})
		}
	}

	return vers, nil
}
