package modules

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/goplus/llar/build/formula"
	"github.com/goplus/llar/build/formula/repo"
	"github.com/goplus/llar/build/internal/mvs"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	"github.com/goplus/llar/mod/versions"

	classfile "github.com/goplus/llar/build/runtime/formula"
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

// Module describes a module version and its dependency information.
type Module struct {
	Path    string
	Version string

	// FS is rooted at the selected formula's directory, including its resources.
	FS fs.FS

	// Deps contains all other selected modules for the main module.
	// For other modules, it contains their direct requirements, whose
	// versions may differ from the MVS-selected versions in the build list.
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
		return repo.Latest(ctx)
	}
	max := slices.MaxFunc(tags, func(a, b vcs.Tag) int {
		return comparator(module.Version{modPath, a.Name}, module.Version{modPath, b.Name})
	})
	return max.Name, nil
}

// formulaContext supplies formula sources and matrix selections during Load.
type formulaContext struct {
	moduleFS func(ctx context.Context, modPath string) (fs.FS, error)
	matrix   classfile.Matrix
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

// moduleOf obtains the formula source for the given module path.
func (c *formulaContext) moduleOf(ctx context.Context, modPath string) (*formulaModule, error) {
	// ModuleFS always fetches the formula from the latest commit.
	fs, err := c.moduleFS(ctx, modPath)
	if err != nil {
		return nil, err
	}
	// TODO(MeteorsLiu): Support different code host sites.
	repo, err := vcs.NewRepo(fmt.Sprintf("github.com/%s", modPath))
	if err != nil {
		return nil, err
	}
	return newFormulaModule(fs, modPath, c.matrix, repo), nil
}

// loadDeps returns the dependencies and selected formula directory for a version.
func (c *formulaContext) loadDeps(ctx context.Context, mod module.Version) ([]module.Version, fs.FS, error) {
	thisMod, err := c.moduleOf(ctx, mod.Path)
	if err != nil {
		return nil, nil, err
	}
	f, err := thisMod.at(mod.Version)
	if err != nil {
		return nil, nil, err
	}
	deps, err := resolveDeps(mod, thisMod.fsys.(fs.ReadFileFS), f, thisMod.repo)
	if err != nil {
		return nil, nil, err
	}
	return deps, f.FS, nil
}

// convertToModules constructs records using formula directories captured during resolution.
func (*formulaContext) convertToModules(modList []module.Version, sources map[module.Version]fs.FS) []*Module {
	var modules []*Module

	for _, mod := range modList {
		module := &Module{
			Path:    mod.Path,
			Version: mod.Version,
			FS:      sources[mod],
		}
		modules = append(modules, module)
	}

	return modules
}

// Load resolves the main module and its dependencies using MVS. It returns
// module versions, dependency edges and selected formula directories, with the
// main module first. A returned FS can be passed directly to formula.LoadFS(fs, ".").
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
		latest, err := latestVersion(ctx, main.Path, mainMod.repo, cmp)
		if err != nil {
			return nil, err
		}
		main.Version = latest
	}
	mainFormula, err := mainMod.at(main.Version)
	if err != nil {
		return nil, err
	}
	mainDeps, err := resolveDeps(main, mainMod.fsys.(fs.ReadFileFS), mainFormula, mainMod.repo)
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

	var graphMu sync.Mutex
	graph := mvs.NewGraph(cmp, mainDeps)
	sources := map[module.Version]fs.FS{main: mainFormula.FS}

	onLoad := func(mod module.Version) ([]module.Version, error) {
		deps, source, err := context.loadDeps(ctx, mod)
		if err != nil {
			return nil, err
		}
		graphMu.Lock()
		graph.Require(mod, deps)
		sources[mod] = source
		graphMu.Unlock()
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

	modules := context.convertToModules(buildList, sources)

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
			deps = context.convertToModules(reqs, sources)
		}
		mod.Deps = deps
	}

	return modules, nil
}

func runFormulaHook(fn func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if panicErr, ok := recovered.(error); ok {
				err = panicErr
				return
			}
			err = fmt.Errorf("formula hook panic: %v", recovered)
		}
	}()
	fn()
	return nil
}

// resolveDeps resolves the dependencies for a formula.
// It first tries to get dependencies from the OnRequire callback,
// then falls back to parsing versions.json if no dependencies are found.
func resolveDeps(mod module.Version, modFS fs.ReadFileFS, frla *formula.Formula, repo vcs.Repo) ([]module.Version, error) {
	if err := validateModulePath(mod.Path); err != nil {
		return nil, err
	}

	if frla.Filter != nil {
		var supported bool
		if err := runFormulaHook(func() {
			supported = frla.Filter()
		}); err != nil {
			return nil, fmt.Errorf("filter failed for %s@%s: %w", mod.Path, mod.Version, err)
		}
		if !supported {
			return nil, fmt.Errorf("%s@%s does not support selected matrix", mod.Path, mod.Version)
		}
	}

	var deps classfile.ModuleDeps

	// onRequire is optional
	if frla.OnRequire != nil {
		// TODO(MeteorsLiu): Design source cache dir
		// In the most common case, onRequire only read one file like CMakelist.txt, etc.
		// So missing cache here is acceptable.
		tmpSourceDir, err := os.MkdirTemp("", fmt.Sprintf("source-%s-%s*", strings.ReplaceAll(mod.Path, "/", "-"), strings.ReplaceAll(mod.Version, "/", "-")))
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpSourceDir)

		repoFS := repo.At(mod.Version, tmpSourceDir)
		proj := &classfile.Project{
			SourceFS: repoFS.(fs.ReadFileFS),
		}
		if err := runFormulaHook(func() {
			frla.OnRequire(proj, &deps)
		}); err != nil {
			return nil, fmt.Errorf("onRequire failed for %s@%s: %w", mod.Path, mod.Version, err)
		}
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
