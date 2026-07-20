// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/modules"
	"github.com/goplus/llar/mod/module"
	"golang.org/x/sync/singleflight"
)

const artifactPath = "/v1/artifacts/"

type Options struct {
	FormulaStore repo.Store
	Cache        cache.Cache
	Artifacts    artifact.Store
	WorkspaceDir string
}

type handler struct {
	formulaStore repo.Store
	cache        cache.Cache
	artifacts    artifact.Store
	workspaceDir string
	group        singleflight.Group
	infos        sync.Map
}

type request struct {
	module    string
	version   string
	matrix    formula.Matrix
	matrixStr string
	query     url.Values
}

type artifactMessage struct {
	ID   string   `json:"id"`
	Type string   `json:"type"`
	URL  string   `json:"url"`
	Deps []string `json:"deps,omitempty"`
}

type result struct {
	artifacts []artifactMessage
}

func New(opts Options) http.Handler {
	return &handler{
		formulaStore: opts.FormulaStore,
		cache:        opts.Cache,
		artifacts:    opts.Artifacts,
		workspaceDir: opts.WorkspaceDir,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-cmdjsonl")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeCommand(w, "error", "method not allowed")
		return
	}

	req, err := parseRequest(r)
	if err != nil {
		writeCommand(w, "error", err.Error())
		return
	}
	writeCommand(w, "info", fmt.Sprintf("resolving %s", moduleID(req.module, req.version, req.query)))
	info := &infoWriter{w: w}
	result, err := h.do(r.Context(), req, info)
	info.flush()
	if err != nil {
		writeCommand(w, "error", err.Error())
		return
	}
	for _, artifact := range result.artifacts {
		writeCommand(w, "artifact", artifact)
	}
}

func (h *handler) do(ctx context.Context, req request, info io.Writer) (result, error) {
	key := requestKey(req)
	stream, _ := h.infos.LoadOrStore(key, newFanout())
	fanout := stream.(*fanout)
	remove := fanout.add(info)
	defer remove()

	call := h.group.DoChan(key, func() (any, error) {
		defer h.infos.Delete(key)
		return h.build(context.WithoutCancel(ctx), req, fanout)
	})
	select {
	case <-ctx.Done():
		return result{}, ctx.Err()
	case call := <-call:
		if call.Err != nil {
			return result{}, call.Err
		}
		return call.Val.(result), nil
	}
}

func (h *handler) build(ctx context.Context, req request, info io.Writer) (result, error) {
	mods, err := modules.Load(ctx, module.Version{Path: req.module, Version: req.version}, modules.Options{
		FormulaStore: h.formulaStore,
		Matrix:       req.matrix,
	})
	if err != nil {
		return result{}, err
	}

	builder, err := build.NewBuilder(build.Options{
		Store:        h.formulaStore,
		MatrixStr:    req.matrixStr,
		Stdout:       info,
		Stderr:       info,
		WorkspaceDir: h.workspaceDir,
		Cache:        h.cache,
	})
	if err != nil {
		return result{}, err
	}
	if _, err := builder.Build(ctx, mods); err != nil {
		return result{}, err
	}

	query := req.query.Encode()
	artifacts := make([]artifactMessage, 0, len(mods))
	for i := len(mods) - 1; i >= 0; i-- {
		mod := mods[i]
		stored, err := h.artifacts.Get(ctx, artifact.Key{
			Module:    mod.Path,
			Version:   mod.Version,
			MatrixStr: req.matrixStr,
		})
		if err != nil {
			return result{}, err
		}
		item := artifactMessage{
			ID:   moduleIDFromQuery(mod.Path, mod.Version, query),
			Type: stored.Type,
			URL:  stored.Source.URL,
		}
		if len(mod.Deps) > 0 {
			item.Deps = make([]string, 0, len(mod.Deps))
			for _, dep := range mod.Deps {
				item.Deps = append(item.Deps, moduleIDFromQuery(dep.Path, dep.Version, query))
			}
		}
		artifacts = append(artifacts, item)
	}
	return result{artifacts: artifacts}, nil
}

func parseRequest(r *http.Request) (request, error) {
	if !strings.HasPrefix(r.URL.Path, artifactPath) {
		return request{}, fmt.Errorf("artifact path not found")
	}
	target := strings.TrimPrefix(r.URL.Path, artifactPath)
	if target == "" || strings.HasSuffix(target, "/") {
		return request{}, fmt.Errorf("module is required")
	}

	modulePath, version := target, ""
	if pos := strings.LastIndexByte(target, '@'); pos >= 0 {
		modulePath, version = target[:pos], target[pos+1:]
	}
	if modulePath == "" {
		return request{}, fmt.Errorf("module is required")
	}

	query := r.URL.Query()
	if len(query) == 0 {
		return request{}, fmt.Errorf("build matrix is required")
	}
	var require, options map[string][]string
	for key, values := range query {
		if key == "" {
			return request{}, fmt.Errorf("matrix key is required")
		}
		if len(values) != 1 || values[0] == "" {
			return request{}, fmt.Errorf("matrix %q requires exactly one value", key)
		}
		// Requests carry a flat matrix. Platform dimensions propagate to
		// dependencies; all other dimensions are package build options.
		if key == "os" || key == "arch" {
			if require == nil {
				require = make(map[string][]string)
			}
			require[key] = []string{values[0]}
		} else {
			if options == nil {
				options = make(map[string][]string)
			}
			options[key] = []string{values[0]}
		}
	}
	matrix := formula.Matrix{Require: require, Options: options}
	combinations := matrix.Combinations()
	if len(combinations) == 0 {
		return request{}, fmt.Errorf("build matrix is required")
	}
	return request{
		module:    modulePath,
		version:   version,
		matrix:    matrix,
		matrixStr: combinations[0],
		query:     cloneValues(query),
	}, nil
}

func requestKey(req request) string {
	return moduleID(req.module, req.version, req.query)
}

func moduleID(modulePath, version string, query url.Values) string {
	return moduleIDFromQuery(modulePath, version, query.Encode())
}

func moduleIDFromQuery(modulePath, version, query string) string {
	id := modulePath
	if version != "" {
		id += "@" + version
	}
	if query != "" {
		id += "?" + query
	}
	return id
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, value := range values {
		cloned[key] = append([]string(nil), value...)
	}
	return cloned
}

func writeCommand(w http.ResponseWriter, command string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		data, _ = json.Marshal(err.Error())
		command = "error"
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", command, data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

type fanout struct {
	mu      sync.Mutex
	next    uint64
	writers map[uint64]io.Writer
}

func newFanout() *fanout {
	return &fanout{writers: make(map[uint64]io.Writer)}
}

func (f *fanout) add(w io.Writer) func() {
	if w == nil {
		return func() {}
	}
	f.mu.Lock()
	id := f.next
	f.next++
	f.writers[id] = w
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.writers, id)
		f.mu.Unlock()
	}
}

func (f *fanout) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, writer := range f.writers {
		_, _ = writer.Write(p)
	}
	return len(p), nil
}

type infoWriter struct {
	mu  sync.Mutex
	w   http.ResponseWriter
	buf bytes.Buffer
}

func (w *infoWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, _ := w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			w.buf.WriteString(line)
			break
		}
		line = strings.TrimSuffix(line, "\n")
		if line != "" {
			writeCommand(w.w, "info", line)
		}
	}
	return n, nil
}

func (w *infoWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		writeCommand(w.w, "info", w.buf.String())
		w.buf.Reset()
	}
}
