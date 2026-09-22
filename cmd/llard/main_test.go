// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/artifact"
	"github.com/goplus/llar/internal/build"
	"github.com/goplus/llar/internal/build/cache"
	"github.com/goplus/llar/mod/module"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("LLARD_ADDR", "127.0.0.1:9000")
	t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
	t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
	t.Setenv("LLARD_KODO_BUCKET", "bucket")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")
	t.Setenv("LLARD_KODO_PREFIX", "artifacts")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:9000" {
		t.Fatalf("addr = %q", cfg.addr)
	}
	if cfg.accessKey != "access" || cfg.secretKey != "secret" || cfg.bucket != "bucket" {
		t.Fatalf("unexpected Kodo credentials: %#v", cfg)
	}
	if cfg.publicDomain != "https://example.com" || cfg.prefix != "artifacts" {
		t.Fatalf("unexpected Kodo location: %#v", cfg)
	}
}

func TestLoadConfigDefaultAddr(t *testing.T) {
	t.Setenv("LLARD_ADDR", "")
	t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
	t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
	t.Setenv("LLARD_KODO_BUCKET", "bucket")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":8080" {
		t.Fatalf("addr = %q, want :8080", cfg.addr)
	}
}

func TestLoadConfigRequiresKodoSettings(t *testing.T) {
	tests := []struct {
		name string
		env  string
		err  string
	}{
		{name: "access key", env: "LLARD_KODO_ACCESS_KEY", err: "LLARD_KODO_ACCESS_KEY is required"},
		{name: "secret key", env: "LLARD_KODO_SECRET_KEY", err: "LLARD_KODO_SECRET_KEY is required"},
		{name: "bucket", env: "LLARD_KODO_BUCKET", err: "LLARD_KODO_BUCKET is required"},
		{name: "public domain", env: "LLARD_KODO_PUBLIC_DOMAIN", err: "LLARD_KODO_PUBLIC_DOMAIN is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
			t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
			t.Setenv("LLARD_KODO_BUCKET", "bucket")
			t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")
			t.Setenv(tt.env, "")

			if _, err := loadConfig(); err == nil || err.Error() != tt.err {
				t.Fatalf("loadConfig error = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestRunRejectsInvalidDotEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("INVALID='unterminated\n"), 0600); err != nil {
		t.Fatal(err)
	}

	err := run()
	if err == nil || !strings.HasPrefix(err.Error(), "load .env: ") {
		t.Fatalf("run error = %v, want .env load error", err)
	}
}

func TestRunRequiresConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("LLARD_KODO_ACCESS_KEY", "")
	t.Setenv("LLARD_KODO_SECRET_KEY", "")
	t.Setenv("LLARD_KODO_BUCKET", "")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "")

	if err := run(); err == nil || err.Error() != "LLARD_KODO_ACCESS_KEY is required" {
		t.Fatalf("run error = %v", err)
	}
}

func TestReadThroughCache_LocalHitSkipsRemote(t *testing.T) {
	workspaceDir := t.TempDir()
	local := build.NewLocalCache(workspaceDir)
	key := cache.Key{Module: module.Version{Path: "madler/zlib", Version: "v1.3.1"}, Matrix: "amd64-linux"}
	if _, err := local.Put(context.Background(), key, nil, cache.Entry{Metadata: "-local"}); err != nil {
		t.Fatal(err)
	}

	remote := &countingCache{}
	c := readThroughCache{local: local, remote: remote, artifacts: presentArtifacts(key)}
	entry, ok, err := c.Get(context.Background(), key)
	if err != nil || !ok || entry.Metadata != "-local" {
		t.Fatalf("Get() = %+v, %v, %v; want local hit", entry, ok, err)
	}
	if remote.gets != 0 {
		t.Fatalf("remote Get calls = %d, want 0", remote.gets)
	}
}

func TestReadThroughCache_PersistsRemoteHit(t *testing.T) {
	workspaceDir := t.TempDir()
	local := build.NewLocalCache(workspaceDir)
	key := cache.Key{Module: module.Version{Path: "madler/zlib", Version: "v1.3.1"}, Matrix: "amd64-linux"}
	remote := &countingCache{entry: cache.Entry{Metadata: "-remote"}, hit: true}

	c := readThroughCache{local: local, remote: remote, artifacts: presentArtifacts(key)}
	for i := 0; i < 2; i++ {
		entry, ok, err := c.Get(context.Background(), key)
		if err != nil || !ok || entry.Metadata != "-remote" {
			t.Fatalf("Get() #%d = %+v, %v, %v", i+1, entry, ok, err)
		}
	}
	if remote.gets != 1 {
		t.Fatalf("remote Get calls = %d, want 1", remote.gets)
	}
}

// TestReadThroughCache_RecordMissingInvalidatesLocal verifies that deleting the
// authoritative artifact record invalidates a local entry: the next Get drops
// the install tree, misses (so the build runs again), and does not consult the
// remote store.
func TestReadThroughCache_RecordMissingInvalidatesLocal(t *testing.T) {
	workspaceDir := t.TempDir()
	local := build.NewLocalCache(workspaceDir)
	key := cache.Key{Module: module.Version{Path: "madler/zlib", Version: "v1.3.1"}, Matrix: "amd64-linux"}
	if _, err := local.Put(context.Background(), key, nil, cache.Entry{Metadata: "-local"}); err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(workspaceDir, "madler", "zlib@v1.3.1-amd64-linux")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}

	remote := &countingCache{entry: cache.Entry{Metadata: "-remote"}, hit: true}
	c := readThroughCache{local: local, remote: remote, artifacts: &fakeArtifacts{}, workspaceDir: workspaceDir}
	if _, ok, err := c.Get(context.Background(), key); err != nil || ok {
		t.Fatalf("Get() = %v, %v; want miss after record deletion", ok, err)
	}
	if remote.gets != 0 {
		t.Fatalf("remote Get calls = %d, want 0", remote.gets)
	}
	if _, err := os.Stat(installDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("install dir still present after record deletion: %v", err)
	}
}

