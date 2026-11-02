// Copyright 2026 The XGo Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"
)

func TestCompareRevisions(t *testing.T) {
	oldTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	revisions := map[string]revision{
		"v1.0.0": {
			commitID: "1000",
			exactTag: "v1.0.0",
		},
		"v2.0.0": {
			commitID: "2000",
			exactTag: "v2.0.0",
		},
		"after-v1-old": {
			commitID:      "1100",
			commitTime:    oldTime,
			reachableTags: []string{"v1.0.0"},
		},
		"after-v1-new": {
			commitID:      "1200",
			commitTime:    newTime,
			reachableTags: []string{"v1.0.0"},
		},
		"after-v2-old": {
			commitID:      "2100",
			commitTime:    oldTime,
			reachableTags: []string{"v1.0.0", "v2.0.0"},
		},
		"at-v2": {
			commitID:      "2000",
			commitTime:    oldTime,
			reachableTags: []string{"v1.0.0", "v2.0.0"},
			tagsAtCommit:  []string{"v2.0.0"},
		},
		"untagged-old": {
			commitID:   "0100",
			commitTime: oldTime,
		},
		"untagged-new": {
			commitID:   "0200",
			commitTime: newTime,
		},
		"same-time-low": {
			commitID:      "1300",
			commitTime:    newTime,
			reachableTags: []string{"v1.0.0"},
		},
		"same-time-high": {
			commitID:      "1400",
			commitTime:    newTime,
			reachableTags: []string{"v1.0.0"},
		},
	}

	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "tag before descendant", a: "v1.0.0", b: "after-v1-old", want: -1},
		{name: "same base by time", a: "after-v1-old", b: "after-v1-new", want: -1},
		{name: "base tag before time", a: "after-v1-new", b: "after-v2-old", want: -1},
		{name: "commit at tag equals tag", a: "at-v2", b: "v2.0.0", want: 0},
		{name: "untagged before tagged", a: "untagged-new", b: "v1.0.0", want: -1},
		{name: "untagged by time", a: "untagged-old", b: "untagged-new", want: -1},
		{name: "same time by commit", a: "same-time-low", b: "same-time-high", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareRevisions(revisions[tt.a], revisions[tt.b], semver.Compare)
			if got < 0 && tt.want >= 0 || got == 0 && tt.want != 0 || got > 0 && tt.want <= 0 {
				t.Fatalf("compareRevisions(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}
			if reverse := compareRevisions(revisions[tt.b], revisions[tt.a], semver.Compare); got != -reverse {
				t.Fatalf("reverse comparison = %d, want %d", reverse, -got)
			}
		})
	}
}

func TestCompareRevisionsUsesTagComparator(t *testing.T) {
	order := map[string]int{
		"release-one": 1,
		"release-two": 2,
	}
	compareTag := func(a, b string) int {
		return order[a] - order[b]
	}
	left := revision{
		commitID:      "1000",
		reachableTags: []string{"release-one"},
	}
	right := revision{
		commitID:      "2000",
		reachableTags: []string{"release-one", "release-two"},
	}

	if got := compareRevisions(left, right, compareTag); got >= 0 {
		t.Fatalf("comparison with custom tag order = %d, want negative", got)
	}
}

