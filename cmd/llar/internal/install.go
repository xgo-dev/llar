package internal

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/build"
	buildcache "github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
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
	installCmd.Flags().StringVarP(&installOutput, "output", "o", "", "Output archive path (.zip file or .tar.gz file)")
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
		// TODO: Let clients request the artifact compression format from llard.
		//
		// Current behavior:
		//   - --output repacks the installed root. For example, .zip selects ZIP
		//     and .tar.gz selects tar+gzip, regardless of the remote artifact type.
		body, err := metadata.Encode(metadata.Info{Metadata: result.Metadata, Deps: result.Deps}, result.OutputDir)
		if err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
		if err := archiver.Pack(result.OutputDir, installOutput, body); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
	}
	return nil
}

func install(ctx context.Context, progress io.Writer, serviceURL, arg string, matrix formula.Matrix) (moduleOutputResult, error) {
	modPath, version, isLocal, err := parseModuleArg(arg)
	if err != nil {
		return moduleOutputResult{}, err
	}
	if isLocal {
		return moduleOutputResult{}, fmt.Errorf("llar install does not support local formulas: %q", arg)
	}

	downloader, err := artifact.NewDownloader(artifact.DownloaderOptions{BaseURL: serviceURL})
	if err != nil {
		return moduleOutputResult{}, err
	}

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return moduleOutputResult{}, err
	}
	workspaceDir := filepath.Join(userCacheDir, ".llar", "workspaces")
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return moduleOutputResult{}, err
	}
	return installModules(ctx, progress, downloader, workspaceDir, module.Version{Path: modPath, Version: version}, matrix)
}

func installModules(ctx context.Context, progress io.Writer, downloader *artifact.Downloader, workspaceDir string, root module.Version, matrix formula.Matrix) (moduleOutputResult, error) {
	cache := build.NewLocalCache(workspaceDir)
	matrixStr := matrix.Combinations()[0]
	var rootResult moduleOutputResult

	modules := []module.Version{root}
	downloadAndInstall := func(i int) error {
		downloaded, err := downloader.Download(ctx, modules[i], matrix, progress)
		if err != nil {
			return err
		}
		fileName := downloaded.File.Name()
		defer os.Remove(fileName)

		if i == 0 {
			// The root artifact lists the complete MVS build list.
			modules = append(modules, downloaded.Deps...)
			rootResult.Module = downloaded.Module
			rootResult.Deps = downloaded.Deps
		}
		escaped, err := module.EscapePath(downloaded.Module.Path)
		if err != nil {
			return fmt.Errorf("invalid downloaded module %q: %w", downloaded.Module.Path, err)
		}
		installDir := filepath.Join(workspaceDir, fmt.Sprintf("%s@%s-%s", escaped, downloaded.Module.Version, matrixStr))
		info, err := installDownloadedArtifact(fileName, installDir)
		if err != nil {
			return fmt.Errorf("install artifact %s@%s: %w", downloaded.Module.Path, downloaded.Module.Version, err)
		}
		_, err = cache.Put(ctx, buildcache.Key{
			Module: downloaded.Module,
			Matrix: matrixStr,
		}, os.DirFS(installDir), buildcache.Entry{
			Metadata: info.Metadata,
			Deps:     info.Deps,
		})
		if err != nil {
			return fmt.Errorf("cache artifact %s@%s: %w", downloaded.Module.Path, downloaded.Module.Version, err)
		}
		if i == 0 {
			rootResult.Metadata = info.Metadata
			rootResult.OutputDir = installDir
		}
		return nil
	}
	for i := 0; i < len(modules); i++ {
		if err := downloadAndInstall(i); err != nil {
			return moduleOutputResult{}, err
		}
	}
	return rootResult, nil
}

func installDownloadedArtifact(fileName, installDir string) (metadata.Info, error) {
	parent := filepath.Dir(installDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return metadata.Info{}, err
	}
	stagingDir, err := os.MkdirTemp(parent, ".llar-install-*")
	if err != nil {
		return metadata.Info{}, err
	}
	defer os.RemoveAll(stagingDir)
	if err := os.Chmod(stagingDir, 0o755); err != nil {
		return metadata.Info{}, err
	}

	data, err := archiver.Unpack(fileName, stagingDir)
	if err != nil {
		return metadata.Info{}, err
	}
	info, err := metadata.Decode(data, installDir)
	if err != nil {
		return metadata.Info{}, err
	}
	if err := os.RemoveAll(installDir); err != nil {
		return metadata.Info{}, err
	}
	if err := os.Rename(stagingDir, installDir); err != nil {
		return metadata.Info{}, err
	}
	return info, nil
}