// TestReadThroughCache_PutOrdersRemoteThenLocal pins the write order: the
// artifact is published remotely before the local entry is written.
func TestReadThroughCache_PutOrdersRemoteThenLocal(t *testing.T) {
	var order []string
	c := readThroughCache{
		local:  &orderCache{name: "local", order: &order},
		remote: &orderCache{name: "remote", order: &order},
	}
	key := cache.Key{Module: module.Version{Path: "madler/zlib", Version: "v1.3.1"}, Matrix: "amd64-linux"}

	if _, err := c.Put(context.Background(), key, nil, cache.Entry{Metadata: "-built"}); err != nil {
		t.Fatalf("Put() failed: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"remote", "local"}) {
		t.Fatalf("Put order = %v, want [remote local]", order)
	}
}

// TestReadThroughCache_PutSkipsLocalWhenRemoteFails verifies that a remote
// publish failure does not leave a divergent local entry behind: the next Get
// must restore the canonical artifact from the remote store.
func TestReadThroughCache_PutSkipsLocalWhenRemoteFails(t *testing.T) {
	var order []string
	remoteErr := errors.New("remote put failed")
	c := readThroughCache{
		local:  &orderCache{name: "local", order: &order},
		remote: &orderCache{name: "remote", order: &order, err: remoteErr},
	}
	key := cache.Key{Module: module.Version{Path: "madler/zlib", Version: "v1.3.1"}, Matrix: "amd64-linux"}

	if _, err := c.Put(context.Background(), key, nil, cache.Entry{Metadata: "-built"}); !errors.Is(err, remoteErr) {
		t.Fatalf("Put() error = %v, want %v", err, remoteErr)
	}
	if len(order) != 1 || order[0] != "remote" {
		t.Fatalf("Put order = %v, want [remote] only", order)
	}
}

type orderCache struct {
	name  string
	order *[]string
	err   error
}

func (c *orderCache) Get(context.Context, cache.Key) (cache.Entry, bool, error) {
	return cache.Entry{}, false, nil
}

func (c *orderCache) Put(context.Context, cache.Key, fs.FS, cache.Entry) (cache.Entry, error) {
	*c.order = append(*c.order, c.name)
	return cache.Entry{}, c.err
}

type countingCache struct {
	gets  int
	puts  int
	entry cache.Entry
	hit   bool
	err   error
}

func (c *countingCache) Get(context.Context, cache.Key) (cache.Entry, bool, error) {
	c.gets++
	return c.entry, c.hit, c.err
}

func (c *countingCache) Put(context.Context, cache.Key, fs.FS, cache.Entry) (cache.Entry, error) {
	c.puts++
	return cache.Entry{}, nil
}

func artifactRecordKey(key cache.Key) string {
	return key.Module.Path + "@" + key.Module.Version + "?" + key.Matrix
}

// presentArtifacts returns an artifact store holding the record for key.
func presentArtifacts(key cache.Key) *fakeArtifacts {
	return &fakeArtifacts{record: map[string]artifact.Artifact{artifactRecordKey(key): {}}}
}

type fakeArtifacts struct {
	record map[string]artifact.Artifact
	err    error
}

func (f *fakeArtifacts) Get(_ context.Context, key artifact.Key) (artifact.Artifact, error) {
	if f.err != nil {
		return artifact.Artifact{}, f.err
	}
	if a, ok := f.record[key.Module+"@"+key.Version+"?"+key.MatrixStr]; ok {
		return a, nil
	}
	return artifact.Artifact{}, artifact.ErrNotFound
}

func (f *fakeArtifacts) Put(context.Context, artifact.Key, artifact.Artifact) (artifact.Artifact, error) {
	return artifact.Artifact{}, nil
}

func (f *fakeArtifacts) Delete(context.Context, artifact.Key) error { return nil }

func TestRunRejectsInvalidAddress(t *testing.T) {
	t.Chdir(t.TempDir())
	cacheDir := t.TempDir()
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("LLARD_ADDR", "127.0.0.1:not-a-port")
	t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
	t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
	t.Setenv("LLARD_KODO_BUCKET", "bucket")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")

	if err := run(); err == nil || !strings.Contains(err.Error(), "not-a-port") {
		t.Fatalf("run error = %v, want listen error for not-a-port", err)
	}
}