func TestCompareGitRefs(t *testing.T) {
	dir := t.TempDir()
	mustRunGit(t, dir, nil, "init")
	mustRunGit(t, dir, nil, "config", "user.name", "LLAR Test")
	mustRunGit(t, dir, nil, "config", "user.email", "llar@example.com")

	commitFile(t, dir, "first\n", "2026-01-02T03:04:05Z")
	mustRunGit(t, dir, nil, "tag", "v1.0.0")
	secondID := commitFile(t, dir, "second\n", "2026-02-03T04:05:06Z")
	thirdID := commitFile(t, dir, "third\n", "2026-03-04T05:06:07Z")
	mustRunGit(t, dir, nil, "tag", "-a", "v2.0.0", "-m", "v2.0.0")

	useLocalGitRemote(t, dir)
	logFile := installTestGit(t, "", nil, false)
	repo := newTestRepo()
	defer func() {
		_ = os.RemoveAll(githubRefsOf(repo).dir)
		runtime.KeepAlive(repo)
	}()
	if got := repo.Refs().CompareFunc("v1.0.0", "v2.0.0", semver.Compare); got >= 0 {
		t.Fatalf("tag comparison = %d, want negative", got)
	}
	if got := countGitCalls(t, logFile, "fetch"); got != 0 {
		t.Fatalf("fetch calls after tag comparison = %d, want 0", got)
	}
	if got := repo.Refs().CompareFunc("v1.0.0", secondID[:4], semver.Compare); got >= 0 {
		t.Fatalf("tag and descendant comparison = %d, want negative", got)
	}
	if got := repo.Refs().CompareFunc(secondID, thirdID, semver.Compare); got >= 0 {
		t.Fatalf("commit time comparison = %d, want negative", got)
	}
	if got := repo.Refs().CompareFunc(thirdID, "v2.0.0", semver.Compare); got != 0 {
		t.Fatalf("commit at tag comparison = %d, want 0", got)
	}
	if got := countGitCalls(t, logFile, "fetch"); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
	if got := countGitCalls(t, logFile, "ls-remote"); got != 1 {
		t.Fatalf("ls-remote calls = %d, want 1", got)
	}
	if _, err := runGit(githubRefsOf(repo).dir, "show-ref"); err == nil {
		t.Fatal("fetched refs were written to the local repository")
	}
}

func TestLoadRefs(t *testing.T) {
	headOID := strings.Repeat("1", 40)
	tagObjectOID := strings.Repeat("2", 40)
	tagCommitOID := strings.Repeat("3", 40)
	output := strings.Join([]string{
		headOID + "\trefs/heads/main",
		headOID + "\trefs/tags/v1.0.0",
		tagCommitOID + "\trefs/tags/v2.0.0^{}",
		tagObjectOID + "\trefs/tags/v2.0.0",
	}, "\n")

	repo := newTestRepo()
	installTestGit(t, "ls-remote", []byte(output), false)
	tags, err := repo.Tags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := tags[0].Hash; tags[0].Name != "v1.0.0" || got != headOID {
		t.Fatalf("v1.0.0 commit = %q, want %q", got, headOID)
	}
	if got := tags[1].Hash; tags[1].Name != "v2.0.0" || got != tagCommitOID {
		t.Fatalf("v2.0.0 commit = %q, want peeled %q", got, tagCommitOID)
	}
	if got, want := strings.Join(githubRefsOf(repo).tips, ","), strings.Join([]string{headOID, tagCommitOID}, ","); got != want {
		t.Fatalf("tips = %q, want %q", got, want)
	}
}

