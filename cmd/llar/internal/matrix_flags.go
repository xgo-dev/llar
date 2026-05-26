package internal

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/goplus/llar/formula"
	goflags "github.com/jessevdk/go-flags"
	"github.com/spf13/cobra"
)

var matrixKeyRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]*$`)

// resolveMatrixStr extracts matrix flags from os.Args and returns the
// encoded matrix string. Falls back to hostMatrixCombo() when no
// matrix flags are present.
func resolveMatrixStr(cmd *cobra.Command) (string, error) {
	startIdx := 0
	for i, a := range os.Args {
		if a == cmd.CalledAs() {
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

// extractMatrixFlags uses go-flags to parse subArgs. All flags enter
// UnknownOptionHandler; known flags (via cmd.Flags().Lookup) are
// skipped, unknown long flags are collected as matrix dimensions.
func extractMatrixFlags(cmd *cobra.Command, subArgs []string) (*formula.Matrix, error) {
	dims := map[string]string{}
	var matrixErr error

	parser := goflags.NewParser(&struct{}{}, goflags.PassDoubleDash)
	parser.UnknownOptionHandler = func(option string, arg goflags.SplitArgument, args []string) ([]string, error) {
		if matrixErr != nil {
			return args, nil
		}

		// Known long flag → skip, consume value if needed
		if f := cmd.Flags().Lookup(option); f != nil {
			if _, hasVal := arg.Value(); !hasVal && f.NoOptDefVal == "" && len(args) > 0 {
				return args[1:], nil
			}
			return args, nil
		}

		// Known short flag → skip
		if len(option) == 1 && cmd.Flags().ShorthandLookup(option) != nil {
			if f := cmd.Flags().ShorthandLookup(option); f.NoOptDefVal == "" && len(args) > 0 {
				return args[1:], nil
			}
			return args, nil
		}

		// Unknown short flag → error
		if len(option) == 1 {
			matrixErr = fmt.Errorf("unknown short flag %q", "-"+option)
			return args, nil
		}

		// Resolve matrix key
		key := option
		if rest, ok := strings.CutPrefix(option, "matrix-"); ok {
			if rest == "" {
				matrixErr = fmt.Errorf("missing matrix key in --matrix-")
				return args, nil
			}
			key = rest
		}

		if !matrixKeyRE.MatchString(key) {
			matrixErr = fmt.Errorf("invalid matrix key %q", key)
			return args, nil
		}

		// Resolve value
		val, hasVal := arg.Value()
		if !hasVal {
			if len(args) == 0 {
				matrixErr = fmt.Errorf("missing value for matrix flag --%s", option)
				return args, nil
			}
			val = args[0]
			args = args[1:]
		}
		if val == "" {
			matrixErr = fmt.Errorf("missing value for matrix flag --%s", option)
			return args, nil
		}
		dims[key] = val
		return args, nil
	}

	parser.ParseArgs(subArgs)
	if matrixErr != nil {
		return nil, matrixErr
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
