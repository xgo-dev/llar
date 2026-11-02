// Copyright 2024 The llar Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"context"
	"fmt"
	"io/fs"
	"time"
)

type refInfo struct {
	commit string
	time   time.Time
}

// client defines the internal interface for interacting with code hosting platforms.
type client interface {
	ref(owner, repo, ref string) (refInfo, error)
	ancestors(owner, repo, ref string) ([]string, error)

	// Tags returns all tags from the repository.
	Tags(ctx context.Context, owner, repo string) ([]Tag, error)

	// Latest returns the latest commit hash on the default branch.
	Latest(ctx context.Context, owner, repo string) (string, error)

	// Stat returns file info for the given path.
	Stat(ctx context.Context, owner, repo, ref, path string) (fs.FileInfo, error)

	// ReadFile reads the content of a file.
	ReadFile(ctx context.Context, owner, repo, ref, path string) ([]byte, error)

	// SyncDir downloads a directory to the destination directory.
	SyncDir(ctx context.Context, owner, repo, ref, path, destDir string) error
}

// newClient creates a client for the specified host.
func newClient(host string) (client, error) {
	switch host {
	case "github.com":
		return newGitHubClient(), nil
	default:
		return nil, fmt.Errorf("unsupported host: %s", host)
	}
}
