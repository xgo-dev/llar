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
	depth := len(strings.Fields(cmd.CommandPath()))
	m, err := extractMatrixFlags(cmd, os.Args[depth:])
	if err != nil {
		return "", err
	}
	if m == nil {
		return hostMatrixCombo(), nil
	}
	return m.Combinations()[0], nil
}

// extractMatrixFlags uses go-flags to parse registered matrix assignment flags
// and collect unknown long flags as shortcut require dimensions.
func extractMatrixFlags(cmd *cobra.Command, subArgs []string) (*formula.Matrix, error) {
	var opts struct {
		Require []string `long:"require"`
		Option  []string `long:"option"`
	}
	m := &formula.Matrix{}
	var matrixErr error

	parser := goflags.NewParser(&opts, goflags.PassDoubleDash)
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
		if len(option) == 1 {
			if f := cmd.Flags().ShorthandLookup(option); f != nil {
				if f.NoOptDefVal == "" && len(args) > 0 {
					return args[1:], nil
				}
				return args, nil
			}
			matrixErr = fmt.Errorf("unknown short flag %q", "-"+option)
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

		if !matrixKeyRE.MatchString(option) {
			matrixErr = fmt.Errorf("invalid matrix key %q", option)
			return args, nil
		}
		if m.Require == nil {
			m.Require = map[string][]string{}
		}
		m.Require[option] = []string{val}
		return args, nil
	}

	_, err := parser.ParseArgs(subArgs)
	if err != nil {
		return nil, err
	}
	if matrixErr != nil {
		return nil, matrixErr
	}

	applyAssignments := func(flag string, assignments []string, target *map[string][]string) error {
		for _, assignment := range assignments {
			key, val, err := splitMatrixAssignment(flag, assignment)
			if err != nil {
				return err
			}
			if !matrixKeyRE.MatchString(key) {
				return fmt.Errorf("invalid matrix key %q", key)
			}
			if *target == nil {
				*target = map[string][]string{}
			}
			(*target)[key] = []string{val}
		}
		return nil
	}
	if err := applyAssignments("require", opts.Require, &m.Require); err != nil {
		return nil, err
	}
	if err := applyAssignments("option", opts.Option, &m.Options); err != nil {
		return nil, err
	}

	if len(m.Require) == 0 && len(m.Options) == 0 {
		return nil, nil
	}
	return m, nil
}

func splitMatrixAssignment(flag, assignment string) (string, string, error) {
	key, val, ok := strings.Cut(assignment, "=")
	if !ok {
		return "", "", fmt.Errorf("invalid matrix assignment for --%s: expected key=value", flag)
	}
	if key == "" {
		return "", "", fmt.Errorf("missing matrix key in --%s", flag)
	}
	if val == "" {
		return "", "", fmt.Errorf("missing value for matrix flag --%s", flag)
	}
	return key, val, nil
}
