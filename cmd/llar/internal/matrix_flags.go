package internal

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/goplus/llar/formula"
	"github.com/spf13/cobra"
)

var matrixKeyRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]*$`)

// resolveMatrixStr extracts matrix flags from os.Args and returns the
// encoded matrix string. Falls back to hostMatrixCombo() when no
// matrix flags are present.
func resolveMatrixStr(cmd *cobra.Command) (string, error) {
	startIdx := 0
	for i, a := range os.Args {
		if a == cmd.Name() {
			startIdx = i + 1
			break
		}
	}
	m, err := extractMatrixFlags(cmd, os.Args[startIdx:])
	if err != nil {
		return "", err
	}
	if m == nil {
		return hostMatrixCombo(), nil
	}
	return m.Combinations()[0], nil
}

// extractMatrixFlags scans subArgs and collects unknown long flags as
// matrix dimensions. Known flags are identified via cmd.Flags().Lookup().
// Returns nil when no matrix flags are found.
func extractMatrixFlags(cmd *cobra.Command, subArgs []string) (*formula.Matrix, error) {
	dims := map[string]string{}

	for i := 0; i < len(subArgs); i++ {
		arg := subArgs[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}

		// Short flag
		if !strings.HasPrefix(arg, "--") {
			c := string(arg[1])
			if f := cmd.Flags().ShorthandLookup(c); f != nil {
				if f.NoOptDefVal == "" {
					i++
				}
				continue
			}
			return nil, fmt.Errorf("unknown short flag %q", arg)
		}

		// Long flag
		body := arg[2:]
		name, val, hasEq := strings.Cut(body, "=")

		// --matrix-<key> → force matrix even if key matches known flag
		matrixKey := ""
		if rest, ok := strings.CutPrefix(name, "matrix-"); ok {
			matrixKey = rest
			if matrixKey == "" {
				return nil, fmt.Errorf("missing matrix key in --matrix-")
			}
		} else if f := cmd.Flags().Lookup(name); f != nil {
			// Known to Cobra → skip
			if !hasEq && f.NoOptDefVal == "" {
				i++
			}
			continue
		} else {
			matrixKey = name
		}

		if !matrixKeyRE.MatchString(matrixKey) {
			return nil, fmt.Errorf("invalid matrix key %q", matrixKey)
		}

		// Resolve value
		if !hasEq {
			if i+1 >= len(subArgs) || strings.HasPrefix(subArgs[i+1], "-") {
				return nil, fmt.Errorf("missing value for matrix flag --%s", name)
			}
			i++
			val = subArgs[i]
		}
		if val == "" {
			return nil, fmt.Errorf("missing value for matrix flag --%s", name)
		}
		dims[matrixKey] = val
	}

	if len(dims) == 0 {
		return nil, nil
	}

	m := &formula.Matrix{
		Require: make(map[string][]string, len(dims)),
	}
	for k, v := range dims {
		m.Require[k] = []string{v}
	}
	return m, nil
}
