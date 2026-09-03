package internal

import (
	"context"
	"fmt"
	stdbuild "go/build"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/internal/crosscompile"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/internal/modules/modlocal"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	"github.com/spf13/cobra"
)

var makeVerbose bool
var makeOutput string
var makeJSON bool

// newRemoteStore creates the remote formula store. Overridable for testing.
var newRemoteStore = func() (repo.Store, error) {
	formulaDir, err := repo.DefaultDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get formula dir: %w", err)
	}
	formulaRepo, err := vcs.NewRepo("github.com/goplus/llarhub")
	if err != nil {
		return nil, err
	}
	return repo.New(formulaDir, formulaRepo), nil
}

var makeCmd = &cobra.Command{
	Use:                "make [module@version]",
	Short:              "Build a module from source",
	Long:               `Make resolves a module and its dependencies, then builds the selected matrix from source using LLAR formulas.`,
	Args:               cobra.ExactArgs(1),
	FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	RunE:               runMake,
}

func init() {
	makeCmd.Flags().BoolVarP(&makeVerbose, "verbose", "v", false, "Enable verbose build output")
	makeCmd.Flags().StringVarP(&makeOutput, "output", "o", "", "Output path (directory, .zip file, or .tar.gz file)")
	makeCmd.Flags().BoolVarP(&makeJSON, "json", "j", false, "Print module result as JSON")
	rootCmd.AddCommand(makeCmd)
}

func runMake(cmd *cobra.Command, args []string) error {
	pattern, version, isLocal, err := parseModuleArg(args[0])
	if err != nil {
		return err
	}

	ctx := context.Background()

	if makeOutput != "" {
		makeOutput, err = filepath.Abs(makeOutput)
		if err != nil {
			return fmt.Errorf("failed to resolve output path: %w", err)
		}
	}

	matrix, err := resolveMatrix(cmd)
	if err != nil {
		return err
	}

	// Set up remote formula store (always needed for deps)
	remoteStore, err := newRemoteStore()
	if err != nil {
		return err
	}

	if !isLocal {
		return buildModule(ctx, remoteStore, pattern, version, matrix, false)
	}

	// Resolve local pattern
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	localMods, err := modlocal.Resolve(cwd, pattern)
	if err != nil {
		return err
	}

	// Build overlay: local modules from disk, deps from remote
	locals := make(map[string]string, len(localMods))
	for _, m := range localMods {
		locals[m.Path] = m.Dir
	}
	store := repo.NewOverlayStore(remoteStore, locals)

	for _, m := range localMods {
		ver := m.Version
		if ver == "" {
			ver = version // global @version from arg
		}
		if err := buildModule(ctx, store, m.Path, ver, matrix, false); err != nil {
			return err
		}
	}
	return nil
}

func hostMatrix() formula.Matrix {
	return formula.Matrix{
		Require: map[string][]string{
			"os":   {runtime.GOOS},
			"arch": {runtime.GOARCH},
		},
	}
}

// buildModule loads and builds a single module. When runTest is true, the
// builder additionally runs the root target's onTest hook against the
// module's artifacts (freshly built or reused from cache). Transitive
// dependencies still honor the build cache and do not have their onTest
// hooks triggered — each dependency is verified by its own
// `llar test <dep>` invocation.
func buildModule(ctx context.Context, store repo.Store, modPath, version string, matrix formula.Matrix, runTest bool) error {
	root := module.Version{Path: modPath, Version: version}
	var targetOS, targetArch string
	if values := matrix.Require["os"]; len(values) > 0 {
		targetOS = values[0]
	}
	if values := matrix.Require["arch"]; len(values) > 0 {
		targetArch = values[0]
	}
	crossCompile := targetOS != runtime.GOOS || targetArch != runtime.GOARCH
	if runTest && crossCompile {
		return fmt.Errorf("llar test cannot run %s/%s target on %s/%s host", targetOS, targetArch, runtime.GOOS, runtime.GOARCH)
	}
	loadOpts := modules.Options{
		FormulaStore: store,
		Matrix:       matrix,
	}
	mods, err := modules.Load(ctx, root, loadOpts)
	if err != nil {
		return fmt.Errorf("failed to load modules: %w", err)
	}

	var buildOutput io.Writer = io.Discard
	if makeVerbose {
		buildOutput = os.Stderr
	}

	matrixStr := matrix.Combinations()[0]
	buildOpts := build.Options{
		Store:     store,
		MatrixStr: matrixStr,
		RunTest:   runTest,
		Stdout:    buildOutput,
		Stderr:    buildOutput,
	}
	if makeOutput != "" {
		tmpDir, err := os.MkdirTemp("", "llar-make-*")
		if err != nil {
			return fmt.Errorf("failed to create temp workspace: %w", err)
		}
		defer os.RemoveAll(tmpDir)
		buildOpts.WorkspaceDir = tmpDir
	}
	target, err := crosscompile.Load(ctx, root, crosscompile.Config{
		Store:        store,
		Matrix:       matrix,
		Stdout:       buildOpts.Stdout,
		Stderr:       buildOpts.Stderr,
		WorkspaceDir: buildOpts.WorkspaceDir,
		Cache:        buildOpts.Cache,
	})
	if err != nil {
		return err
	}
	if target != nil {
		if closer, ok := target.(io.Closer); ok {
			defer closer.Close()
		}
		buildOpts.Target = target
	}
	builder, err := build.NewBuilder(buildOpts)
	if err != nil {
		return fmt.Errorf("failed to create builder: %w", err)
	}

	results, err := builder.Build(ctx, mods)
	if err != nil {
		return fmt.Errorf("failed to build %s@%s: %w", modPath, version, err)
	}

	if len(results) > 0 {
		main := results[len(results)-1]
		outputDeps := make([]moduleOutputDep, 0, len(results)-1)
		for _, result := range results[:len(results)-1] {
			outputDeps = append(outputDeps, moduleOutputDep{
				Module:    result.Module,
				OutputDir: result.OutputDir,
			})
		}
		result := moduleOutputResult{
			Module:    main.Module,
			Deps:      outputDeps,
			Metadata:  main.Metadata,
			OutputDir: main.OutputDir,
		}
		if err := writeModuleResult(os.Stdout, result, makeJSON); err != nil {
			return err
		}
		if makeOutput != "" {
			if err := writeModuleOutput(result, makeOutput); err != nil {
				return fmt.Errorf("failed to write output: %w", err)
			}
		}
	}

	return nil
}

// parseModuleArg parses a module argument and detects local filesystem patterns.
// Local patterns follow Go-style local import forms (., .., ./x, ../x, absolute path).
// Returns an error for invalid patterns like ".@version" (use "./@version" instead).
func parseModuleArg(arg string) (pattern, version string, isLocal bool, err error) {
	if strings.HasPrefix(arg, ".@") {
		return "", "", false, fmt.Errorf("invalid local pattern %q: use \"./@version\" instead of \".@version\"", arg)
	}

	pattern = arg
	for i := len(pattern) - 1; i >= 0; i-- {
		if pattern[i] == '@' {
			version = pattern[i+1:]
			pattern = pattern[:i]
			break
		}
	}

	if stdbuild.IsLocalImport(pattern) || filepath.IsAbs(pattern) {
		isLocal = true
		pattern = filepath.Clean(pattern)
		if pattern == "." {
			pattern = ""
		}
	}
	return
}
