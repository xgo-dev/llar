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
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/internal/modules/modlocal"
	"github.com/goplus/llar/internal/vcs"
	"github.com/goplus/llar/mod/module"
	"github.com/spf13/cobra"
)

var makeVerbose bool
var makeOutput string

type artifactMetadata struct {
	Metadata string   `json:"metadata"`
	Deps     []string `json:"deps,omitempty"`
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
	makeCmd.Flags().StringVarP(&makeOutput, "output", "o", "", "Output path (directory, .zip file, or .tar.gz file)")
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

	matrixStr, err := resolveMatrixStr(cmd)
	if err != nil {
		return err
	}

	// Set up remote formula store (always needed for deps)
	remoteStore, err := newRemoteStore()
	if err != nil {
		return err
	}

	if !isLocal {
		return buildModule(ctx, remoteStore, pattern, version, matrixStr, false)
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
		if err := buildModule(ctx, store, m.Path, ver, matrixStr, false); err != nil {
			return err
		}
	}
	return nil
}

// hostMatrixCombo returns the matrix combination for the current host
// (os+arch). It is used by both `llar make` and `llar test` to select
// the default build variant when the user does not specify one.
func hostMatrixCombo() string {
	matrix := formula.Matrix{
		Require: map[string][]string{
			"os":   {runtime.GOOS},
			"arch": {runtime.GOARCH},
		},
	}
	return matrix.Combinations()[0]
}

// buildModule loads and builds a single module. When runTest is true, the
// builder additionally runs the root target's onTest hook against the
// module's artifacts (freshly built or reused from cache). Transitive
// dependencies still honor the build cache and do not have their onTest
// hooks triggered — each dependency is verified by its own
// `llar test <dep>` invocation.
func buildModule(ctx context.Context, store repo.Store, modPath, version, matrixStr string, runTest bool) error {
	mods, err := modules.Load(ctx, module.Version{Path: modPath, Version: version}, modules.Options{
		FormulaStore: store,
	})
	if err != nil {
		return fmt.Errorf("failed to load modules: %w", err)
	}

	restoreOutput, err := redirectBuildOutput(mods)
	if err != nil {
		return err
	}
	outputRestored := false
	defer func() {
		if !outputRestored {
			restoreOutput()
		}
	}()

	buildOpts := build.Options{
		Store:     store,
		MatrixStr: matrixStr,
		RunTest:   runTest,
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

	// Restore stdout before printing results.
	restoreOutput()
	outputRestored = true

	if len(results) > 0 {
		main := results[len(results)-1]
		if main.Metadata != "" {
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

// redirectBuildOutput reserves command stdout for final metadata. In verbose
// mode, build stdout is redirected to stderr; in silent mode, build output is
// discarded until the metadata line is printed.
func redirectBuildOutput(mods []*modules.Module) (func(), error) {
	if !makeVerbose {
		for _, mod := range mods {
			mod.SetStdout(io.Discard)
			mod.SetStderr(io.Discard)
		}

		savedStdout := os.Stdout
		savedStderr := os.Stderr
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to open devnull: %w", err)
		}
		os.Stdout = devNull
		os.Stderr = devNull
		return func() {
			os.Stdout = savedStdout
			os.Stderr = savedStderr
			_ = devNull.Close()
		}, nil
	}

	savedStdout := os.Stdout
	os.Stdout = os.Stderr
	for _, mod := range mods {
		mod.SetStdout(os.Stderr)
	}
	return func() {
		os.Stdout = savedStdout
	}, nil
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

func artifactMetainfo(metadata string, deps []module.Version) ([]byte, error) {
	body, err := json.MarshalIndent(artifactMetadata{
		Metadata: metadata,
		Deps:     artifactDepStrings(deps),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
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

func artifactDepStrings(deps []module.Version) []string {
	if len(deps) == 0 {
		return nil
	}
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		out = append(out, dep.Path+"@"+dep.Version)
	}
	return out
}

func outputArtifact(srcDir, dest, metadata string, deps []module.Version) error {
	metainfo, err := artifactMetainfo(metadata, deps)
	if err != nil {
		return err
	}
	return archiver.Pack(srcDir, dest, metainfo)
}