func TestLoadRefsRejectsInvalidOutput(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr string
	}{
		{name: "invalid line", output: "invalid", wantErr: "invalid ls-remote output"},
		{name: "line too long", output: strings.Repeat("x", bufio.MaxScanTokenSize+1), wantErr: "token too long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestRepo()
			installTestGit(t, "ls-remote", []byte(tt.output), false)
			_, err := repo.Tags(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("loadRefs error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCompareHexadecimalTag(t *testing.T) {
	dir := t.TempDir()
	mustRunGit(t, dir, nil, "init")
	mustRunGit(t, dir, nil, "config", "user.name", "LLAR Test")
	mustRunGit(t, dir, nil, "config", "user.email", "llar@example.com")
	commitID := commitFile(t, dir, "first\n", "2026-01-02T03:04:05Z")
	mustRunGit(t, dir, nil, "tag", "deadbeef")

	useLocalGitRemote(t, dir)
	repo := newTestRepo()
	defer func() {
		_ = os.RemoveAll(githubRefsOf(repo).dir)
		runtime.KeepAlive(repo)
	}()
	if got := repo.Refs().CompareFunc("deadbeef", commitID, strings.Compare); got != 0 {
		t.Fatalf("hexadecimal tag and its commit comparison = %d, want 0", got)
	}
}

func TestCompareCommitPrefersTagsAtCommit(t *testing.T) {
	dir := t.TempDir()
	mustRunGit(t, dir, nil, "init")
	mustRunGit(t, dir, nil, "config", "user.name", "LLAR Test")
	mustRunGit(t, dir, nil, "config", "user.email", "llar@example.com")

	commitFile(t, dir, "first\n", "2026-01-02T03:04:05Z")
	mustRunGit(t, dir, nil, "tag", "v2.0.0")
	commitID := commitFile(t, dir, "second\n", "2026-02-03T04:05:06Z")
	mustRunGit(t, dir, nil, "tag", "v1.0.0")
	mustRunGit(t, dir, nil, "tag", "v1.1.0")

	useLocalGitRemote(t, dir)
	logFile := installTestGit(t, "", nil, false)
	repo := newTestRepo()
	defer func() {
		_ = os.RemoveAll(githubRefsOf(repo).dir)
		runtime.KeepAlive(repo)
	}()
	if got := repo.Refs().CompareFunc(commitID, "v1.1.0", semver.Compare); got != 0 {
		t.Fatalf("commit and highest tag at commit comparison = %d, want 0", got)
	}
	if got := repo.Refs().CompareFunc(commitID, "v2.0.0", semver.Compare); got >= 0 {
		t.Fatalf("commit and higher ancestor tag comparison = %d, want negative", got)
	}
	if got := countGitCalls(t, logFile, "rev-list"); got != 0 {
		t.Fatalf("rev-list calls = %d, want 0", got)
	}
}

func TestCompareSameVersion(t *testing.T) {
	version := strings.Repeat("a", 40)
	if got := newTestRepo().Refs().CompareFunc(version, version, semver.Compare); got != 0 {
		t.Fatalf("CompareFunc(same, same) = %d, want 0", got)
	}
}

func TestCompareFallsBackOnTempDirError(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	repo := newTestRepo()
	useLocalGitRemote(t, dir)
	if _, err := repo.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}

	tmpRoot := t.TempDir()
	tmpFile := filepath.Join(tmpRoot, "not-a-directory")
	if err := os.WriteFile(tmpFile, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpFile)

	if got := repo.Refs().CompareFunc("v1.0.0", commitID, fallbackCompare); got != 7 {
		t.Fatalf("comparison = %d, want fallback result 7", got)
	}
}

func TestCompareFallsBackOnLoadRefsError(t *testing.T) {
	installTestGit(t, "ls-remote", nil, true)
	if got := newTestRepo().Refs().CompareFunc("left", "right", fallbackCompare); got != 7 {
		t.Fatalf("comparison = %d, want fallback result 7", got)
	}
}

func TestCompareFallsBackWithoutRemoteRefs(t *testing.T) {
	installTestGit(t, "ls-remote", nil, false)
	if got := newTestRepo().Refs().CompareFunc("left", "right", fallbackCompare); got != 7 {
		t.Fatalf("comparison = %d, want fallback result 7", got)
	}
}

func TestCompareFallsBackOnFetchErrors(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	tests := []struct {
		name string
		arg  string
	}{
		{name: "init", arg: "init"},
		{name: "remote add", arg: "remote"},
		{name: "fetch", arg: "fetch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useLocalGitRemote(t, dir)
			installTestGit(t, tt.arg, nil, true)
			tmpRoot := t.TempDir()
			t.Setenv("TMPDIR", tmpRoot)
			if got := newTestRepo().Refs().CompareFunc("v1.0.0", commitID, fallbackCompare); got != 7 {
				t.Fatalf("comparison = %d, want fallback result 7", got)
			}
			entries, err := os.ReadDir(tmpRoot)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary refs directory was not removed: %v", entries)
			}
		})
	}
}

func TestCompareFallsBackOnRevisionErrors(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")

	t.Run("left revision", func(t *testing.T) {
		useLocalGitRemote(t, dir)
		repo := newTestRepo()
		defer func() {
			_ = os.RemoveAll(githubRefsOf(repo).dir)
			runtime.KeepAlive(repo)
		}()
		left := strings.Repeat("a", 40)
		if got := repo.Refs().CompareFunc(left, "v1.0.0", fallbackCompare); got != 7 {
			t.Fatalf("comparison = %d, want fallback result 7", got)
		}
	})

	t.Run("right revision", func(t *testing.T) {
		useLocalGitRemote(t, dir)
		repo := newTestRepo()
		defer func() {
			_ = os.RemoveAll(githubRefsOf(repo).dir)
			runtime.KeepAlive(repo)
		}()
		right := strings.Repeat("b", 40)
		if got := repo.Refs().CompareFunc(commitID, right, fallbackCompare); got != 7 {
			t.Fatalf("comparison = %d, want fallback result 7", got)
		}
	})
}

