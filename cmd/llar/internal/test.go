package internal

import (
	"context"
	"fmt"
	"os"

	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/modules/modlocal"
	"github.com/spf13/cobra"
)

var testVerbose bool

var testCmd = &cobra.Command{
	Use:   "test [module@version]",
	Short: "Build a module and run its onTest hook",
	Long: `Test builds a module the same way as 'llar make', then executes
the module's onTest callback on the resulting artifacts.

The build cache is consulted as usual: if the module has already been built
with the same matrix, onBuild is skipped and onTest runs against the cached
artifacts. On a cache miss, onBuild runs and its result is cached for later
invocations before onTest executes.`,
	Args:               cobra.ExactArgs(1),
	FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	RunE:               runTest,
}

func init() {
	testCmd.Flags().BoolVarP(&testVerbose, "verbose", "v", false, "Enable verbose build/test output")
	rootCmd.AddCommand(testCmd)
}

func runTest(cmd *cobra.Command, args []string) error {
	pattern, version, isLocal, err := parseModuleArg(args[0])
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Reuse the verbose-redirection logic in buildModule by toggling the
	// shared makeVerbose flag for the duration of the test run.
	savedVerbose := makeVerbose
	makeVerbose = testVerbose
	defer func() { makeVerbose = savedVerbose }()

	matrix, err := resolveMatrix(cmd)
	if err != nil {
		return err
	}

	remoteStore, err := newRemoteStore()
	if err != nil {
		return err
	}

	if !isLocal {
		return buildModule(ctx, remoteStore, pattern, version, matrix, true)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	localMods, err := modlocal.Resolve(cwd, pattern)
	if err != nil {
		return err
	}

	locals := make(map[string]string, len(localMods))
	for _, m := range localMods {
		locals[m.Path] = m.Dir
	}
	store := repo.NewOverlayStore(remoteStore, locals)

	for _, m := range localMods {
		ver := m.Version
		if ver == "" {
			ver = version
		}
		if err := buildModule(ctx, store, m.Path, ver, matrix, true); err != nil {
			return err
		}
	}
	return nil
}
