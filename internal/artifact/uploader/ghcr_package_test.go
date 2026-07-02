package uploader

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v68/github"
)

func TestGHCRUploadWritesWhenPackageExists(t *testing.T) {
	client, assertDone := newTestGitHubClient(t, []githubHandler{
		{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusOK, body: `{}`},
	})
	writer := &recordingIndexWriter{}
	u := newGHCR(GHCRConfig{
		Owner:     "MeteorsLiu",
		Token:     "token",
		SourceURL: "https://github.com/MeteorsLiu/llar",
	}, ghcrOptions{
		client:     client,
		writeIndex: writer.write,
	})

	if _, err := u.Upload(context.Background(), bytes.NewReader([]byte("archive")), Options{Name: "madler/zlib", Tag: "v1.3.1"}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if writer.ref != "ghcr.io/meteorsliu/madler/zlib:v1.3.1" {
		t.Fatalf("written ref = %q", writer.ref)
	}
	assertDone()
}

func TestGHCRUploadCreatesPackageAndWaitsBeforeWriting(t *testing.T) {
	client, assertDone := newTestGitHubClient(t, []githubHandler{
		{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
		{method: http.MethodGet, path: "/orgs/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
		{
			method: http.MethodPost,
			path:   "/repos/MeteorsLiu/llar/actions/workflows/ghcr-package-create.yml/dispatches",
			status: http.StatusNoContent,
			checkBody: func(t *testing.T, body string) {
				t.Helper()
				var got struct {
					Ref    string         `json:"ref"`
					Inputs map[string]any `json:"inputs"`
				}
				if err := json.Unmarshal([]byte(body), &got); err != nil {
					t.Fatalf("decode dispatch body: %v", err)
				}
				if got.Ref != "main" {
					t.Fatalf("dispatch ref = %q, want main", got.Ref)
				}
				if got.Inputs["package"] != "madler/zlib" {
					t.Fatalf("dispatch package = %q", got.Inputs["package"])
				}
				if got.Inputs["source_url"] != "https://github.com/MeteorsLiu/llar" {
					t.Fatalf("dispatch source_url = %q", got.Inputs["source_url"])
				}
			},
		},
		{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusOK, body: `{}`},
	})
	writer := &recordingIndexWriter{}
	u := newGHCR(GHCRConfig{
		Owner:     "MeteorsLiu",
		Token:     "token",
		SourceURL: "https://github.com/MeteorsLiu/llar",
	}, ghcrOptions{
		client:     client,
		writeIndex: writer.write,
	})

	if _, err := u.Upload(context.Background(), bytes.NewReader([]byte("archive")), Options{Name: "madler/zlib", Tag: "v1.3.1"}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if writer.ref != "ghcr.io/meteorsliu/madler/zlib:v1.3.1" {
		t.Fatalf("written ref = %q", writer.ref)
	}
	assertDone()
}

func TestGHCRUploadWaitsUntilPackageAppears(t *testing.T) {
	client, assertDone := newTestGitHubClient(t, []githubHandler{
		{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
		{method: http.MethodGet, path: "/orgs/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
		{method: http.MethodPost, path: "/repos/MeteorsLiu/llar/actions/workflows/ghcr-package-create.yml/dispatches", status: http.StatusNoContent},
		{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
		{method: http.MethodGet, path: "/orgs/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusNotFound, body: `{"message":"not found"}`},
		{method: http.MethodGet, path: "/users/MeteorsLiu/packages/container/madler%2Fzlib", status: http.StatusOK, body: `{}`},
	})
	writer := &recordingIndexWriter{}
	sleeps := 0
	u := newGHCR(GHCRConfig{
		Owner:     "MeteorsLiu",
		Token:     "token",
		SourceURL: "https://github.com/MeteorsLiu/llar",
	}, ghcrOptions{
		client:     client,
		writeIndex: writer.write,
		sleep: func(context.Context) error {
			sleeps++
			return nil
		},
	})

	if _, err := u.Upload(context.Background(), bytes.NewReader([]byte("archive")), Options{Name: "madler/zlib", Tag: "v1.3.1"}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if sleeps != 1 {
		t.Fatalf("sleeps = %d, want 1", sleeps)
	}
	if writer.ref != "ghcr.io/meteorsliu/madler/zlib:v1.3.1" {
		t.Fatalf("written ref = %q", writer.ref)
	}
	assertDone()
}

func TestParseGitHubSourceURL(t *testing.T) {
	got, err := parseGitHubSourceURL("https://github.com/MeteorsLiu/llar")
	if err != nil {
		t.Fatalf("parseGitHubSourceURL: %v", err)
	}
	if got != (githubRepository{Owner: "MeteorsLiu", Name: "llar"}) {
		t.Fatalf("repo = %+v", got)
	}
	if _, err := parseGitHubSourceURL("https://example.com/MeteorsLiu/llar"); err == nil {
		t.Fatal("parseGitHubSourceURL error = nil, want error")
	}
}

type githubHandler struct {
	method    string
	path      string
	status    int
	body      string
	checkBody func(*testing.T, string)
}

func newTestGitHubClient(t *testing.T, handlers []githubHandler) (*github.Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		if len(handlers) == 0 {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		}
		next := handlers[0]
		handlers = handlers[1:]
		if r.Method != next.method {
			t.Fatalf("request method = %s, want %s", r.Method, next.method)
		}
		if got := r.URL.EscapedPath(); got != next.path {
			t.Fatalf("request path = %s, want %s", got, next.path)
		}
		var body []byte
		if r.Body != nil {
			var err error
			body, err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
		}
		if next.checkBody != nil {
			next.checkBody(t, string(body))
		}
		if next.status == 0 {
			next.status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.status)
		if next.body != "" {
			_, _ = w.Write([]byte(next.body))
		}
	}))
	client := github.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	client.BaseURL = baseURL
	return client, func() {
		t.Helper()
		server.Close()
		if len(handlers) != 0 {
			remaining := make([]string, 0, len(handlers))
			for _, handler := range handlers {
				remaining = append(remaining, handler.method+" "+handler.path)
			}
			t.Fatalf("remaining handlers = %s", strings.Join(remaining, ", "))
		}
	}
}
