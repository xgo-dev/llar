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
	Use:                "test [module@version]",
	Short:              "Verify a module's installed artifacts",
	Long:               `Test builds and installs the selected module matrix, then verifies that the resulting artifacts are usable from a consumer's perspective.`,
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
