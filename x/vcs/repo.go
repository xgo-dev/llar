// Copyright 2024 The llar Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
)

// Repo represents a code hosting repository.
// It provides version queries and can create filesystem views for specific refs.
type Repo interface {
	// Tags returns all tags from the repository.
	Tags(ctx context.Context) ([]Tag, error)
	// Latest returns the latest commit hash on the default branch.
	Latest(ctx context.Context) (string, error)
	// Refs returns the repository references used for version comparison.
	Refs() Refs
	// At returns a filesystem view of the repository at the specified ref.
	// The returned fs.FS also implements fs.ReadFileFS and fs.ReadDirFS.
	At(ref, localDir string) fs.FS
	// Sync downloads the specified path to localDir.
	Sync(ctx context.Context, ref, path, localDir string) error
}

// Tag identifies the commit referenced by a repository tag.
type Tag struct {
	Name string
	Hash string
}

// repo is the default implementation of Repo.
type repo struct {
	client client
	host   string
	owner  string
	name   string
	refs   repoRefs
}

// Refs provides version comparison over repository references.
type Refs interface {
	// CompareFunc compares two tags or commits, falling back to compareTag when
	// the references cannot be prepared or resolved.
	CompareFunc(a, b string, compareTag func(a, b string) int) int
}

// NewRepo creates a new Repo for the given repository path.
// repoPath format: "github.com/owner/repo"
func NewRepo(repoPath string) (Repo, error) {
	host, owner, repoName, err := parseRepoPath(repoPath)
	if err != nil {
		return nil, err
	}

	c, err := newClient(host)
	if err != nil {
		return nil, err
	}

	r := &repo{
		client: c,
		host:   host,
		owner:  owner,
		name:   repoName,
		refs: repoRefs{
			client: c,
			owner:  owner,
			repo:   repoName,
		},
	}
	return r, nil
}

// Tags returns all tags from the repository.
func (r *repo) Tags(ctx context.Context) ([]Tag, error) {
	return r.client.Tags(ctx, r.owner, r.name)
}

// Latest returns the latest commit hash on the default branch.
func (r *repo) Latest(ctx context.Context) (string, error) {
	return r.client.Latest(ctx, r.owner, r.name)
}

// Refs returns the repository references used for version comparison.
func (r *repo) Refs() Refs {
	return &r.refs
}

// At returns a filesystem view of the repository at the specified ref.
func (r *repo) At(ref, localDir string) fs.FS {
	return &repoFS{
		client:   r.client,
		owner:    r.owner,
		repoName: r.name,
		ref:      ref,
		localDir: localDir,
	}
}

// Sync downloads the specified path to localDir.
func (r *repo) Sync(ctx context.Context, ref, path, localDir string) error {
	return r.client.SyncDir(ctx, r.owner, r.name, ref, path, localDir)
}

// parseRepoPath parses "github.com/owner/repo" into components.
func parseRepoPath(repoPath string) (host, owner, repo string, err error) {
	parts := strings.Split(repoPath, "/")
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("invalid repo path: %s, expected host/owner/repo", repoPath)
	}
	return parts[0], parts[1], strings.Join(parts[2:], "/"), nil
}
