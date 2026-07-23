package internal

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/build"
	buildcache "github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
	"github.com/spf13/cobra"
)

const llardServiceURL = "https://llar.xgo.dev"

type installArtifactMessage struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

type resolvedInstallArtifact struct {
	message installArtifactMessage
	module  module.Version
}

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

func install(ctx context.Context, progress io.Writer, serviceURL, arg string, matrix formula.Matrix) (moduleOutputResult, error) {
	modPath, version, isLocal, err := parseModuleArg(arg)
	if err != nil {
		return moduleOutputResult{}, err
	}
	if isLocal {
		return moduleOutputResult{}, fmt.Errorf("llar install does not support local formulas: %q", arg)
	}

	baseURL, err := url.Parse(serviceURL)
	if err != nil || baseURL.Scheme != "http" && baseURL.Scheme != "https" || baseURL.Host == "" {
		return moduleOutputResult{}, fmt.Errorf("invalid llard service URL %q", serviceURL)
	}
	query := make(url.Values, len(matrix.Require)+len(matrix.Options))
	addMatrix := func(values map[string][]string) error {
		for key, items := range values {
			if key == "" {
				return fmt.Errorf("matrix key is required")
			}
			if len(items) != 1 || items[0] == "" {
				return fmt.Errorf("matrix %q requires exactly one value", key)
			}
			if query.Has(key) {
				return fmt.Errorf("matrix %q is duplicated", key)
			}
			query.Set(key, items[0])
		}
		return nil
	}
	if err := addMatrix(matrix.Require); err != nil {
		return moduleOutputResult{}, err
	}
	if err := addMatrix(matrix.Options); err != nil {
		return moduleOutputResult{}, err
	}
	if len(query) == 0 {
		return moduleOutputResult{}, fmt.Errorf("build matrix is required")
	}

	requested := module.Version{Path: modPath, Version: version}
	messages, err := requestInstallArtifacts(ctx, progress, http.DefaultClient, baseURL, requested, query)
	if err != nil {
		return moduleOutputResult{}, err
	}
	artifacts, err := resolveInstallArtifacts(messages, requested, query)
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
	cache := build.NewLocalCache(workspaceDir)
	matrixStr := matrix.Combinations()[0]
	var root module.Version
	deps := make([]module.Version, 0, len(artifacts)-1)
	for _, artifact := range artifacts {
		if artifact.module.Path == requested.Path && (requested.Version == "" || artifact.module.Version == requested.Version) {
			root = artifact.module
			continue
		}
		deps = append(deps, artifact.module)
	}

	var rootResult moduleOutputResult
	for _, artifact := range artifacts {
		escaped, err := module.EscapePath(artifact.module.Path)
		if err != nil {
			return moduleOutputResult{}, fmt.Errorf("invalid artifact id %q: %w", artifact.message.ID, err)
		}
		installDir := filepath.Join(workspaceDir, fmt.Sprintf("%s@%s-%s", escaped, artifact.module.Version, matrixStr))
		key := buildcache.Key{Module: artifact.module, Matrix: matrixStr}
		entry, ok, err := cache.Get(ctx, key)
		if err != nil {
			return moduleOutputResult{}, fmt.Errorf("read cache for artifact %s: %w", artifact.message.ID, err)
		}
		if !ok {
			info, err := downloadInstallArtifact(ctx, http.DefaultClient, baseURL, artifact.message, installDir)
			if err != nil {
				return moduleOutputResult{}, fmt.Errorf("install artifact %s: %w", artifact.message.ID, err)
			}
			entry, err = cache.Put(ctx, key, os.DirFS(installDir), buildcache.Entry{
				Metadata: info.Metadata,
				Deps:     info.Deps,
			})
			if err != nil {
				return moduleOutputResult{}, fmt.Errorf("cache artifact %s: %w", artifact.message.ID, err)
			}
		}
		if artifact.module == root {
			rootResult = moduleOutputResult{
				Module:    root,
				Deps:      deps,
				Metadata:  entry.Metadata,
				OutputDir: installDir,
			}
		}
	}
	return rootResult, nil
}

