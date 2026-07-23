package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goplus/llar/internal/artifact/archiver"
	"github.com/goplus/llar/internal/metadata"
	"github.com/goplus/llar/mod/module"
)

type moduleJSONDep struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type moduleJSONResult struct {
	Path     string          `json:"path"`
	Version  string          `json:"version"`
	Deps     []moduleJSONDep `json:"deps,omitempty"`
	Metadata string          `json:"metadata"`
}

type moduleOutputResult struct {
	Module    module.Version
	Deps      []module.Version
	Metadata  string
	OutputDir string
}

func writeModuleResult(output io.Writer, result moduleOutputResult, jsonOutput bool) error {
	if !jsonOutput {
		if result.Metadata != "" {
			_, err := fmt.Fprintln(output, result.Metadata)
			return err
		}
		return nil
	}

	deps := make([]moduleJSONDep, 0, len(result.Deps))
	for _, dep := range result.Deps {
		deps = append(deps, moduleJSONDep{Path: dep.Path, Version: dep.Version})
	}
	return json.NewEncoder(output).Encode(moduleJSONResult{
		Path:     result.Module.Path,
		Version:  result.Module.Version,
		Deps:     deps,
		Metadata: result.Metadata,
	})
}

// writeModuleOutput resolves --output in this order:
//   - An existing directory receives a copy, even if its name has an archive suffix.
//   - A non-directory path ending in .zip or .tar.gz is written as an archive.
//   - Any other missing path is created as a directory and receives a copy.
//
// For example, an existing "zlib.zip/" directory remains a directory, while a
// missing "zlib.zip" path becomes an archive. Directory output is intentional
// and must remain available alongside archive output.
func writeModuleOutput(result moduleOutputResult, dest string) error {
	info, err := os.Stat(dest)
	if err == nil && info.IsDir() {
		return os.CopyFS(dest, os.DirFS(result.OutputDir))
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !strings.HasSuffix(dest, ".zip") && !strings.HasSuffix(dest, ".tar.gz") {
		return os.CopyFS(dest, os.DirFS(result.OutputDir))
	}
	body, err := metadata.Encode(metadata.Info{Metadata: result.Metadata, Deps: result.Deps}, result.OutputDir)
	if err != nil {
		return err
	}
	return archiver.Pack(result.OutputDir, dest, body)
}
