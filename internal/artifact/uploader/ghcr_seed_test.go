package uploader

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGHCRSeedReturnsWhenPackageExists(t *testing.T) {
	client := &fakeGitHubClient{
		t: t,
		handlers: []fakeGitHubHandler{
			{
				method: http.MethodGet,
				path:   "/users/MeteorsLiu/packages/container/madler%2Fzlib",
				status: http.StatusOK,
				body:   `{}`,
			},
		},
	}
	u := NewGHCR(GHCRConfig{
		Owner:          "MeteorsLiu",
		Token:          "token",
		SeedRepository: "MeteorsLiu/llar",
	})
	u.httpClient = client

	if err := u.Seed(context.Background(), Options{Name: "madler/zlib", Tag: "v1.3.1"}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	client.assertDone()
}

func TestGHCRSeedDispatchesAndWaitsForPackage(t *testing.T) {
	client := &fakeGitHubClient{
		t: t,
		handlers: []fakeGitHubHandler{
			{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
			{method: http.MethodGet, path: "/orgs/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
			{
				method: http.MethodPost,
				path:   "/repos/MeteorsLiu/llar/actions/workflows/ghcr-package-seed.yml/dispatches",
				status: http.StatusOK,
				body:   `{"id":123}`,
				checkBody: func(t *testing.T, body string) {
					t.Helper()
					var got struct {
						Ref    string            `json:"ref"`
						Inputs map[string]string `json:"inputs"`
					}
					if err := json.Unmarshal([]byte(body), &got); err != nil {
						t.Fatalf("decode dispatch body: %v", err)
					}
					if got.Ref != "feat/build-cache-backend" {
						t.Fatalf("dispatch ref = %q", got.Ref)
					}
					if got.Inputs["package"] != "madler/zlib" {
						t.Fatalf("dispatch package = %q", got.Inputs["package"])
					}
					if got.Inputs["source_url"] != "https://github.com/MeteorsLiu/llar" {
						t.Fatalf("dispatch source_url = %q", got.Inputs["source_url"])
					}
				},
			},
			{method: http.MethodGet, path: "/repos/MeteorsLiu/llar/actions/runs/123", status: http.StatusOK, body: `{"status":"completed","conclusion":"success"}`},
			{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusOK, body: `{}`},
		},
	}
	u := NewGHCR(GHCRConfig{
		Owner:          "MeteorsLiu",
		Token:          "token",
		SeedRepository: "MeteorsLiu/llar",
		SeedRef:        "feat/build-cache-backend",
	})
	u.httpClient = client

	if err := u.Seed(context.Background(), Options{Name: "madler/zlib", Tag: "v1.3.1"}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	client.assertDone()
}

func TestGHCRSeedContinuesPollingPackageAfterSkippedRun(t *testing.T) {
	client := &fakeGitHubClient{
		t: t,
		handlers: []fakeGitHubHandler{
			{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
			{method: http.MethodGet, path: "/orgs/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
			{method: http.MethodPost, path: "/repos/MeteorsLiu/llar/actions/workflows/ghcr-package-seed.yml/dispatches", status: http.StatusOK, body: `{"id":123}`},
			{method: http.MethodGet, path: "/repos/MeteorsLiu/llar/actions/runs/123", status: http.StatusOK, body: `{"status":"completed","conclusion":"cancelled"}`},
			{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
			{method: http.MethodGet, path: "/orgs/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
			{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusOK, body: `{}`},
		},
	}
	u := NewGHCR(GHCRConfig{
		Owner:          "MeteorsLiu",
		Token:          "token",
		SeedRepository: "MeteorsLiu/llar",
	})
	u.httpClient = client
	sleeps := 0
	u.sleep = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}

	if err := u.Seed(context.Background(), Options{Name: "madler/zlib", Tag: "v1.3.1"}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if sleeps != 1 {
		t.Fatalf("sleeps = %d, want 1", sleeps)
	}
	client.assertDone()
}

type fakeGitHubHandler struct {
	method    string
	path      string
	status    int
	body      string
	checkBody func(*testing.T, string)
}

type fakeGitHubClient struct {
	t        *testing.T
	handlers []fakeGitHubHandler
}

func (c *fakeGitHubClient) Do(req *http.Request) (*http.Response, error) {
	c.t.Helper()
	if len(c.handlers) == 0 {
		c.t.Fatalf("unexpected request %s %s", req.Method, req.URL.EscapedPath())
	}
	next := c.handlers[0]
	c.handlers = c.handlers[1:]
	if req.Method != next.method {
		c.t.Fatalf("request method = %s, want %s", req.Method, next.method)
	}
	if got := req.URL.EscapedPath(); got != next.path {
		c.t.Fatalf("request path = %s, want %s", got, next.path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer token" {
		c.t.Fatalf("authorization = %q", got)
	}
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			c.t.Fatalf("read request body: %v", err)
		}
	}
	if next.checkBody != nil {
		next.checkBody(c.t, string(body))
	}
	return &http.Response{
		StatusCode: next.status,
		Status:     http.StatusText(next.status),
		Body:       io.NopCloser(strings.NewReader(next.body)),
		Header:     make(http.Header),
	}, nil
}

func (c *fakeGitHubClient) assertDone() {
	c.t.Helper()
	if len(c.handlers) != 0 {
		c.t.Fatalf("remaining handlers = %d", len(c.handlers))
	}
}
