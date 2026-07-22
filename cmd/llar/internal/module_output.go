package internal

import (
	"encoding/json"
	"fmt"
	"io"

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