func TestCompareFallsBackOnAncestorsError(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	useLocalGitRemote(t, dir)
	installTestGit(t, "rev-list", nil, true)
	repo := newTestRepo()
	defer func() {
		_ = os.RemoveAll(githubRefsOf(repo).dir)
		runtime.KeepAlive(repo)
	}()
	if got := repo.Refs().CompareFunc(commitID, "v1.0.0", fallbackCompare); got != 7 {
		t.Fatalf("comparison = %d, want fallback result 7", got)
	}
}

func TestGitHubRefErrors(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")

	tests := []struct {
		name     string
		ref      string
		command  string
		output   []byte
		fail     bool
		wantText string
	}{
		{
			name:     "resolve commit",
			ref:      "missing",
			command:  "rev-parse",
			fail:     true,
			wantText: "injected failure",
		},
		{
			name:     "show",
			ref:      "v1.0.0",
			command:  "show",
			fail:     true,
			wantText: "injected failure",
		},
		{
			name:     "invalid timestamp",
			ref:      "v1.0.0",
			command:  "show",
			output:   []byte("not-a-timestamp"),
			wantText: "parse commit time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installTestGit(t, tt.command, tt.output, tt.fail)
			repo := newTestRepo()
			refs := githubRefsOf(repo)
			refs.dir = dir
			refs.tags = map[string]string{"v1.0.0": commitID}
			if _, err := repo.client.ref(repo.owner, repo.name, tt.ref); err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("ref error = %v, want %q", err, tt.wantText)
			}
		})
	}
}

func TestGitHubRefsLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		call func(client) error
	}{
		{
			name: "ref",
			call: func(c client) error {
				_, err := c.ref("owner", "repo", "deadbeef")
				return err
			},
		},
		{
			name: "ancestors",
			call: func(c client) error {
				_, err := c.ancestors("owner", "repo", "deadbeef")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installTestGit(t, "ls-remote", nil, true)
			repo := newTestRepo()
			if err := tt.call(repo.client); err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("error = %v, want injected failure", err)
			}
		})
	}
}

func TestGitHubAncestors(t *testing.T) {
	dir := newHistoryRepo(t)
	parentID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	commitID := commitFile(t, dir, "second\n", "2026-02-03T04:05:06Z")
	repo := newTestRepo()
	refs := githubRefsOf(repo)
	refs.dir = dir
	refs.tags = map[string]string{"v1.0.0": parentID}

	ancestors, err := repo.client.ancestors(repo.owner, repo.name, commitID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ancestors, commitID) {
		t.Fatalf("ancestors contain commit itself %q", commitID)
	}
	if !slices.Contains(ancestors, parentID) {
		t.Fatalf("ancestors %v do not contain parent %q", ancestors, parentID)
	}
}

func TestGitHubAncestorsError(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	repo := newTestRepo()
	refs := githubRefsOf(repo)
	refs.dir = dir
	refs.tags = map[string]string{"v1.0.0": commitID}
	installTestGit(t, "rev-list", nil, true)
	if _, err := repo.client.ancestors(repo.owner, repo.name, commitID); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("ancestors error = %v, want injected failure", err)
	}
}

func TestGitHubAncestorsFetchError(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	useLocalGitRemote(t, dir)
	installTestGit(t, "fetch", nil, true)
	repo := newTestRepo()
	if _, err := repo.client.ancestors(repo.owner, repo.name, commitID); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("ancestors error = %v, want injected failure", err)
	}
}

