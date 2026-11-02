// Copyright 2026 The XGo Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type githubRefs struct {
	mu   sync.Mutex
	dir  string
	tags map[string]string
	tips []string
}

func (r *githubRefs) list(ctx context.Context, owner, repo string) ([]Tag, error) {
	defer runtime.KeepAlive(r)
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.load(ctx, owner, repo); err != nil {
		return nil, err
	}
	names := slices.Sorted(maps.Keys(r.tags))
	tags := make([]Tag, len(names))
	for i, name := range names {
		tags[i] = Tag{Name: name, Hash: r.tags[name]}
	}
	return tags, nil
}

func (r *githubRefs) ref(owner, repo, ref string) (refInfo, error) {
	defer runtime.KeepAlive(r)
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.load(context.Background(), owner, repo); err != nil {
		return refInfo{}, err
	}
	if err := r.fetch(owner, repo); err != nil {
		return refInfo{}, err
	}
	output, err := runGit(r.dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return refInfo{}, err
	}
	commit := strings.TrimSpace(string(output))
	timestamp, err := runGit(r.dir, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return refInfo{}, err
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(timestamp)), 10, 64)
	if err != nil {
		return refInfo{}, fmt.Errorf("parse commit time: %w", err)
	}
	return refInfo{commit: commit, time: time.Unix(seconds, 0).UTC()}, nil
}

func (r *githubRefs) ancestors(owner, repo, ref string) ([]string, error) {
	defer runtime.KeepAlive(r)
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.load(context.Background(), owner, repo); err != nil {
		return nil, err
	}
	if err := r.fetch(owner, repo); err != nil {
		return nil, err
	}
	commits, err := gitLines(r.dir, "rev-list", ref)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(commits, func(commit string) bool {
		return commit == ref
	}), nil
}

func (r *githubRefs) load(ctx context.Context, owner, repo string) error {
	if r.tags != nil {
		return nil
	}

	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "--tags", githubRepoURL(owner, repo))
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git ls-remote: %w: %s", err, strings.TrimSpace(string(output)))
	}

	tags := make(map[string]string)
	tips := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		oid, ref, ok := strings.Cut(line, "\t")
		if !ok {
			return fmt.Errorf("invalid ls-remote output: %q", line)
		}
		if _, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			tips[oid] = struct{}{}
		} else if tag, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
			if tag, ok := strings.CutSuffix(tag, "^{}"); ok {
				tags[tag] = oid
			} else if _, ok := tags[tag]; !ok {
				tags[tag] = oid
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, oid := range tags {
		tips[oid] = struct{}{}
	}
	r.tags = tags
	r.tips = slices.Sorted(maps.Keys(tips))
	return nil
}

func (r *githubRefs) fetch(owner, repo string) (err error) {
	if r.dir != "" {
		return nil
	}
	if len(r.tips) == 0 {
		return fmt.Errorf("repository has no branch or tag refs")
	}

	dir, err := os.MkdirTemp("", "llar-vcs-refs-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	if _, err = runGit(dir, "init", "--bare", "."); err != nil {
		return err
	}
	if _, err = runGit(dir, "remote", "add", "origin", githubRepoURL(owner, repo)); err != nil {
		return err
	}
	args := []string{
		"fetch",
		"--quiet",
		"--no-tags",
		"--no-write-fetch-head",
		"--filter=tree:0",
		"origin",
	}
	if _, err = runGit(dir, append(args, r.tips...)...); err != nil {
		return err
	}

	r.dir = dir
	runtime.AddCleanup(r, func(path string) {
		_ = os.RemoveAll(path)
	}, dir)
	return nil
}

func (g *githubClient) ref(owner, repo, ref string) (refInfo, error) {
	return g.refs.ref(owner, repo, ref)
}

func (g *githubClient) ancestors(owner, repo, ref string) ([]string, error) {
	return g.refs.ancestors(owner, repo, ref)
}

func gitLines(dir string, args ...string) ([]string, error) {
	output, err := runGit(dir, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func runGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func githubRepoURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
}
