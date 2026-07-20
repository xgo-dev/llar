package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/execbroker"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
)

type Builder struct {
	store        repo.Store
	matrix       string
	runTest      bool
	stdout       io.Writer
	stderr       io.Writer
	workspaceDir string
	cache        cache.Cache
	newRepo      func(repoPath string) (vcs.Repo, error) // defaults to vcs.NewRepo
}

type Result struct {
	Metadata  string
	OutputDir string
}

type Options struct {
	Store     repo.Store
	MatrixStr string
	// RunTest, when true, causes Build to invoke OnTest on the root target
	// after OnBuild (or after reusing cached build metadata). The build
	// cache is consulted as usual: on a cache hit the root's OnBuild is
	// skipped and OnTest runs against the cached artifacts; on a cache
	// miss OnBuild runs and the fresh metadata is cached before OnTest.
	// Transitive dependencies honor the cache normally and do not have
	// their OnTest hooks triggered.
	RunTest      bool
	Stdout       io.Writer
	Stderr       io.Writer
	WorkspaceDir string
	Cache        cache.Cache
}

func defaultWorkspaceDir() (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	workspaceDir := filepath.Join(userCacheDir, ".llar", "workspaces")

	if err := os.MkdirAll(workspaceDir, 0700); err != nil {
		return "", err
	}
	return workspaceDir, nil
}

