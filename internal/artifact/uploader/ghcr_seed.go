package uploader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultGHCRSeedWorkflow = "ghcr-package-seed.yml"
const defaultGHCRSeedTag = "llar-seed"

type githubHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type githubRepository struct {
	Owner string
	Name  string
}

func (u *GHCR) Seed(ctx context.Context, opts Options) error {
	if strings.TrimSpace(u.cfg.SeedRepository) == "" {
		return nil
	}
	if strings.TrimSpace(u.cfg.Owner) == "" {
		return errors.New("ghcr seed owner is required")
	}
	if strings.TrimSpace(u.cfg.Token) == "" {
		return errors.New("ghcr seed token is required")
	}

	ref, err := parseGHCRName(opts.Name, opts.Tag, u.cfg.Owner)
	if err != nil {
		return err
	}
	packageName := ghcrPackageName(ref.repo, u.cfg.Owner)
	if packageName == "" {
		return fmt.Errorf("ghcr package name is empty for %q", ref.repo)
	}

	exists, err := u.ghcrPackageExists(ctx, packageName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	runID, err := u.dispatchSeedWorkflow(ctx, packageName)
	if err != nil {
		return err
	}
	if runID != 0 {
		if _, err := u.waitSeedWorkflow(ctx, runID); err != nil {
			return err
		}
	}
	return u.waitGHCRPackage(ctx, packageName)
}

func ghcrPackageName(repo, owner string) string {
	repo = strings.ToLower(strings.Trim(repo, "/"))
	owner = strings.ToLower(strings.Trim(owner, "/"))
	if owner != "" {
		repo = strings.TrimPrefix(repo, owner+"/")
	}
	return repo
}

func (u *GHCR) dispatchSeedWorkflow(ctx context.Context, packageName string) (int64, error) {
	repo, err := parseGitHubRepository(u.cfg.SeedRepository)
	if err != nil {
		return 0, err
	}
	workflow := strings.TrimSpace(u.cfg.SeedWorkflow)
	if workflow == "" {
		workflow = defaultGHCRSeedWorkflow
	}
	ref := strings.TrimSpace(u.cfg.SeedRef)
	if ref == "" {
		ref = "main"
	}
	tag := strings.TrimSpace(u.cfg.SeedTag)
	if tag == "" {
		tag = defaultGHCRSeedTag
	}
	sourceURL := strings.TrimSpace(u.cfg.SeedSourceURL)
	if sourceURL == "" {
		sourceURL = "https://github.com/" + strings.Trim(u.cfg.SeedRepository, "/")
	}

	body := map[string]any{
		"ref": ref,
		"inputs": map[string]string{
			"package":    packageName,
			"tag":        tag,
			"source_url": sourceURL,
		},
		"return_run_details": true,
	}
	var out struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	path := fmt.Sprintf("repos/%s/%s/actions/workflows/%s/dispatches",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(workflow))
	if err := u.doGitHub(ctx, http.MethodPost, path, body, http.StatusOK, http.StatusNoContent, &out); err != nil {
		return 0, err
	}
	if out.ID != 0 {
		return out.ID, nil
	}
	return workflowRunID(out.HTMLURL), nil
}

func workflowRunID(htmlURL string) int64 {
	_, id, ok := strings.Cut(htmlURL, "/actions/runs/")
	if !ok {
		return 0
	}
	if slash := strings.IndexByte(id, '/'); slash >= 0 {
		id = id[:slash]
	}
	runID, _ := strconv.ParseInt(id, 10, 64)
	return runID
}

func (u *GHCR) waitSeedWorkflow(ctx context.Context, runID int64) (bool, error) {
	repo, err := parseGitHubRepository(u.cfg.SeedRepository)
	if err != nil {
		return false, err
	}
	path := fmt.Sprintf("repos/%s/%s/actions/runs/%d",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), runID)
	for {
		var run struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		}
		if err := u.doGitHub(ctx, http.MethodGet, path, nil, http.StatusOK, 0, &run); err != nil {
			return false, err
		}
		if run.Status == "completed" {
			return run.Conclusion == "success", nil
		}
		if err := u.wait(ctx); err != nil {
			return false, err
		}
	}
}

func (u *GHCR) waitGHCRPackage(ctx context.Context, packageName string) error {
	for {
		exists, err := u.ghcrPackageExists(ctx, packageName)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		if err := u.wait(ctx); err != nil {
			return err
		}
	}
}

func (u *GHCR) ghcrPackageExists(ctx context.Context, packageName string) (bool, error) {
	owner := strings.TrimSpace(u.cfg.Owner)
	escapedPackage := url.PathEscape(packageName)
	userExists, userErr := u.githubPackageExists(ctx, fmt.Sprintf("users/%s/packages/container/%s", url.PathEscape(owner), escapedPackage))
	if userExists || userErr != nil {
		return userExists, userErr
	}
	return u.githubPackageExists(ctx, fmt.Sprintf("orgs/%s/packages/container/%s", url.PathEscape(owner), escapedPackage))
}

func (u *GHCR) githubPackageExists(ctx context.Context, path string) (bool, error) {
	err := u.doGitHub(ctx, http.MethodGet, path, nil, http.StatusOK, 0, nil)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errGitHubNotFound) {
		return false, nil
	}
	return false, err
}

var errGitHubNotFound = errors.New("github resource not found")

func (u *GHCR) doGitHub(ctx context.Context, method, path string, body any, okStatus, altOKStatus int, out any) error {
	req, err := u.newGitHubRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	client := u.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != okStatus && (altOKStatus == 0 || resp.StatusCode != altOKStatus) {
		if resp.StatusCode == http.StatusNotFound {
			return errGitHubNotFound
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (u *GHCR) newGitHubRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	base := strings.TrimRight(u.apiBaseURL, "/") + "/"
	if strings.TrimSpace(u.apiBaseURL) == "" {
		base = "https://api.github.com/"
	}
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+strings.TrimLeft(path, "/"), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+u.cfg.Token)
	return req, nil
}

func (u *GHCR) wait(ctx context.Context) error {
	interval := u.cfg.SeedPollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if u.sleep != nil {
		return u.sleep(ctx, interval)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseGitHubRepository(raw string) (githubRepository, error) {
	owner, repo, ok := strings.Cut(strings.Trim(raw, "/"), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return githubRepository{}, fmt.Errorf("github repository must be owner/repo, got %q", raw)
	}
	return githubRepository{Owner: owner, Name: repo}, nil
}
