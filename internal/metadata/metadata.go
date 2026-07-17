package metadata

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/goplus/llar/mod/module"
)

const installDirTemplate = "{{.InstallDir}}"

type file struct {
	Metadata *string  `json:"metadata"`
	Deps     []string `json:"deps,omitempty"`
}

// Encode creates an artifact metadata file.
func Encode(value, installDir string, deps []module.Version) (json.RawMessage, error) {
	value = normalize(value, installDir)
	encoded := file{Metadata: &value}
	if len(deps) > 0 {
		encoded.Deps = make([]string, 0, len(deps))
		for _, dep := range deps {
			encoded.Deps = append(encoded.Deps, dep.Path+"@"+dep.Version)
		}
	}
	data, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Decode reads an artifact metadata file.
func Decode(data []byte, installDir string) (string, []module.Version, error) {
	var encoded file
	if err := json.Unmarshal(data, &encoded); err != nil {
		return "", nil, err
	}
	if encoded.Metadata == nil {
		return "", nil, fmt.Errorf("metadata is required")
	}
	value, err := expand(*encoded.Metadata, installDir)
	if err != nil {
		return "", nil, err
	}
	deps := make([]module.Version, 0, len(encoded.Deps))
	for _, dep := range encoded.Deps {
		index := strings.LastIndexByte(dep, '@')
		if index <= 0 || index == len(dep)-1 {
			return "", nil, fmt.Errorf("invalid dependency %q", dep)
		}
		deps = append(deps, module.Version{Path: dep[:index], Version: dep[index+1:]})
	}
	return value, deps, nil
}

func normalize(value, installDir string) string {
	pattern := regexp.MustCompile(regexp.QuoteMeta(filepath.Clean(installDir)) + `([/\\]|$)`)
	return pattern.ReplaceAllString(value, installDirTemplate+"${1}")
}

func expand(value, installDir string) (string, error) {
	tmpl, err := template.New("metadata").Parse(value)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	if err := tmpl.Execute(&output, struct{ InstallDir string }{filepath.Clean(installDir)}); err != nil {
		return "", err
	}
	return output.String(), nil
}