// NewBuilder creates a new Builder.
func NewBuilder(opts Options) (*Builder, error) {
	workspaceDir := opts.WorkspaceDir
	if workspaceDir == "" {
		var err error
		workspaceDir, err = defaultWorkspaceDir()
		if err != nil {
			return nil, err
		}
	}
	c := opts.Cache
	if c == nil {
		c = &localCache{workspaceDir: workspaceDir}
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	return &Builder{
		store:        opts.Store,
		matrix:       opts.MatrixStr,
		runTest:      opts.RunTest,
		stdout:       stdout,
		stderr:       stderr,
		workspaceDir: workspaceDir,
		cache:        c,
		newRepo:      vcs.NewRepo,
	}, nil
}

// constructBuildList reorders the MVS build list into a valid build order
// using DFS post-order traversal: leaves (modules with no deps) come first,
// the main module (root) comes last.
//
// This method lives in the build module (rather than modules) because build
// ordering may change in the future, e.g. to support parallel builds.
//
// Example:
//
//	Graph: A -> B -> C, A -> D -> C
//	Input  (MVS BuildList): [A@1.0.0, B@1.2.0, C@1.2.0, D@1.0.0]
//	Output (build order):   [C@1.2.0, B@1.2.0, D@1.0.0, A@1.0.0]
func (b *Builder) constructBuildList(targets []*modules.Module) []*modules.Module {
	byPath := make(map[string]*modules.Module, len(targets))
	for _, m := range targets {
		byPath[m.Path] = m
	}

	var order []*modules.Module
	visited := make(map[string]bool, len(targets))

	var visit func(m *modules.Module)
	visit = func(m *modules.Module) {
		if visited[m.Path] {
			return
		}
		visited[m.Path] = true
		for _, dep := range m.Deps {
			if d, ok := byPath[dep.Path]; ok {
				visit(d)
			}
		}
		order = append(order, m)
	}

	if len(targets) > 0 {
		visit(targets[0])
	}

	return order
}

// resolveModTransitiveDeps collects all transitive dependencies of mod from
// the MVS build list and returns them in build order (DFS post-order: leaves first).
//
// modules.Module.Deps only stores direct dependencies so that the build module
// can reorder freely. This method reconstructs the full transitive set by
// walking the dependency graph through targets (which use MVS-selected versions).
//
// Case 1 - Simple:
//
//	Graph:  A -> B -> C -> D
//	Input:  targets=[A@1.0.0, B@1.2.0, C@1.2.0, D@1.0.0], mod=C@1.2.0
//	Output: [D@1.0.0]
//
// Case 2 - Diamond (MVS version selection):
//
//	Graph:  A -> B -> C, A -> D -> C   (MVS selects C@2.0.0)
//	Input:  targets=[A@1.0.0, B@1.2.0, C@2.0.0, D@1.0.0], mod=B@1.2.0
//	Output: [C@2.0.0]
//
// Case 3 - Diamond with transitive dep:
//
//	Graph:  A -> B -> C, A -> D -> C -> E   (MVS selects C@2.0.0)
//	Input:  targets=[A@1.0.0, B@1.2.0, C@2.0.0, D@1.0.0, E@1.0.0], mod=B@1.2.0
//	Output: [E@1.0.0, C@2.0.0]
//
// Case 4 - Multiple direct deps (alphabet order):
//
//	Graph:  A -> B -> C, A -> B -> D
//	Input:  targets=[A@1.0.0, B@1.2.0, C@1.1.0, D@1.0.0], mod=B@1.2.0
//	Output: [C@1.1.0, D@1.0.0]
//
// Case 5 - Dep ordering by topology:
//
//	Graph:  A -> B -> C -> D, A -> B -> D
//	Input:  targets=[A@1.0.0, B@1.2.0, C@1.1.0, D@1.2.0], mod=B@1.2.0
//	Output: [D@1.2.0, C@1.1.0]  (D before C because B depends on both D and C directly, while C depends on D transitively)
func (b *Builder) resolveModTransitiveDeps(targets []*modules.Module, mod *modules.Module) []module.Version {
	byPath := make(map[string]*modules.Module, len(targets))
	for _, m := range targets {
		byPath[m.Path] = m
	}

	var order []module.Version
	visited := make(map[string]bool)
	visited[mod.Path] = true

	var visit func(m *modules.Module)
	visit = func(m *modules.Module) {
		if visited[m.Path] {
			return
		}
		visited[m.Path] = true
		for _, dep := range m.Deps {
			if d, ok := byPath[dep.Path]; ok {
				visit(d)
			}
		}
		order = append(order, module.Version{Path: m.Path, Version: m.Version})
	}

	for _, dep := range mod.Deps {
		if d, ok := byPath[dep.Path]; ok {
			visit(d)
		}
	}

	return order
}

func (b *Builder) Build(ctx context.Context, targets []*modules.Module) ([]Result, error) {
	builtResults := make(map[module.Version]classfile.BuildResult)

	// Identify the root target. By MVS convention (see constructBuildList
	// and modules.Load), targets[0] is the main module requested by the
	// caller; runTest semantics (fresh build + OnTest invocation) only
	// apply to it.
	//
	// Root identity is tracked by (Path, Version) rather than pointer
	// equality so the comparison survives any future refactor of
	// constructBuildList that stops reusing *modules.Module pointers
	// (e.g. parallel builds that clone module structs). The pair is
	// unique in an MVS build list, so it is a safe identity key.
	var rootID module.Version
	if len(targets) > 0 {
		rootID = module.Version{Path: targets[0].Path, Version: targets[0].Version}
	}

	build := func(mod *modules.Module) (Result, error) {
		isRoot := mod.Path == rootID.Path && mod.Version == rootID.Version
		testThisMod := b.runTest && isRoot && mod.OnTest != nil

		installDir, err := b.installDir(mod.Path, mod.Version)
		if err != nil {
			return Result{}, err
		}
		deps := b.resolveModTransitiveDeps(targets, mod)
		modVer := module.Version{Path: mod.Path, Version: mod.Version}

		// Consult the build cache. A hit means we already have the
		// module's build metadata and its installDir is populated from a
		// previous successful build.
		entry, cacheHit, err := b.cache.Get(ctx, cache.Key{Module: modVer, Matrix: b.matrix})
		if err != nil {
			return Result{}, err
		}

		// Fast path: cache hit and no OnTest to run. Skip source clone
		// and OnBuild entirely.
		if cacheHit && !testThisMod {
			return Result{Metadata: entry.Metadata, OutputDir: installDir}, nil
		}

		// At this point we need to run OnBuild, OnTest, or both. All of
		// them expect a source checkout and a prepared build context, so
		// set those up uniformly regardless of cache state.

		// TODO(MeteorsLiu): Source cache dir (belongs in the vcs layer)
		tmpSourceDir, err := os.MkdirTemp("", fmt.Sprintf("source-%s-%s*", strings.ReplaceAll(mod.Path, "/", "-"), mod.Version))
		if err != nil {
			return Result{}, err
		}
		defer os.RemoveAll(tmpSourceDir)

		// Before we start to build, clone source to tmpSourceDir.
		// TODO(MeteorsLiu): Support different code host
		repo, err := b.newRepo(fmt.Sprintf("github.com/%s", mod.Path))
		if err != nil {
			return Result{}, err
		}
		if err := repo.Sync(ctx, mod.Version, "", tmpSourceDir); err != nil {
			return Result{}, err
		}

		if err := os.MkdirAll(installDir, 0o755); err != nil {
			return Result{}, err
		}

		getOutputDir := func(_ string, m module.Version) (string, error) {
			return b.installDir(m.Path, m.Version)
		}
		buildContext := classfile.NewContext(tmpSourceDir, installDir, b.matrix, getOutputDir)

		// Inject results of already-built dependencies
		for modVer, result := range builtResults {
			buildContext.AddBuildResult(modVer, result)
		}

		project := &classfile.Project{Deps: deps, SourceFS: mod.FS.(fs.ReadFileFS)}

		var metadata string
		if err := execbroker.Do(execbroker.Scope{
			Dir:    tmpSourceDir,
			Stdin:  os.Stdin,
			Stdout: b.stdout,
			Stderr: b.stderr,
		}, func() error {
			// Run OnBuild only on cache miss; reuse cached metadata otherwise.
			if cacheHit {
				metadata = entry.Metadata
			} else {
				var out classfile.BuildResult
				mod.OnBuild(buildContext, project, &out)
				if len(out.Errs()) > 0 {
					return errors.Join(out.Errs()...)
				}
				metadata = out.Metadata()
			}

			// Run OnTest (root only) against the just-built or cached
			// artifacts, reusing the same build context so tests see a
			// consistent environment either way.
			if testThisMod {
				var testOut classfile.TestResult
				mod.OnTest(buildContext, project, &testOut)
				if len(testOut.Errs()) > 0 {
					return fmt.Errorf("onTest failed for %s@%s: %w", mod.Path, mod.Version, errors.Join(testOut.Errs()...))
				}
			}
			return nil
		}); err != nil {
			return Result{}, err
		}

		// Save cache only on cache miss. A cache hit means the entry is
		// already present and current; OnTest does not modify metadata.
		if !cacheHit {
			entry, err := b.cache.Put(ctx, cache.Key{Module: modVer, Matrix: b.matrix}, os.DirFS(installDir), cache.Entry{
				Metadata: metadata,
				Deps:     deps,
			})
			if err != nil {
				return Result{}, err
			}
			metadata = entry.Metadata
		}

		return Result{Metadata: metadata, OutputDir: installDir}, nil
	}

	var results []Result

	buildList := b.constructBuildList(targets)
	lockPaths := make([]string, 0, len(buildList))
	for _, target := range buildList {
		lockPaths = append(lockPaths, target.Path)
	}
	// A dependent keeps reading its dependencies' install directories after
	// their own build steps return, so hold the whole graph until Build completes.
	//
	// Case 1 - Disjoint graphs:
	// Request A builds libpng -> zlib and request B builds curl -> openssl.
	// They lock different module paths and remain parallel.
	//
	// Case 2 - Overlapping graphs:
	// Request A builds libpng -> zlib and request B builds freetype -> zlib.
	// Because both graphs contain zlib, the later request waits for the earlier
	// Build to finish, then reuses its published zlib artifact instead of observing
	// a replaced install tree.
	//
	// Lock ordering:
	// Use a stable order so overlapping graphs cannot deadlock. For example,
	// X -> Y produces build order [Y, X], while another matrix with Y -> X produces
	// [X, Y]. Locking in build order can leave each request holding one lock and
	// waiting for the other; sorting makes both lock [X, Y].
	sort.Strings(lockPaths)
	unlocks := make([]func(), 0, len(lockPaths))
	for _, path := range lockPaths {
		unlock, err := b.store.LockModule(path)
		if err != nil {
			for i := len(unlocks) - 1; i >= 0; i-- {
				unlocks[i]()
			}
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()

	// Save current environment and restore it after OnBuild,
	// that's because OnBuild may break environment
	// TODO(MeteorsLiu): Switch to sandbox to run OnBuild
	savedEnv := os.Environ()
	defer func() {
		os.Clearenv()
		for _, env := range savedEnv {
			k, v, _ := strings.Cut(env, "=")
			os.Setenv(k, v)
		}
	}()

	// TODO(MeteorsLiu): Parallel build
	for _, target := range buildList {
		result, err := build(target)
		if err != nil {
			return nil, err
		}

		// Track result for downstream dependencies
		modVer := module.Version{Path: target.Path, Version: target.Version}
		br := classfile.BuildResult{}
		if result.Metadata != "" {
			br.SetMetadata(result.Metadata)
		}
		builtResults[modVer] = br

		results = append(results, result)
	}
	return results, nil
}
