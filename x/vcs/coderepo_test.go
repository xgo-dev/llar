// Copyright 2018 The Go Authors. All rights reserved.
// Copyright 2026 The XGo Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vcs

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"
)

type repoRefsTest struct {
	path          string
	ref           string
	tagPrefix     string
	version       string
	commit        string
	shortCommit   string
	commitTime    time.Time
	checkBaseTag  bool
	wantBaseTag   string
	wantAfterBase bool
}

var repoRefsTests = []repoRefsTest{
	{
		path:         "github.com/rsc/vgotest1",
		ref:          "v0.0.0",
		version:      "v0.0.0",
		commit:       "80d85c5d4d17598a0e9055e7c175a32b415d6128",
		shortCommit:  "80d85c5d4d17",
		commitTime:   time.Date(2018, 2, 19, 23, 10, 6, 0, time.UTC),
		checkBaseTag: true,
		wantBaseTag:  "v1.0.0",
	},
	{
		path:        "github.com/rsc/vgotest1",
		ref:         "v0.0.0-20180219231006-80d85c5d4d17",
		version:     "v0.0.0-20180219231006-80d85c5d4d17",
		commit:      "80d85c5d4d17598a0e9055e7c175a32b415d6128",
		shortCommit: "80d85c5d4d17",
		commitTime:  time.Date(2018, 2, 19, 23, 10, 6, 0, time.UTC),
	},
	{
		path: "github.com/rsc/vgotest1",
		ref:  "v0.0.1-0.20180219231006-80d85c5d4d17",
	},
	{
		path:        "github.com/rsc/vgotest1",
		ref:         "v1.0.0",
		version:     "v1.0.0",
		commit:      "80d85c5d4d17598a0e9055e7c175a32b415d6128",
		shortCommit: "80d85c5d4d17",
		commitTime:  time.Date(2018, 2, 19, 23, 10, 6, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1/v2",
		ref:         "v2.0.0",
		version:     "v2.0.0",
		commit:      "45f53230a74ad275c7127e117ac46914c8126160",
		shortCommit: "45f53230a74a",
		commitTime:  time.Date(2018, 7, 19, 1, 21, 27, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1",
		ref:         "80d85c5",
		version:     "v1.0.0",
		commit:      "80d85c5d4d17598a0e9055e7c175a32b415d6128",
		shortCommit: "80d85c5d4d17",
		commitTime:  time.Date(2018, 2, 19, 23, 10, 6, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1",
		ref:         "mytag",
		version:     "v1.0.0",
		commit:      "80d85c5d4d17598a0e9055e7c175a32b415d6128",
		shortCommit: "80d85c5d4d17",
		commitTime:  time.Date(2018, 2, 19, 23, 10, 6, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1/v2",
		ref:         "45f53230a",
		version:     "v2.0.0",
		commit:      "45f53230a74ad275c7127e117ac46914c8126160",
		shortCommit: "45f53230a74a",
		commitTime:  time.Date(2018, 7, 19, 1, 21, 27, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1/v54321",
		ref:         "80d85c5",
		version:     "v54321.0.0-20180219231006-80d85c5d4d17",
		commit:      "80d85c5d4d17598a0e9055e7c175a32b415d6128",
		shortCommit: "80d85c5d4d17",
		commitTime:  time.Date(2018, 2, 19, 23, 10, 6, 0, time.UTC),
	},
	{
		path:      "github.com/rsc/vgotest1/submod",
		ref:       "v1.0.0",
		tagPrefix: "submod/",
	},
	{
		path:      "github.com/rsc/vgotest1/submod",
		ref:       "v1.0.3",
		tagPrefix: "submod/",
	},
	{
		path:        "github.com/rsc/vgotest1/submod",
		ref:         "v1.0.4",
		tagPrefix:   "submod/",
		version:     "v1.0.4",
		commit:      "8afe2b2efed96e0880ecd2a69b98a53b8c2738b6",
		shortCommit: "8afe2b2efed9",
		commitTime:  time.Date(2018, 2, 19, 23, 12, 7, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1",
		ref:         "v1.1.0",
		version:     "v1.1.0",
		commit:      "b769f2de407a4db81af9c5de0a06016d60d2ea09",
		shortCommit: "b769f2de407a",
		commitTime:  time.Date(2018, 2, 19, 23, 13, 36, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1/v2",
		ref:         "v2.0.1",
		version:     "v2.0.1",
		commit:      "ea65f87c8f52c15ea68f3bdd9925ef17e20d91e9",
		shortCommit: "ea65f87c8f52",
		commitTime:  time.Date(2018, 2, 19, 23, 14, 23, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1/v2",
		ref:         "v2.0.3",
		version:     "v2.0.3",
		commit:      "f18795870fb14388a21ef3ebc1d75911c8694f31",
		shortCommit: "f18795870fb1",
		commitTime:  time.Date(2018, 2, 19, 23, 16, 4, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1/v2",
		ref:         "v2.0.4",
		version:     "v2.0.4",
		commit:      "1f863feb76bc7029b78b21c5375644838962f88d",
		shortCommit: "1f863feb76bc",
		commitTime:  time.Date(2018, 2, 20, 0, 3, 38, 0, time.UTC),
	},
	{
		path:        "github.com/rsc/vgotest1/v2",
		ref:         "v2.0.5",
		version:     "v2.0.5",
		commit:      "2f615117ce481c8efef46e0cc0b4b4dccfac8fea",
		shortCommit: "2f615117ce48",
		commitTime:  time.Date(2018, 2, 20, 0, 3, 59, 0, time.UTC),
	},
	{
		path:        "rsc.io/quote",
		ref:         "v1.0.0",
		version:     "v1.0.0",
		commit:      "f488df80bcdbd3e5bafdc24ad7d1e79e83edd7e6",
		shortCommit: "f488df80bcdb",
		commitTime:  time.Date(2018, 2, 14, 0, 45, 20, 0, time.UTC),
	},

	{
		path:          "golang.org/x/text",
		ref:           "4e4a3210bb",
		version:       "v0.3.1-0.20180208041248-4e4a3210bb54",
		commit:        "4e4a3210bb54bb31f6ab2cdca2edcc0b50c420c1",
		shortCommit:   "4e4a3210bb54",
		commitTime:    time.Date(2018, 2, 8, 4, 12, 48, 0, time.UTC),
		checkBaseTag:  true,
		wantBaseTag:   "v0.3.0",
		wantAfterBase: true,
	},
	{
		path:        "github.com/pkg/errors",
		ref:         "v0.8.0",
		version:     "v0.8.0",
		commit:      "645ef00459ed84a119197bfb8d8205042c6df63d",
		shortCommit: "645ef00459ed",
		commitTime:  time.Date(2016, 9, 29, 1, 48, 1, 0, time.UTC),
	},

	{
		path: "github.com/rsc/quote/buggy",
		ref:  "c4d4236f",
	},
	{
		path:        "gopkg.in/yaml.v2",
		ref:         "d670f940",
		version:     "v2.0.0",
		commit:      "d670f9405373e636a5a2765eea47fac0c9bc91a4",
		shortCommit: "d670f9405373",
		commitTime:  time.Date(2018, 1, 9, 11, 43, 31, 0, time.UTC),
	},
	{
		path:          "gopkg.in/check.v1",
		ref:           "20d25e280405",
		version:       "v1.0.0-20161208181325-20d25e280405",
		commit:        "20d25e2804050c1cd24a7eea1e7a6447dd0e74ec",
		shortCommit:   "20d25e280405",
		commitTime:    time.Date(2016, 12, 8, 18, 13, 25, 0, time.UTC),
		checkBaseTag:  true,
		wantAfterBase: true,
	},
	{
		path:        "vcs-test.golang.org/go/mod/gitrepo1",
		ref:         "master",
		version:     "v1.2.4-annotated",
		commit:      "ede458df7cd0fdca520df19a33158086a8a68e81",
		shortCommit: "ede458df7cd0",
		commitTime:  time.Date(2018, 4, 17, 19, 43, 22, 0, time.UTC),
	},
	{
		path: "gopkg.in/natefinch/lumberjack.v2",
		// v2.1 is a real tag even though semver.Compare does not consider it valid.
		// Comparing the tag with its commit must still report equality.
		ref:         "v2.1",
		version:     "v2.0.0-20170531160350-a96e63847dc3",
		commit:      "a96e63847dc3c67d17befa69c303767e2f84e54f",
		shortCommit: "a96e63847dc3",
		commitTime:  time.Date(2017, 5, 31, 16, 3, 50, 0, time.UTC),
	},
	{
		path:        "vcs-test.golang.org/go/v2module/v2",
		ref:         "v2.0.0",
		version:     "v2.0.0",
		commit:      "203b91c896acd173aa719e4cdcb7d463c4b090fa",
		shortCommit: "203b91c896ac",
		commitTime:  time.Date(2019, 4, 3, 15, 52, 15, 0, time.UTC),
	},
	{
		path: "vcs-test.golang.org/go/mod/gitrepo1",
		ref:  "v2.3.4+incompatible",
	},
	{
		path: "vcs-test.golang.org/git/semver-branch.git",
		ref:  "v1.0.0",
	},
	{
		path: "vcs-test.golang.org/git/semver-branch.git",
		ref:  "v2.0.0+incompatible",
	},
	{
		path: "vcs-test.golang.org/git/semver-branch.git",
		ref:  "v2.0.0",
	},
	{
		path: "vcs-test.golang.org/git/semver-branch.git",
		ref:  "v3.0.0-devel",
	},
	{
		path:          "vcs-test.golang.org/git/semver-branch.git",
		ref:           "09c4d8f6938c",
		version:       "v0.1.1-0.20220202191944-09c4d8f6938c",
		commit:        "09c4d8f6938c7b5eeae46858a72712b8700fa46a",
		shortCommit:   "09c4d8f6938c",
		commitTime:    time.Date(2022, 2, 2, 19, 19, 44, 0, time.UTC),
		checkBaseTag:  true,
		wantBaseTag:   "v0.1.0",
		wantAfterBase: true,
	},

	// Go excludes v2.0.0 from this root module's pseudo-version base. LLAR compares
	// repository refs without module-path filtering, so the reachable v2.0.0 wins.
	{
		path:          "vcs-test.golang.org/git/v2sub.git",
		ref:           "80beb17a1603",
		version:       "v0.0.0-20220222205507-80beb17a1603",
		commit:        "80beb17a16036f17a5aedd1bb5bd6d407b3c6dc5",
		shortCommit:   "80beb17a1603",
		commitTime:    time.Date(2022, 2, 22, 20, 55, 7, 0, time.UTC),
		checkBaseTag:  true,
		wantBaseTag:   "v2.0.0",
		wantAfterBase: true,
	},
	{
		path: "vcs-test.golang.org/git/v2sub.git",
		ref:  "v2.0.0",
	},
	{
		path: "vcs-test.golang.org/git/v2sub.git",
		ref:  "v2.0.1-0.20220222205507-80beb17a1603",
	},
	{
		path:        "vcs-test.golang.org/git/v2sub.git",
		ref:         "v2.0.0+incompatible",
		version:     "v2.0.0+incompatible",
		commit:      "5fcd3eaeeb391d399f562fd45a50dac9fc34ae8b",
		shortCommit: "5fcd3eaeeb39",
		commitTime:  time.Date(2022, 2, 22, 20, 53, 33, 0, time.UTC),
	},
	{
		path:        "vcs-test.golang.org/git/v2sub.git",
		ref:         "v2.0.1-0.20220222205507-80beb17a1603+incompatible",
		version:     "v2.0.1-0.20220222205507-80beb17a1603+incompatible",
		commit:      "80beb17a16036f17a5aedd1bb5bd6d407b3c6dc5",
		shortCommit: "80beb17a1603",
		commitTime:  time.Date(2022, 2, 22, 20, 55, 7, 0, time.UTC),
	},

	// A tag with build metadata and its commit are the same repository revision.
	{
		path:        "vcs-test.golang.org/git/odd-tags.git",
		ref:         "v0.1.0+build-metadata",
		version:     "v0.1.1-0.20220223184835-9d863d525bbf",
		commit:      "9d863d525bbfcc8eda09364738c4032393711a56",
		shortCommit: "9d863d525bbf",
		commitTime:  time.Date(2022, 2, 23, 18, 48, 35, 0, time.UTC),
	},
	{
		path:         "vcs-test.golang.org/git/odd-tags.git",
		ref:          "9d863d525bbf",
		version:      "v0.1.1-0.20220223184835-9d863d525bbf",
		commit:       "9d863d525bbfcc8eda09364738c4032393711a56",
		shortCommit:  "9d863d525bbf",
		commitTime:   time.Date(2022, 2, 23, 18, 48, 35, 0, time.UTC),
		checkBaseTag: true,
		wantBaseTag:  "v0.1.0+build-metadata",
	},
	{
		path:        "vcs-test.golang.org/git/odd-tags.git",
		ref:         "latest",
		version:     "v0.1.1-0.20220223184835-9d863d525bbf",
		commit:      "9d863d525bbfcc8eda09364738c4032393711a56",
		shortCommit: "9d863d525bbf",
		commitTime:  time.Date(2022, 2, 23, 18, 48, 35, 0, time.UTC),
	},

	// A tag containing +incompatible remains an ordinary repository tag.
	{
		path: "vcs-test.golang.org/git/odd-tags.git",
		ref:  "v2.0.0+incompatible",
	},
	{
		path:         "vcs-test.golang.org/git/odd-tags.git",
		ref:          "12d19af20458",
		version:      "v2.0.1-0.20220223184802-12d19af20458+incompatible",
		commit:       "12d19af204585b0db3d2a876ceddf5b9323f5a4a",
		shortCommit:  "12d19af20458",
		commitTime:   time.Date(2022, 2, 23, 18, 48, 2, 0, time.UTC),
		checkBaseTag: true,
		wantBaseTag:  "v2.0.0+incompatible",
	},

	// A pseudo-version-shaped tag resolves to the tag's commit, not to the commit
	// hash embedded in its text.
	{
		path:        "vcs-test.golang.org/git/odd-tags.git",
		ref:         "v3.0.0-20220223184802-12d19af20458",
		version:     "v3.0.0-20220223184802-12d19af20458+incompatible",
		commit:      "12d19af204585b0db3d2a876ceddf5b9323f5a4a",
		shortCommit: "12d19af20458",
		commitTime:  time.Date(2022, 2, 23, 18, 48, 2, 0, time.UTC),
	},

	// v0.2.2 is on a side branch merged into this commit. It is reachable and
	// therefore wins over v0.2.1 from the first-parent history.
	{
		path:          "vcs-test.golang.org/git/tagtests.git",
		ref:           "c7818c24fa2f",
		version:       "v0.2.3-0.20190509225625-c7818c24fa2f",
		commit:        "c7818c24fa2f3f714c67d0a6d3e411c85a518d1f",
		shortCommit:   "c7818c24fa2f",
		commitTime:    time.Date(2019, 5, 9, 22, 56, 25, 0, time.UTC),
		checkBaseTag:  true,
		wantBaseTag:   "v0.2.2",
		wantAfterBase: true,
	},

	// Only sub/ tags participate in this comparison. The later root tag v0.2.0
	// must not replace the reachable sub/v0.0.10 base.
	{
		path:          "vcs-test.golang.org/git/prefixtagtests.git/sub",
		ref:           "c3ee5d0dfbb9",
		tagPrefix:     "sub/",
		version:       "v0.0.11-0.20190509223500-c3ee5d0dfbb9",
		commit:        "c3ee5d0dfbb9bf3c4d8bb2bce24cd8d14d2d4238",
		shortCommit:   "c3ee5d0dfbb9",
		commitTime:    time.Date(2019, 5, 9, 22, 35, 0, 0, time.UTC),
		checkBaseTag:  true,
		wantBaseTag:   "sub/v0.0.10",
		wantAfterBase: true,
	},
	{
		path:          "vcs-test.golang.org/git/commit-after-tag.git",
		ref:           "b325d8217783",
		version:       "v1.0.1-0.20190715211727-b325d8217783",
		commit:        "b325d821778320fc48ad589fda5db3df61d062a7",
		shortCommit:   "b325d8217783",
		commitTime:    time.Date(2019, 7, 15, 21, 17, 27, 0, time.UTC),
		checkBaseTag:  true,
		wantBaseTag:   "v1.0.0",
		wantAfterBase: true,
	},
	{
		path:          "vcs-test.golang.org/git/no-tags.git",
		ref:           "e706ba1d9f6d",
		version:       "v0.0.0-20190715212047-e706ba1d9f6d",
		commit:        "e706ba1d9f6dc0a5948103cf51e0e840abf00646",
		shortCommit:   "e706ba1d9f6d",
		commitTime:    time.Date(2019, 7, 15, 21, 20, 47, 0, time.UTC),
		checkBaseTag:  true,
		wantAfterBase: true,
	},
}

func TestRepoRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("requires external Git repositories")
	}
	repos := make(map[string]*repo)
	for _, tt := range repoRefsTests {
		tt := tt
		t.Run(strings.NewReplacer("/", "_", "@", "_").Replace(tt.path+"@"+tt.ref), func(t *testing.T) {
			path, ok := repoPath(tt)
			if !ok {
				t.Fatalf("no repository mapping for %q", tt.path)
			}
			r := repos[path]
			if r == nil {
				got, err := NewRepo(path)
				if err != nil {
					t.Fatal(err)
				}
				r = got.(*repo)
				repos[path] = r
			}
			if tt.commit == "" {
				testRepoRefsWithoutCommit(t, r, tt)
				return
			}

			info, err := r.client.ref(r.owner, r.name, tt.commit)
			if err != nil {
				t.Fatalf("resolve commit %q: %v", tt.commit, err)
			}
			if info.commit != tt.commit {
				t.Fatalf("commit = %q, want %q", info.commit, tt.commit)
			}
			if !info.time.Equal(tt.commitTime) {
				t.Fatalf("commit time = %v, want %v", info.time, tt.commitTime)
			}
			if tt.shortCommit != "" {
				short, err := r.client.ref(r.owner, r.name, tt.shortCommit)
				if err != nil {
					t.Fatalf("resolve short commit %q: %v", tt.shortCommit, err)
				}
				if short.commit != tt.commit {
					t.Fatalf("short commit = %q, want %q", short.commit, tt.commit)
				}
			}
			testRepoRefsVersion(t, r, tt)
		})
	}
}

func repoPath(tt repoRefsTest) (string, bool) {
	switch {
	case strings.HasPrefix(tt.path, "github.com/rsc/vgotest1"):
		return "github.com/rsc/vgotest1", true
	case strings.HasPrefix(tt.path, "github.com/rsc/quote"), tt.path == "rsc.io/quote":
		return "github.com/rsc/quote", true
	case tt.path == "golang.org/x/text":
		return "github.com/golang/text", true
	case tt.path == "github.com/pkg/errors":
		return tt.path, true
	case strings.HasPrefix(tt.path, "gopkg.in/yaml.v2"):
		return "github.com/go-yaml/yaml", true
	case tt.path == "gopkg.in/check.v1":
		return "github.com/go-check/check", true
	case tt.path == "gopkg.in/natefinch/lumberjack.v2":
		return "github.com/natefinch/lumberjack", true
	case tt.path == "vcs-test.golang.org/go/mod/gitrepo1":
		return "github.com/MeteorsLiu/llar-vcstest-gitrepo1", true
	case tt.path == "vcs-test.golang.org/go/v2module/v2":
		return "github.com/MeteorsLiu/llar-vcstest-v2repo", true
	case tt.path == "vcs-test.golang.org/git/semver-branch.git":
		return "github.com/MeteorsLiu/llar-vcstest-semver-branch", true
	case tt.path == "vcs-test.golang.org/git/v2sub.git":
		return "github.com/MeteorsLiu/llar-vcstest-v2sub", true
	case tt.path == "vcs-test.golang.org/git/odd-tags.git":
		return "github.com/MeteorsLiu/llar-vcstest-odd-tags", true
	case tt.path == "vcs-test.golang.org/git/tagtests.git":
		return "github.com/MeteorsLiu/llar-vcstest-tagtests", true
	case tt.path == "vcs-test.golang.org/git/prefixtagtests.git/sub":
		return "github.com/MeteorsLiu/llar-vcstest-prefixtagtests", true
	case tt.path == "vcs-test.golang.org/git/commit-after-tag.git":
		return "github.com/MeteorsLiu/llar-vcstest-commit-after-tag", true
	case tt.path == "vcs-test.golang.org/git/no-tags.git":
		return "github.com/MeteorsLiu/llar-vcstest-no-tags", true
	default:
		return "", false
	}
}

func testRepoRefsVersion(t *testing.T, r *repo, tt repoRefsTest) {
	t.Helper()
	tags, err := r.Tags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tagHash := make(map[string]string, len(tags))
	var tagsAtCommit []string
	for _, tag := range tags {
		tagHash[tag.Name] = tag.Hash
		if tag.Hash == tt.commit {
			tagsAtCommit = append(tagsAtCommit, tag.Name)
		}
	}
	compareTag := semver.Compare
	if tt.tagPrefix != "" {
		compareTag = func(a, b string) int {
			left, leftHasPrefix := strings.CutPrefix(a, tt.tagPrefix)
			right, rightHasPrefix := strings.CutPrefix(b, tt.tagPrefix)
			if leftHasPrefix && !rightHasPrefix {
				return 1
			}
			if !leftHasPrefix && rightHasPrefix {
				return -1
			}
			if leftHasPrefix {
				return semver.Compare(left, right)
			}
			return semver.Compare(a, b)
		}
	}

	if tt.checkBaseTag {
		rev, err := r.refs.revision(tt.commit, tagHash)
		if err != nil {
			t.Fatalf("resolve revision %q: %v", tt.commit, err)
		}
		got := comparableRevisionOf(rev, compareTag)
		if got.baseTag != tt.wantBaseTag || got.afterBase != tt.wantAfterBase {
			t.Fatalf("revision %q = {baseTag: %q, afterBase: %t}, want {baseTag: %q, afterBase: %t}", tt.commit, got.baseTag, got.afterBase, tt.wantBaseTag, tt.wantAfterBase)
		}
		if tt.wantBaseTag != "" {
			order := r.Refs().CompareFunc(tt.commit, tt.wantBaseTag, compareTag)
			if tt.wantAfterBase && order <= 0 {
				t.Fatalf("CompareFunc(%q, %q) = %d, want positive", tt.commit, tt.wantBaseTag, order)
			}
			if !tt.wantAfterBase && order != 0 {
				t.Fatalf("CompareFunc(%q, %q) = %d, want 0", tt.commit, tt.wantBaseTag, order)
			}
		}
	}
	if tt.ref == "latest" {
		latest, err := r.Latest(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if latest != tt.commit {
			t.Fatalf("Latest() = %q, want %q", latest, tt.commit)
		}
	}

	if tt.tagPrefix != "" && tt.version != "" {
		tag := tt.tagPrefix + tt.version
		if tagHash[tag] != "" {
			if got := r.Refs().CompareFunc(tt.commit, tag, compareTag); got != 0 {
				t.Fatalf("CompareFunc(%q, %q) = %d, want 0", tt.commit, tag, got)
			}
			return
		}
	}

	if tt.ref == "mytag" {
		if got := r.Refs().CompareFunc(tt.ref, "v1.0.0", semver.Compare); got >= 0 {
			t.Fatalf("LLAR comparator order for mytag = %d, want negative", got)
		}
		if got := r.Refs().CompareFunc(tt.commit, "v1.0.0", semver.Compare); got != 0 {
			t.Fatalf("CompareFunc(%q, v1.0.0) = %d, want 0", tt.commit, got)
		}
		return
	}
	if tt.ref == "master" {
		if got := r.Refs().CompareFunc(tt.ref, tt.version, fallbackCompare); got != 7 {
			t.Fatalf("CompareFunc(%q, %q) = %d, want 7", tt.ref, tt.version, got)
		}
		if got := r.Refs().CompareFunc(tt.commit, tt.version, semver.Compare); got != 0 {
			t.Fatalf("CompareFunc(%q, %q) = %d, want 0", tt.commit, tt.version, got)
		}
		return
	}
	if tt.path == "vcs-test.golang.org/git/odd-tags.git" && strings.HasPrefix(tt.ref, "v3.0.0-") {
		hash := tagHash[tt.ref]
		if hash == "" {
			t.Fatalf("tag %q not found", tt.ref)
		}
		if hash == tt.commit {
			t.Fatalf("pseudo-version-shaped tag unexpectedly points to embedded commit %q", tt.commit)
		}
		if got := r.Refs().CompareFunc(tt.ref, hash, semver.Compare); got != 0 {
			t.Fatalf("CompareFunc(%q, %q) = %d, want 0", tt.ref, hash, got)
		}
		return
	}

	if len(tagsAtCommit) != 0 {
		if tt.version != "" {
			if hash := tagHash[tt.version]; hash != "" {
				if got := r.Refs().CompareFunc(tt.ref, tt.version, semver.Compare); got != 0 {
					t.Fatalf("CompareFunc(%q, %q) = %d, want 0", tt.ref, tt.version, got)
				}
			}
		}
		return
	}
}

func testRepoRefsWithoutCommit(t *testing.T, r *repo, tt repoRefsTest) {
	t.Helper()
	tags, err := r.Tags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ref := tt.ref
	if tt.tagPrefix != "" {
		ref = tt.tagPrefix + ref
	}
	for _, tag := range tags {
		if tag.Name == ref {
			if got := r.Refs().CompareFunc(ref, ref, semver.Compare); got != 0 {
				t.Fatalf("CompareFunc(%q, %q) = %d, want 0", ref, ref, got)
			}
			return
		}
	}
	if strings.HasPrefix(tt.path, "github.com/rsc/quote/") {
		info, err := r.client.ref(r.owner, r.name, tt.ref)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(info.commit, tt.ref) {
			t.Fatalf("commit = %q, want prefix %q", info.commit, tt.ref)
		}
		return
	}
	if got := r.Refs().CompareFunc(ref, "fallback", fallbackCompare); got != 7 {
		t.Fatalf("CompareFunc(%q, fallback) = %d, want 7", ref, got)
	}
}
