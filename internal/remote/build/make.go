package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	target := req.Target.Module + "@" + req.Target.Version
	cmd := exec.CommandContext(ctx, "llar", "make", "-v", "-o", archivePath, target)
	cmd.Dir = workDir
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

	archive, err := os.ReadFile(archivePath)
	if err != nil {
		return makeResult{}, err
	}
	return makeResult{
		Archive:  bytes.NewReader(archive),
		Type:     "tar.gz",
		Metadata: strings.TrimSpace(stdout.String()),
	}, nil
}
