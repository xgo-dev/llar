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

type llarMaker struct {
	command string
	args    []string
	workDir string
	homeDir string
}

func newLLARMaker(opts Options) maker {
	command := opts.MakeCommand
	if command == "" {
		command = "llar"
	}
	return &llarMaker{
		command: command,
		args:    append([]string(nil), opts.MakeArgs...),
		workDir: opts.MakeWorkDir,
		homeDir: opts.MakeHomeDir,
	}
}

func (m *llarMaker) make(ctx context.Context, req Request, info io.Writer) (makeResult, error) {
	dir, err := os.MkdirTemp("", "llar-remote-build-*")
	if err != nil {
		return makeResult{}, err
	}
	defer os.RemoveAll(dir)

	archivePath := filepath.Join(dir, "artifact.tar.gz")
	target := m.targetArg(req) + "@" + req.Target.Version
	args := append([]string(nil), m.args...)
	args = append(args, "make", "-o", archivePath, target)
	cmd := exec.CommandContext(ctx, m.command, args...)
	if m.workDir != "" {
		cmd.Dir = m.workDir
	}
	if m.homeDir != "" {
		cmd.Env = append(os.Environ(), "HOME="+m.homeDir)
	}

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

func (m *llarMaker) targetArg(req Request) string {
	if m.workDir == "" {
		return req.Target.Module
	}
	localFormula := filepath.Join(m.workDir, filepath.FromSlash(req.Target.Module), "versions.json")
	if _, err := os.Stat(localFormula); err == nil {
		return "./" + filepath.ToSlash(req.Target.Module)
	}
	return req.Target.Module
}
