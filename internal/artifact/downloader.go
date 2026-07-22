// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package artifact

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
	"strings"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/mod/module"
)

type DownloaderOptions struct {
	BaseURL string
}

type DownloadResult struct {
	Module module.Version
	File   *os.File
	Deps   []module.Version
}

type Downloader struct {
	baseURL *url.URL
}

type downloadArtifactMessage struct {
	ID   string   `json:"id"`
	Type string   `json:"type"`
	URL  string   `json:"url"`
	Deps []string `json:"deps,omitempty"`
}

func NewDownloader(opts DownloaderOptions) (*Downloader, error) {
	baseURL, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid artifact base URL %q", opts.BaseURL)
	}
	return &Downloader{baseURL: baseURL}, nil
}

func (d *Downloader) Download(ctx context.Context, mod module.Version, matrix formula.Matrix, progress io.Writer) (DownloadResult, error) {
	query, err := downloadQuery(matrix)
	if err != nil {
		return DownloadResult{}, err
	}
	messages, err := d.request(ctx, mod, query, progress)
	if err != nil {
		return DownloadResult{}, err
	}
	message, resolved, deps, err := resolveDownloadArtifact(messages, mod, query)
	if err != nil {
		return DownloadResult{}, err
	}
	file, err := d.download(ctx, message)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("download artifact %s: %w", message.ID, err)
	}
	return DownloadResult{Module: resolved, File: file, Deps: deps}, nil
}

func downloadQuery(matrix formula.Matrix) (url.Values, error) {
	query := make(url.Values, len(matrix.Require)+len(matrix.Options))
	add := func(values map[string][]string) error {
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
	if err := add(matrix.Require); err != nil {
		return nil, err
	}
	if err := add(matrix.Options); err != nil {
		return nil, err
	}
	if len(query) == 0 {
		return nil, fmt.Errorf("build matrix is required")
	}
	return query, nil
}

func (d *Downloader) request(ctx context.Context, mod module.Version, query url.Values, progress io.Writer) ([]downloadArtifactMessage, error) {
	endpoint := *d.baseURL
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
	resp, err := http.DefaultClient.Do(req)
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

	var artifacts []downloadArtifactMessage
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
				var artifact downloadArtifactMessage
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

func resolveDownloadArtifact(messages []downloadArtifactMessage, requested module.Version, query url.Values) (downloadArtifactMessage, module.Version, []module.Version, error) {
	wantQuery := query.Encode()
	var root downloadArtifactMessage
	var resolved module.Version
	rootFound := false
	for _, message := range messages {
		mod, gotQuery, err := parseDownloadArtifactID(message.ID)
		if err != nil {
			return downloadArtifactMessage{}, module.Version{}, nil, err
		}
		if gotQuery != wantQuery {
			return downloadArtifactMessage{}, module.Version{}, nil, fmt.Errorf("artifact %q has matrix query %q, want %q", message.ID, gotQuery, wantQuery)
		}
		if mod.Path == requested.Path && (requested.Version == "" || mod.Version == requested.Version) {
			if rootFound {
				return downloadArtifactMessage{}, module.Version{}, nil, fmt.Errorf("llard returned multiple artifacts for %s", requested.Path)
			}
			root = message
			resolved = mod
			rootFound = true
		}
	}
	if !rootFound {
		return downloadArtifactMessage{}, module.Version{}, nil, fmt.Errorf("llard response is missing requested artifact %s", requested.Path)
	}

	deps := make([]module.Version, 0, len(root.Deps))
	for _, depID := range root.Deps {
		dep, depQuery, err := parseDownloadArtifactID(depID)
		if err != nil {
			return downloadArtifactMessage{}, module.Version{}, nil, err
		}
		if depQuery != wantQuery {
			return downloadArtifactMessage{}, module.Version{}, nil, fmt.Errorf("dependency %q has matrix query %q, want %q", depID, depQuery, wantQuery)
		}
		deps = append(deps, dep)
	}
	return root, resolved, deps, nil
}

func parseDownloadArtifactID(id string) (module.Version, string, error) {
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

func (d *Downloader) download(ctx context.Context, artifact downloadArtifactMessage) (*os.File, error) {
	var suffix string
	switch artifact.Type {
	case "tar.gz":
		suffix = ".tar.gz"
	case "zip":
		suffix = ".zip"
	default:
		return nil, fmt.Errorf("unsupported artifact type %q", artifact.Type)
	}

	source, err := url.Parse(artifact.URL)
	if err != nil {
		return nil, err
	}
	source = d.baseURL.ResolveReference(source)
	if source.Scheme != "http" && source.Scheme != "https" || source.Host == "" {
		return nil, fmt.Errorf("invalid artifact URL %q", artifact.URL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}

	file, err := os.CreateTemp("", "llar-artifact-*"+suffix)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return file, nil
}