func requestInstallArtifacts(ctx context.Context, progress io.Writer, client *http.Client, baseURL *url.URL, mod module.Version, query url.Values) ([]installArtifactMessage, error) {
	endpoint := *baseURL
	target := mod.Path
	if mod.Version != "" {
		target += "@" + mod.Version
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/artifacts/" + target
	endpoint.RawQuery = query.Encode()
	endpoint.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("llard returned %s", resp.Status)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-cmdjsonl" {
		return nil, fmt.Errorf("llard returned content type %q, want application/x-cmdjsonl", resp.Header.Get("Content-Type"))
	}
	if progress == nil {
		progress = io.Discard
	}

	var artifacts []installArtifactMessage
	// TODO: Upgrade ixgo and replace this parser with github.com/qiniu/x/cmdjsonl.
	//
	// Dependency constraint:
	//   - ixgo v0.61.0 ships generated bindings for qiniu/x packages such as
	//     gsh, osx, stringutil, and xgo/ng.
	//   - Its stringutil binding references Builder, NewBuilder, and
	//     NewBuilderSize.
	//   - cmdjsonl first appears in qiniu/x v1.17.1, which removed those
	//     stringutil APIs, so upgrading qiniu/x alone breaks the build.
	//
	// Migration:
	//   - Upgrade ixgo to bindings compatible with qiniu/x v1.17.1 or newer.
	//   - Replace this temporary parser with cmdjsonl.Parser.
	reader := bufio.NewReader(resp.Body)
	for lineNo := 1; ; lineNo++ {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line != "" {
			command, data, ok := strings.Cut(line, " ")
			if !ok {
				return nil, fmt.Errorf("invalid llard response line %d", lineNo)
			}
			switch command {
			case "info":
				var message string
				if err := json.Unmarshal([]byte(data), &message); err != nil {
					return nil, fmt.Errorf("decode llard info line %d: %w", lineNo, err)
				}
				fmt.Fprintln(progress, message)
			case "error":
				var message string
				if err := json.Unmarshal([]byte(data), &message); err != nil {
					return nil, fmt.Errorf("decode llard error line %d: %w", lineNo, err)
				}
				return nil, fmt.Errorf("llard: %s", message)
			case "artifact":
				var artifact installArtifactMessage
				if err := json.Unmarshal([]byte(data), &artifact); err != nil {
					return nil, fmt.Errorf("decode llard artifact line %d: %w", lineNo, err)
				}
				if artifact.ID == "" || artifact.Type == "" || artifact.URL == "" {
					return nil, fmt.Errorf("invalid llard artifact line %d", lineNo)
				}
				artifacts = append(artifacts, artifact)
			default:
				return nil, fmt.Errorf("unsupported llard response command %q", command)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("llard returned no artifacts")
	}
	return artifacts, nil
}

func resolveInstallArtifacts(messages []installArtifactMessage, requested module.Version, query url.Values) ([]resolvedInstallArtifact, error) {
	wantQuery := query.Encode()
	artifacts := make([]resolvedInstallArtifact, 0, len(messages))
	rootFound := false
	for _, message := range messages {
		mod, gotQuery, err := parseInstallArtifactID(message.ID)
		if err != nil {
			return nil, err
		}
		if gotQuery != wantQuery {
			return nil, fmt.Errorf("artifact %q has matrix query %q, want %q", message.ID, gotQuery, wantQuery)
		}
		if mod.Path == requested.Path && (requested.Version == "" || mod.Version == requested.Version) {
			if rootFound {
				return nil, fmt.Errorf("llard returned multiple artifacts for %s", requested.Path)
			}
			rootFound = true
		}
		artifacts = append(artifacts, resolvedInstallArtifact{message: message, module: mod})
	}
	if !rootFound {
		return nil, fmt.Errorf("llard response is missing requested artifact %s", requested.Path)
	}
	return artifacts, nil
}

func parseInstallArtifactID(id string) (module.Version, string, error) {
	parsed, err := url.Parse(id)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Fragment != "" || parsed.RawQuery == "" {
		return module.Version{}, "", fmt.Errorf("invalid artifact id %q", id)
	}
	index := strings.LastIndexByte(parsed.Path, '@')
	if index <= 0 || index == len(parsed.Path)-1 {
		return module.Version{}, "", fmt.Errorf("invalid artifact id %q", id)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) == 0 {
		return module.Version{}, "", fmt.Errorf("invalid artifact id %q", id)
	}
	return module.Version{Path: parsed.Path[:index], Version: parsed.Path[index+1:]}, query.Encode(), nil
}

func downloadInstallArtifact(ctx context.Context, client *http.Client, baseURL *url.URL, artifact installArtifactMessage, installDir string) (metadata.Info, error) {
	var suffix string
	switch artifact.Type {
	case "tar.gz":
		suffix = ".tar.gz"
	case "zip":
		suffix = ".zip"
	default:
		return metadata.Info{}, fmt.Errorf("unsupported artifact type %q", artifact.Type)
	}

	source, err := url.Parse(artifact.URL)
	if err != nil {
		return metadata.Info{}, err
	}
	source = baseURL.ResolveReference(source)
	if source.Scheme != "http" && source.Scheme != "https" || source.Host == "" {
		return metadata.Info{}, fmt.Errorf("invalid artifact URL %q", artifact.URL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return metadata.Info{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return metadata.Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return metadata.Info{}, fmt.Errorf("download returned %s", resp.Status)
	}

	file, err := os.CreateTemp("", "llar-install-*"+suffix)
	if err != nil {
		return metadata.Info{}, err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	if _, err := io.Copy(file, resp.Body); err != nil {
		return metadata.Info{}, err
	}
	return installDownloadedArtifact(file.Name(), installDir)
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
