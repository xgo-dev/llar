// Copyright 2024 The llar Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// repoFS implements fs.FS, fs.ReadFileFS, and fs.ReadDirFS.
type repoFS struct {
	client   client
	owner    string
	repoName string
	ref      string
	localDir string
}

// Open opens the named file for reading (lazy loading).
func (r *repoFS) Open(name string) (fs.File, error) {
	return &repoFile{
		name:   name,
		local:  filepath.Join(r.localDir, name),
		client: r.client,
		owner:  r.owner,
		repo:   r.repoName,
		ref:    r.ref,
	}, nil
}

// ReadFile reads the content of a file.
func (r *repoFS) ReadFile(name string) ([]byte, error) {
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// ReadDir reads the contents of a directory.
func (r *repoFS) ReadDir(name string) ([]fs.DirEntry, error) {
	local := filepath.Join(r.localDir, name)

	// Local directory exists and is not empty, read directly
	if entries, err := os.ReadDir(local); err == nil && len(entries) > 0 {
		return entries, nil
	}

	// Download the entire directory
	ctx := context.Background()
	if err := r.client.SyncDir(ctx, r.owner, r.repoName, r.ref, name, local); err != nil {
		return nil, err
	}

	return os.ReadDir(local)
}

// repoFile implements fs.File with lazy loading.
type repoFile struct {
	name   string
	local  string
	client client
	owner  string
	repo   string
	ref    string

	once   sync.Once
	reader *bytes.Reader
	err    error
}

func (f *repoFile) load() {
	// Try local first
	if data, err := os.ReadFile(f.local); err == nil {
		f.reader = bytes.NewReader(data)
		return
	}

	// Fetch from remote
	ctx := context.Background()
	data, err := f.client.ReadFile(ctx, f.owner, f.repo, f.ref, f.name)
	if err != nil {
		f.err = err
		return
	}

	// Save to local
	if err := os.MkdirAll(filepath.Dir(f.local), 0755); err != nil {
		f.err = err
		return
	}
	if err := os.WriteFile(f.local, data, 0644); err != nil {
		f.err = err
		return
	}

	f.reader = bytes.NewReader(data)
}

func (f *repoFile) Read(p []byte) (int, error) {
	f.once.Do(f.load)
	if f.err != nil {
		return 0, f.err
	}
	return f.reader.Read(p)
}

func (f *repoFile) Stat() (fs.FileInfo, error) {
	// Try local first
	if info, err := os.Stat(f.local); err == nil {
		return info, nil
	}

	// Fetch from remote
	ctx := context.Background()
	return f.client.Stat(ctx, f.owner, f.repo, f.ref, f.name)
}

func (f *repoFile) Close() error { return nil }
