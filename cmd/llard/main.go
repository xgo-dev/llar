// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/internal/build/cache"
	buildhttp "github.com/goplus/llar/internal/build/http"
	"github.com/goplus/llar/internal/formula/repo"
	"github.com/goplus/llar/internal/vcs"
	"github.com/joho/godotenv"
)

type config struct {
	addr         string
	accessKey    string
	secretKey    string
	bucket       string
	publicDomain string
	prefix       string
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	workspaceDir := filepath.Join(userCacheDir, ".llar", "workspaces")
	if err := os.MkdirAll(workspaceDir, 0700); err != nil {
		return err
	}

	formulaDir, err := repo.DefaultDir()
	if err != nil {
		return err
	}
	formulaRepo, err := vcs.NewRepo("github.com/goplus/llarhub")
	if err != nil {
		return err
	}
	formulaStore := repo.New(formulaDir, formulaRepo)

	artifacts := artifact.NewKodoArtifact(artifact.KodoArtifactConfig{
		AccessKey: cfg.accessKey,
		SecretKey: cfg.secretKey,
		Bucket:    cfg.bucket,
		Prefix:    cfg.prefix,
	})
	// Reuse artifacts already present in the workspace before downloading them
	// from Kodo. The workspace is evictable, so Kodo remains the source of
	// truth and restores anything missing back into the workspace.
	buildCache := readThroughCache{
		local: build.NewLocalCache(workspaceDir),
		remote: cache.NewKodo(cache.KodoConfig{
			AccessKey:    cfg.accessKey,
			SecretKey:    cfg.secretKey,
			Bucket:       cfg.bucket,
			PublicDomain: cfg.publicDomain,
			Prefix:       cfg.prefix,
			WorkspaceDir: workspaceDir,
			Artifacts:    artifacts,
		}),
	}
	handler := buildhttp.New(buildhttp.Options{
		FormulaStore: formulaStore,
		Cache:        buildCache,
		Artifacts:    artifacts,
		WorkspaceDir: workspaceDir,
	})
	server := &http.Server{
		Addr: cfg.addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Printf("request method=%s uri=%s remote=%s", r.Method, r.URL.RequestURI(), r.RemoteAddr)
			handler.ServeHTTP(w, r)
		}),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()

	log.Printf("llard listening on %s", cfg.addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// readThroughCache reuses artifacts already present in the local workspace
// before fetching them from the remote store. The local workspace is only a
// best-effort cache: an installed artifact directory may be removed by a
// cleanup pass, so a local miss falls back to the remote store and a remote
// hit is persisted back into the local cache for later reads.
//
// This is a deliberate copy of the llar client's read-through cache: llard and
// the client evolve separately and must not share an abstraction.
type readThroughCache struct {
	local  cache.Cache
	remote cache.Cache
}

func (c readThroughCache) Get(ctx context.Context, key cache.Key) (cache.Entry, bool, error) {
	entry, ok, err := c.local.Get(ctx, key)
	if err != nil || ok {
		return entry, ok, err
	}
	entry, ok, err = c.remote.Get(ctx, key)
	if err != nil || !ok {
		return entry, ok, err
	}
	// The remote store already restored the artifact into the shared
	// workspace, so the local cache only needs to persist its entry.
	entry, err = c.local.Put(ctx, key, nil, entry)
	if err != nil {
		return cache.Entry{}, false, err
	}
	return entry, true, nil
}

func (c readThroughCache) Put(ctx context.Context, key cache.Key, output fs.FS, entry cache.Entry) (cache.Entry, error) {
	return c.local.Put(ctx, key, output, entry)
}

func loadConfig() (config, error) {
	cfg := config{
		addr:         os.Getenv("LLARD_ADDR"),
		accessKey:    os.Getenv("LLARD_KODO_ACCESS_KEY"),
		secretKey:    os.Getenv("LLARD_KODO_SECRET_KEY"),
		bucket:       os.Getenv("LLARD_KODO_BUCKET"),
		publicDomain: os.Getenv("LLARD_KODO_PUBLIC_DOMAIN"),
		prefix:       os.Getenv("LLARD_KODO_PREFIX"),
	}
	if cfg.addr == "" {
		cfg.addr = ":8080"
	}
	if cfg.accessKey == "" {
		return config{}, errors.New("LLARD_KODO_ACCESS_KEY is required")
	}
	if cfg.secretKey == "" {
		return config{}, errors.New("LLARD_KODO_SECRET_KEY is required")
	}
	if cfg.bucket == "" {
		return config{}, errors.New("LLARD_KODO_BUCKET is required")
	}
	if cfg.publicDomain == "" {
		return config{}, errors.New("LLARD_KODO_PUBLIC_DOMAIN is required")
	}
	return cfg, nil
}
