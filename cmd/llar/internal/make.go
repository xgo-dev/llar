package internal

import (
	"context"
	"encoding/json"
	"fmt"
	stdbuild "go/build"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/internal/modules/modlocal"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	"github.com/spf13/cobra"
)

var makeVerbose bool
var makeOutput string
var makeJSON bool

type makeJSONDep struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type makeJSONResult struct {
	Path     string        `json:"path"`
	Version  string        `json:"version"`
	Deps     []makeJSONDep `json:"deps,omitempty"`
	Metadata string        `json:"metadata"`
}

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
	Short:              "Build a module to FormulaDir",
	Long:               `Make downloads and builds a module to FormulaDir.`,
	Args:               cobra.ExactArgs(1),
	FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	RunE:               runMake,
}

func init() {
	makeCmd.Flags().BoolVarP(&makeVerbose, "verbose", "v", false, "Enable verbose build output")
	makeCmd.Flags().StringVarP(&makeOutput, "output", "o", "", "Output archive path (.zip file or .tar.gz file)")
	makeCmd.Flags().BoolVarP(&makeJSON, "json", "j", false, "Print build result as JSON")
	rootCmd.AddCommand(makeCmd)
}

func runMake(cmd *cobra.Command, args []string) error {
	pattern, version, isLocal, err := parseModuleArg(args[0])
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Resolve output path to absolute before build (build may change cwd)
	if makeOutput != "" {
		abs, err := filepath.Abs(makeOutput)
		if err != nil {
			return fmt.Errorf("failed to resolve output path: %w", err)
		}
		makeOutput = abs
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
	mods, err := modules.Load(ctx, module.Version{Path: modPath, Version: version}, modules.Options{
		FormulaStore: store,
		Matrix:       matrix,
	})
	if err != nil {
		return fmt.Errorf("failed to load modules: %w", err)
	}

	buildOutput := buildOutputWriter()

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
		if makeJSON {
			deps := artifactDeps(mods)
			jsonDeps := make([]makeJSONDep, 0, len(deps))
			for _, dep := range deps {
				jsonDeps = append(jsonDeps, makeJSONDep{Path: dep.Path, Version: dep.Version})
			}
			out := makeJSONResult{
				Path:     mods[0].Path,
				Version:  mods[0].Version,
				Deps:     jsonDeps,
				Metadata: main.Metadata,
			}
			if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
				return err
			}
		} else if main.Metadata != "" {
			fmt.Println(main.Metadata)
		}
		if makeOutput != "" {
			if err := outputArtifact(main.OutputDir, makeOutput, main.Metadata, artifactDeps(mods)); err != nil {
				return fmt.Errorf("failed to write output: %w", err)
			}
		}
	}

	return nil
}

func buildOutputWriter() io.Writer {
	if makeVerbose {
		return os.Stderr
	}
	return io.Discard
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

func artifactDeps(mods []*modules.Module) []module.Version {
	if len(mods) <= 1 {
		return nil
	}
	deps := make([]module.Version, 0, len(mods)-1)
	main := mods[0]
	for _, mod := range mods[1:] {
		if mod.Path == main.Path && mod.Version == main.Version {
			continue
		}
		deps = append(deps, module.Version{Path: mod.Path, Version: mod.Version})
	}
	return deps
}

func outputArtifact(srcDir, dest, value string, deps []module.Version) error {
	body, err := metadata.Encode(value, srcDir, deps)
	if err != nil {
		return err
	}
	return archiver.Pack(srcDir, dest, body)
}
