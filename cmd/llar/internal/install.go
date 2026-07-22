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

	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/build"
	buildcache "github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
	"github.com/spf13/cobra"
)

const llardServiceURL = "https://llar.xgo.dev"

type installArtifactMessage struct {
	ID   string   `json:"id"`
	Type string   `json:"type"`
	URL  string   `json:"url"`
	Deps []string `json:"deps,omitempty"`
}

type resolvedInstallArtifact struct {
	message installArtifactMessage
	module  module.Version
}

var installCmd = &cobra.Command{
	Use:   "install [module@version]",
	Short: "Install a module from LLAR Cloud",
	Long:  `Install obtains the selected module build from LLAR Cloud and installs it with its dependencies into the local LLAR workspace. Missing builds are produced on demand and shared for future installs.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runInstall,
}

func init() {
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	return install(cmd.Context(), cmd.ErrOrStderr(), http.DefaultClient, llardServiceURL, args[0])
}

func install(ctx context.Context, progress io.Writer, client *http.Client, serviceURL, arg string) error {
	modPath, version, isLocal, err := parseModuleArg(arg)
	if err != nil {
		return err
	}
	if isLocal {
		return fmt.Errorf("llar install does not support local formulas: %q", arg)
	}

	matrix := hostMatrix()
	matrixStr := matrix.Combinations()[0]
	query := make(url.Values, len(matrix.Require))
	for key, values := range matrix.Require {
		if len(values) != 1 {
			return fmt.Errorf("host matrix %q requires exactly one value", key)
		}
		query.Set(key, values[0])
	}

	messages, err := requestInstallArtifacts(ctx, progress, client, serviceURL, modPath, version, query)
	if err != nil {
		return err
	}
	artifacts, err := resolveInstallArtifacts(messages, module.Version{Path: modPath, Version: version}, query)
	if err != nil {
		return err
	}

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	workspaceDir := filepath.Join(userCacheDir, ".llar", "workspaces")
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return err
	}
	cache := build.NewLocalCache(workspaceDir)

	for _, artifact := range artifacts {
		escaped, err := module.EscapePath(artifact.module.Path)
		if err != nil {
			return fmt.Errorf("invalid artifact id %q: %w", artifact.message.ID, err)
		}
		installDir := filepath.Join(workspaceDir, fmt.Sprintf("%s@%s-%s", escaped, artifact.module.Version, matrixStr))
		info, err := downloadInstallArtifact(ctx, client, artifact.message, installDir)
		if err != nil {
			return fmt.Errorf("install artifact %s: %w", artifact.message.ID, err)
		}
		_, err = cache.Put(ctx, buildcache.Key{
			Module: artifact.module,
			Matrix: matrixStr,
		}, os.DirFS(installDir), buildcache.Entry{
			Metadata: info.Metadata,
			Deps:     info.Deps,
		})
		if err != nil {
			return fmt.Errorf("cache artifact %s: %w", artifact.message.ID, err)
		}
	}
	return nil
}

func requestInstallArtifacts(ctx context.Context, progress io.Writer, client *http.Client, serviceURL, modPath, version string, query url.Values) ([]installArtifactMessage, error) {
	endpoint, err := url.Parse(serviceURL)
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid llard service URL %q", serviceURL)
	}
	target := modPath
	if version != "" {
		target += "@" + version
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
	// TODO: Once ixgo supports a qiniu/x release containing cmdjsonl,
	// replace this temporary response parser with github.com/qiniu/x/cmdjsonl.
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
	artifactIDs := make(map[string]struct{}, len(messages))
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
			rootFound = true
		}
		canonicalID := mod.Path + "@" + mod.Version + "?" + gotQuery
		if _, ok := artifactIDs[canonicalID]; ok {
			return nil, fmt.Errorf("llard returned duplicate artifact %q", message.ID)
		}
		artifactIDs[canonicalID] = struct{}{}
		artifacts = append(artifacts, resolvedInstallArtifact{message: message, module: mod})
	}
	if !rootFound {
		return nil, fmt.Errorf("llard response is missing requested artifact %s", requested.Path)
	}
	for _, artifact := range artifacts {
		for _, depID := range artifact.message.Deps {
			dep, depQuery, err := parseInstallArtifactID(depID)
			if err != nil {
				return nil, err
			}
			canonicalID := dep.Path + "@" + dep.Version + "?" + depQuery
			if _, ok := artifactIDs[canonicalID]; !ok {
				return nil, fmt.Errorf("artifact %q is missing dependency %q", artifact.message.ID, depID)
			}
		}
	}
	return artifacts, nil
}

func parseInstallArtifactID(id string) (module.Version, string, error) {
	parsed, err := url.Parse(id)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Fragment != "" || parsed.RawQuery == "" {
		return module.Version{}, "", fmt.Errorf("invalid artifact id %q", id)
	}
	modPath, version, isLocal, err := parseModuleArg(parsed.Path)
	if err != nil || isLocal || modPath == "" || version == "" {
		return module.Version{}, "", fmt.Errorf("invalid artifact id %q", id)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) == 0 {
		return module.Version{}, "", fmt.Errorf("invalid artifact id %q", id)
	}
	return module.Version{Path: modPath, Version: version}, query.Encode(), nil
}

func downloadInstallArtifact(ctx context.Context, client *http.Client, artifact installArtifactMessage, installDir string) (metadata.Info, error) {
	var suffix string
	switch artifact.Type {
	case "tar.gz":
		suffix = ".tar.gz"
	case "zip":
		suffix = ".zip"
	default:
		return metadata.Info{}, fmt.Errorf("unsupported artifact type %q", artifact.Type)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
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
	fileName := file.Name()
	defer os.Remove(fileName)
	_, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return metadata.Info{}, copyErr
	}
	if closeErr != nil {
		return metadata.Info{}, closeErr
	}

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
