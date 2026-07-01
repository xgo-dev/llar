package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type makeJSONResult struct {
	Deps     []makeJSONDep `json:"deps"`
	Metadata string        `json:"metadata"`
}

type makeJSONDep struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

func runLLARMake(ctx context.Context, req Request, info io.Writer) (makeResult, error) {
	dir, err := os.MkdirTemp("", "llar-remote-build-*")
	if err != nil {
		return makeResult{}, err
	}
	defer os.RemoveAll(dir)

	workDir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return makeResult{}, err
	}
	homeDir := filepath.Join(dir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return makeResult{}, err
	}

	archivePath := filepath.Join(dir, "artifact.tar.gz")
	module := req.Target.Module
	cmdDir := workDir
	if localModule, local := localTargetPattern(module); local {
		module = localModule
		if !filepath.IsAbs(module) {
			module, err = filepath.Abs(module)
			if err != nil {
				return makeResult{}, err
			}
		}
		if resolved, err := filepath.EvalSymlinks(module); err == nil {
			module = resolved
		}
		cmdDir = filepath.Dir(module)
	}
	target := module + "@" + req.Target.Version
	cmd := exec.CommandContext(ctx, "llar", "make", "-v", "--json", "-o", archivePath, target)
	cmd.Dir = cmdDir
	cmd.Env = append(os.Environ(), "HOME="+homeDir)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	if info == nil {
		cmd.Stderr = &stderr
	} else {
		cmd.Stderr = io.MultiWriter(&stderr, info)
	}
	if err := cmd.Run(); err != nil {
		return makeResult{}, fmt.Errorf("llar make %s: %w\nstdout:\n%s\nstderr:\n%s", target, err, stdout.String(), stderr.String())
	}
	var result makeJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return makeResult{}, fmt.Errorf("parse llar make json %s: %w\nstdout:\n%s\nstderr:\n%s", target, err, stdout.String(), stderr.String())
	}

	archive, err := os.ReadFile(archivePath)
	if err != nil {
		return makeResult{}, err
	}
	deps := make([]Target, 0, len(result.Deps))
	for _, dep := range result.Deps {
		deps = append(deps, Target{
			Module:  dep.Path,
			Version: dep.Version,
		})
	}
	return makeResult{
		Archive:  bytes.NewReader(archive),
		Type:     "tar.gz",
		Metadata: result.Metadata,
		Deps:     deps,
	}, nil
}
