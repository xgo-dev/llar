// Copyright 2026 The XGo Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"context"
	"slices"
	"strings"
	"time"
)

type repoRefs struct {
	client client
	owner  string
	repo   string
}

type revision struct {
	commitID      string
	commitTime    time.Time
	exactTag      string
	reachableTags []string
	tagsAtCommit  []string
}

type comparableRevision struct {
	commitID   string
	baseTag    string
	afterBase  bool
	commitTime time.Time
}

func (r *repoRefs) CompareFunc(a, b string, compareTag func(a, b string) int) int {
	if a == b {
		return 0
	}
	tags, err := r.client.Tags(context.Background(), r.owner, r.repo)
	if err != nil {
		return compareTag(a, b)
	}
	tagCommits := make(map[string]string, len(tags))
	for _, tag := range tags {
		tagCommits[tag.Name] = tag.Hash
	}
	_, leftIsTag := tagCommits[a]
	_, rightIsTag := tagCommits[b]
	if leftIsTag && rightIsTag {
		return compareTag(a, b)
	}
	left, err := r.revision(a, tagCommits)
	if err != nil {
		return compareTag(a, b)
	}
	right, err := r.revision(b, tagCommits)
	if err != nil {
		return compareTag(a, b)
	}
	return compareRevisions(left, right, compareTag)
}

func (r *repoRefs) revision(ref string, tags map[string]string) (revision, error) {
	if commit, ok := tags[ref]; ok {
		return revision{commitID: commit, exactTag: ref}, nil
	}
	info, err := r.client.ref(r.owner, r.repo, ref)
	if err != nil {
		return revision{}, err
	}
	var tagsAtCommit []string
	for tag, commit := range tags {
		if commit == info.commit {
			tagsAtCommit = append(tagsAtCommit, tag)
		}
	}
	if len(tagsAtCommit) != 0 {
		return revision{
			commitID:      info.commit,
			commitTime:    info.time,
			reachableTags: tagsAtCommit,
			tagsAtCommit:  tagsAtCommit,
		}, nil
	}
	ancestors, err := r.client.ancestors(r.owner, r.repo, info.commit)
	if err != nil {
		return revision{}, err
	}
	ancestorSet := make(map[string]struct{}, len(ancestors))
	for _, ancestor := range ancestors {
		ancestorSet[ancestor] = struct{}{}
	}
	var reachableTags []string
	for tag, commit := range tags {
		if _, ok := ancestorSet[commit]; ok {
			reachableTags = append(reachableTags, tag)
		}
	}
	return revision{
		commitID:      info.commit,
		commitTime:    info.time,
		reachableTags: reachableTags,
	}, nil
}

func compareRevisions(left, right revision, compareTag func(a, b string) int) int {
	a := comparableRevisionOf(left, compareTag)
	b := comparableRevisionOf(right, compareTag)
	if a.baseTag == "" && b.baseTag != "" {
		return -1
	}
	if a.baseTag != "" && b.baseTag == "" {
		return 1
	}
	if a.baseTag != "" {
		if order := compareTag(a.baseTag, b.baseTag); order != 0 {
			return order
		}
	}
	if a.afterBase != b.afterBase {
		if a.afterBase {
			return 1
		}
		return -1
	}
	if !a.afterBase {
		return 0
	}
	if order := a.commitTime.Compare(b.commitTime); order != 0 {
		return order
	}
	return strings.Compare(a.commitID, b.commitID)
}

func comparableRevisionOf(rev revision, compareTag func(a, b string) int) comparableRevision {
	if rev.exactTag != "" {
		return comparableRevision{
			commitID:   rev.commitID,
			baseTag:    rev.exactTag,
			commitTime: rev.commitTime,
		}
	}

	baseTag := ""
	if len(rev.reachableTags) != 0 {
		baseTag = slices.MaxFunc(rev.reachableTags, compareTag)
	}
	return comparableRevision{
		commitID:   rev.commitID,
		baseTag:    baseTag,
		afterBase:  baseTag == "" || !slices.Contains(rev.tagsAtCommit, baseTag),
		commitTime: rev.commitTime,
	}
}
