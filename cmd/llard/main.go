// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/goplus/llar/internal/artifact"
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
	buildCache := cache.NewKodo(cache.KodoConfig{
		AccessKey:    cfg.accessKey,
		SecretKey:    cfg.secretKey,
		Bucket:       cfg.bucket,
		PublicDomain: cfg.publicDomain,
		Prefix:       cfg.prefix,
		WorkspaceDir: workspaceDir,
		Artifacts:    artifacts,
	})
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
