// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package install obtains native module artifacts and installs them locally.
package install

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
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
	"github.com/qiniu/x/cmdjsonl"
)

const defaultServiceURL = "https://llar.xgo.dev"

// Installer holds the service and local workspace used by installation calls.
// Empty fields use the public LLAR service and the user's LLAR workspace.
type Installer struct {
	WorkspaceDir string
	ServiceURL   string
}

// Options configures one installation. A nil Progress writer discards progress.
type Options struct {
	Config   Config
	Progress io.Writer
}

// Result describes an installed module. Metadata contains consumer build flags.
// Deps is the flat dependency list returned by the service, not direct edges.
type Result struct {
	Module   module.Version
	Dir      string
	Metadata string
	Deps     []*Result
}

type installArtifactMessage struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

// These records retain the existing x/build workspace format, so locally built
// and downloaded artifacts can still be reused across the two entry points.
type cacheEntry struct {
	Metadata  string    `json:"metadata"`
	BuildTime time.Time `json:"build_time"`
}

type cacheFile struct {
	Cache map[string]*cacheEntry `json:"cache"`
}

// Install obtains mod and its dependencies for opts.Config. An empty version is
// resolved by the service. Successful results contain the resolved versions and
// installation directories. Already installed modules remain cached on error.
func (i *Installer) Install(ctx context.Context, mod module.Version, opts Options) (*Result, error) {
	if mod.Path == "" || mod.Path == "." {
		return nil, fmt.Errorf("invalid module path %q", mod.Path)
	}
	if _, err := module.EscapePath(mod.Path); err != nil {
		return nil, fmt.Errorf("invalid module path %q: %w", mod.Path, err)
	}
	serviceURL := i.ServiceURL
	if serviceURL == "" {
		serviceURL = defaultServiceURL
	}
	baseURL, err := url.Parse(serviceURL)
	if err != nil || baseURL.Scheme != "http" && baseURL.Scheme != "https" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid llard service URL %q", serviceURL)
	}

	require := opts.Config.Require
	if require.OS == "" {
		require.OS = runtime.GOOS
	}
	if require.Arch == "" {
		require.Arch = runtime.GOARCH
	}
	query := url.Values{"arch": {require.Arch}, "os": {require.OS}}
	// Preserve Matrix.Combinations ordering: arch, optional libc, then os.
	values := []string{require.Arch}
	if require.Libc != "" {
		query.Set("libc", require.Libc)
		values = append(values, require.Libc)
	}
	values = append(values, require.OS)
	matrixStr := strings.Join(values, "-")
	keys := make([]string, 0, len(opts.Config.Options))
	for key, value := range opts.Config.Options {
		if key == "" {
			return nil, fmt.Errorf("matrix key is required")
		}
		if value == "" {
			return nil, fmt.Errorf("matrix %q requires exactly one value", key)
		}
		if key == "os" || key == "arch" || key == "libc" {
			return nil, fmt.Errorf("matrix %q is duplicated", key)
		}
		keys = append(keys, key)
		query.Set(key, value)
	}
	slices.Sort(keys)
	values = values[:0]
	for _, key := range keys {
		values = append(values, opts.Config.Options[key])
	}
	if len(values) > 0 {
		matrixStr += "|" + strings.Join(values, "-")
	}

	messages, err := requestInstallArtifacts(ctx, opts.Progress, http.DefaultClient, baseURL, mod, query)
	if err != nil {
		return nil, err
	}
	workspaceDir := i.WorkspaceDir
	if workspaceDir == "" {
		userCacheDir, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		workspaceDir = filepath.Join(userCacheDir, ".llar", "workspaces")
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return nil, err
	}

	root := &Result{}
	deps := make([]*Result, 0, len(messages)-1)
	for _, message := range messages {
		parsed, err := url.Parse(message.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid artifact id %q: %w", message.ID, err)
		}
		index := strings.LastIndexByte(parsed.Path, '@')
		if index <= 0 || index == len(parsed.Path)-1 {
			return nil, fmt.Errorf("invalid artifact id %q", message.ID)
		}
		resolved := module.Version{Path: parsed.Path[:index], Version: parsed.Path[index+1:]}
		escaped, err := module.EscapePath(resolved.Path)
		if err != nil {
			return nil, fmt.Errorf("invalid artifact id %q: %w", message.ID, err)
		}
		installDir := filepath.Join(workspaceDir, fmt.Sprintf("%s@%s-%s", escaped, resolved.Version, matrixStr))
		cacheDir := filepath.Join(workspaceDir, escaped)
		cachePath := filepath.Join(cacheDir, ".cache.json")
		var cached cacheFile
		data, err := os.ReadFile(cachePath)
		if err != nil || json.Unmarshal(data, &cached) != nil {
			cached = cacheFile{}
		}
		cacheKey := resolved.Version + "-" + matrixStr
		entry, ok := cached.Cache[cacheKey]
		if !ok {
			info, err := downloadInstallArtifact(ctx, http.DefaultClient, baseURL, message, installDir)
			if err != nil {
				return nil, fmt.Errorf("install artifact %s: %w", message.ID, err)
			}
			entry = &cacheEntry{Metadata: info.Metadata, BuildTime: time.Now()}
			if cached.Cache == nil {
				cached.Cache = make(map[string]*cacheEntry)
			}
			cached.Cache[cacheKey] = entry
			if err := os.MkdirAll(cacheDir, 0o755); err != nil {
				return nil, fmt.Errorf("cache artifact %s: %w", message.ID, err)
			}
			data, err := json.MarshalIndent(cached, "", "  ")
			if err != nil {
				return nil, fmt.Errorf("cache artifact %s: %w", message.ID, err)
			}
			if err := os.WriteFile(cachePath, data, 0o644); err != nil {
				return nil, fmt.Errorf("cache artifact %s: %w", message.ID, err)
			}
		}
		result := &Result{Module: resolved, Metadata: entry.Metadata, Dir: installDir}
		if resolved.Path == mod.Path {
			root = result
		} else {
			deps = append(deps, result)
		}
	}
	root.Deps = deps
	return root, nil
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
	var responseErrs []error
	var parser cmdjsonl.Parser
	if err := parser.HandleFunc("info", func(message string) {
		fmt.Fprintln(progress, message)
	}); err != nil {
		return nil, err
	}
	if err := parser.HandleFunc("error", func(message string) {
		responseErrs = append(responseErrs, fmt.Errorf("llard: %s", message))
	}); err != nil {
		return nil, err
	}
	if err := parser.HandleFunc("artifact", func(artifact installArtifactMessage) error {
		if artifact.ID == "" || artifact.Type == "" || artifact.URL == "" {
			return fmt.Errorf("invalid llard artifact")
		}
		artifacts = append(artifacts, artifact)
		return nil
	}); err != nil {
		return nil, err
	}
	parseErr := parser.Parse(resp.Body, bufio.MaxScanTokenSize)
	if len(responseErrs) > 0 {
		return nil, errors.Join(responseErrs...)
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("llard returned no artifacts")
	}
	return artifacts, nil
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
