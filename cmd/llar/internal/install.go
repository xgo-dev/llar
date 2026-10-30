package internal

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	installer "github.com/goplus/llar/install"
	"github.com/goplus/llar/mod/module"
	"github.com/goplus/llar/x/runtime/formula"
	"github.com/spf13/cobra"
)

const llardServiceURL = "https://llar.xgo.dev"

var installVerbose bool
var installOutput string
var installJSON bool

var installCmd = &cobra.Command{
	Use:                "install [module@version]",
	Short:              "Install a module from LLAR Cloud",
	Long:               `Install obtains the selected module build from LLAR Cloud and installs it with its dependencies into the local LLAR workspace. Missing builds are produced on demand and shared for future installs.`,
	Args:               cobra.ExactArgs(1),
	FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	RunE:               runInstall,
}

func init() {
	installCmd.Flags().BoolVarP(&installVerbose, "verbose", "v", false, "Enable verbose build output")
	installCmd.Flags().StringVarP(&installOutput, "output", "o", "", "Output path (directory, .zip file, or .tar.gz file)")
	installCmd.Flags().BoolVarP(&installJSON, "json", "j", false, "Print module result as JSON")
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	var err error
	if installOutput != "" {
		installOutput, err = filepath.Abs(installOutput)
		if err != nil {
			return fmt.Errorf("failed to resolve output path: %w", err)
		}
	}
	matrix, err := resolveMatrix(cmd)
	if err != nil {
		return err
	}
	var progress io.Writer = io.Discard
	if installVerbose {
		progress = cmd.ErrOrStderr()
	}
	result, err := install(cmd.Context(), progress, llardServiceURL, args[0], matrix)
	if err != nil {
		return err
	}
	if err := writeModuleResult(cmd.OutOrStdout(), result, installJSON); err != nil {
		return err
	}
	if installOutput != "" {
		// TODO: Let install clients request the remote artifact compression format.
		// Until then, .zip and .tar.gz outputs repackage the installed root while
		// directory outputs, for example "-o ./zlib-out", copy its files directly.
		if err := writeModuleOutput(result, installOutput); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
	}
	return nil
}

// defaultWorkspaceDir returns the default LLAR workspace directory.
func defaultWorkspaceDir() (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	workspaceDir := filepath.Join(userCacheDir, ".llar", "workspaces")
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return "", err
	}
	return workspaceDir, nil
}

func install(ctx context.Context, progress io.Writer, serviceURL, arg string, matrix formula.Matrix) (moduleOutputResult, error) {
	modPath, version, isLocal, err := parseModuleArg(arg)
	if err != nil {
		return moduleOutputResult{}, err
	}
	if isLocal {
		return moduleOutputResult{}, fmt.Errorf("llar install does not support local formulas: %q", arg)
	}
	if len(matrix.Require) == 0 && len(matrix.Options) == 0 {
		return moduleOutputResult{}, fmt.Errorf("build matrix is required")
	}
	var config installer.Config
	for key, values := range matrix.Require {
		if key == "" {
			return moduleOutputResult{}, fmt.Errorf("matrix key is required")
		}
		if len(values) != 1 || values[0] == "" {
			return moduleOutputResult{}, fmt.Errorf("matrix %q requires exactly one value", key)
		}
		switch key {
		case "os":
			config.Require.OS = values[0]
		case "arch":
			config.Require.Arch = values[0]
		case "libc":
			config.Require.Libc = values[0]
		default:
			return moduleOutputResult{}, fmt.Errorf("llar install does not support require %q; use os, arch, or libc", key)
		}
	}
	config.Options = make(map[string]string, len(matrix.Options))
	for key, values := range matrix.Options {
		if key == "" {
			return moduleOutputResult{}, fmt.Errorf("matrix key is required")
		}
		if len(values) != 1 || values[0] == "" {
			return moduleOutputResult{}, fmt.Errorf("matrix %q requires exactly one value", key)
		}
		config.Options[key] = values[0]
	}
	client := installer.Installer{ServiceURL: serviceURL}
	installed, err := client.Install(ctx, module.Version{Path: modPath, Version: version}, installer.Options{
		Config:   config,
		Progress: progress,
	})
	if err != nil {
		return moduleOutputResult{}, err
	}
	result := moduleOutputResult{Module: installed.Module, Metadata: installed.Metadata, OutputDir: installed.Dir}
	for _, dep := range installed.Deps {
		result.Deps = append(result.Deps, moduleOutputDep{Module: dep.Module, OutputDir: dep.Dir})
	}
	return result, nil
}
