package uploader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-github/v68/github"
)

const defaultGHCRCreateWorkflow = "ghcr-package-create.yml"
const defaultGHCRCreateTag = "llar-create"
const defaultGHCRCreateRef = "main"

type githubRepository struct {
	Owner string
	Name  string
}

func ghcrPackageName(repo, owner string) string {
	repo = strings.ToLower(strings.Trim(repo, "/"))
	owner = strings.ToLower(strings.Trim(owner, "/"))
	if owner != "" {
		repo = strings.TrimPrefix(repo, owner+"/")
	}
	return repo
}

func (u *ghcr) createPackage(ctx context.Context, repo githubRepository, packageName string) error {
	_, err := u.client.Actions.CreateWorkflowDispatchEventByFileName(ctx, repo.Owner, repo.Name, defaultGHCRCreateWorkflow, github.CreateWorkflowDispatchEventRequest{
		Ref: defaultGHCRCreateRef,
		Inputs: map[string]interface{}{
			"package":    packageName,
			"tag":        defaultGHCRCreateTag,
			"source_url": strings.TrimSpace(u.cfg.SourceURL),
		},
	})
	return err
}

func (u *ghcr) waitPackage(ctx context.Context, packageName string) error {
	for {
		exists, err := u.packageExists(ctx, packageName)
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

func (u *ghcr) packageExists(ctx context.Context, packageName string) (bool, error) {
	owner := strings.TrimSpace(u.cfg.Owner)
	userPackageName := url.PathEscape(packageName)
	_, _, err := u.client.Users.GetPackage(ctx, owner, "container", userPackageName)
	if err == nil {
		return true, nil
	}
	if !isGitHubNotFound(err) {
		return false, err
	}
	_, _, err = u.client.Organizations.GetPackage(ctx, owner, "container", packageName)
	if err == nil {
		return true, nil
	}
	if isGitHubNotFound(err) {
		return false, nil
	}
	return false, err
}

func isGitHubNotFound(err error) bool {
	var errResp *github.ErrorResponse
	return errors.As(err, &errResp) && errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotFound
}

func (u *ghcr) wait(ctx context.Context) error {
	if u.sleep != nil {
		return u.sleep(ctx)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseGitHubSourceURL(raw string) (githubRepository, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return githubRepository{}, err
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return githubRepository{}, fmt.Errorf("github source url must be https://github.com/owner/repo, got %q", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return githubRepository{}, fmt.Errorf("github source url must be https://github.com/owner/repo, got %q", raw)
	}
	return githubRepository{Owner: parts[0], Name: parts[1]}, nil
}