func TestCompareFetchesOnceConcurrently(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	repo := newTestRepo()
	defer func() {
		_ = os.RemoveAll(githubRefsOf(repo).dir)
		runtime.KeepAlive(repo)
	}()
	useLocalGitRemote(t, dir)
	logFile := installTestGit(t, "", nil, false)

	const comparisons = 8
	errs := make(chan error, comparisons)
	for range comparisons {
		go func() {
			if got := repo.Refs().CompareFunc("v1.0.0", commitID[:4], semver.Compare); got != 0 {
				errs <- fmt.Errorf("comparison = %d, want 0", got)
				return
			}
			errs <- nil
		}()
	}
	for range comparisons {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := countGitCalls(t, logFile, "fetch"); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
	if got := countGitCalls(t, logFile, "ls-remote"); got != 1 {
		t.Fatalf("ls-remote calls = %d, want 1", got)
	}
}

func TestCompareRefsCleanup(t *testing.T) {
	dir := newHistoryRepo(t)
	commitID := mustRunGit(t, dir, nil, "rev-parse", "HEAD")
	useLocalGitRemote(t, dir)
	var refsDir string
	func() {
		repo := newTestRepo()
		if got := repo.Refs().CompareFunc("v1.0.0", commitID, semver.Compare); got != 0 {
			t.Fatalf("comparison = %d, want 0", got)
		}
		refsDir = githubRefsOf(repo).dir
		runtime.KeepAlive(repo)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		if _, err := os.Stat(refsDir); os.IsNotExist(err) {
			return
		}
	}
	_ = os.RemoveAll(refsDir)
	t.Fatalf("temporary refs directory %q was not removed", refsDir)
}

func TestGitLinesEmptyOutput(t *testing.T) {
	installTestGit(t, "tag", nil, false)
	lines, err := gitLines(t.TempDir(), "tag")
	if err != nil {
		t.Fatal(err)
	}
	if lines != nil {
		t.Fatalf("gitLines returned %#v, want nil", lines)
	}
}

func newHistoryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustRunGit(t, dir, nil, "init")
	mustRunGit(t, dir, nil, "config", "user.name", "LLAR Test")
	mustRunGit(t, dir, nil, "config", "user.email", "llar@example.com")
	commitFile(t, dir, "first\n", "2026-01-02T03:04:05Z")
	mustRunGit(t, dir, nil, "tag", "v1.0.0")
	return dir
}

func newTestRepo() *repo {
	c := newGitHubClient()
	return &repo{
		client: c,
		host:   "github.com",
		owner:  "owner",
		name:   "repo",
		refs: repoRefs{
			client: c,
			owner:  "owner",
			repo:   "repo",
		},
	}
}

func githubRefsOf(repo *repo) *githubRefs {
	return &repo.client.(*githubClient).refs
}

func commitFile(t *testing.T, dir, content, date string) string {
	t.Helper()
	if err := os.WriteFile(dir+"/source.txt", []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, dir, nil, "add", "source.txt")
	env := []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	mustRunGit(t, dir, env, "commit", "-m", strings.TrimSpace(content))
	return mustRunGit(t, dir, nil, "rev-parse", "HEAD")
}

func fallbackCompare(_, _ string) int {
	return 7
}

func mustRunGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func useLocalGitRemote(t *testing.T, remote string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+remote+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", githubRepoURL("owner", "repo"))
}

func installTestGit(t *testing.T, command string, output []byte, fail bool) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	outputFile := filepath.Join(dir, "output")
	if err := os.WriteFile(outputFile, output, 0644); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "commands")
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$LLAR_TEST_GIT_LOG"
if [ "$1" = "$LLAR_TEST_GIT_COMMAND" ] && [ -n "$LLAR_TEST_GIT_COMMAND" ]; then
	if [ "$LLAR_TEST_GIT_FAIL" = "1" ]; then
		printf 'injected failure' >&2
		exit 1
	fi
	cat "$LLAR_TEST_GIT_OUTPUT"
	exit 0
fi
exec "$LLAR_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LLAR_TEST_REAL_GIT", realGit)
	t.Setenv("LLAR_TEST_GIT_COMMAND", command)
	t.Setenv("LLAR_TEST_GIT_OUTPUT", outputFile)
	t.Setenv("LLAR_TEST_GIT_LOG", logFile)
	if fail {
		t.Setenv("LLAR_TEST_GIT_FAIL", "1")
	} else {
		t.Setenv("LLAR_TEST_GIT_FAIL", "")
	}
	return logFile
}

func countGitCalls(t *testing.T, logFile, command string) int {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if scanner.Text() == command {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return count
}
