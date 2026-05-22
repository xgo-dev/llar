// Package cc parses LLAR C/C++ build metadata.
package cc

import (
	"fmt"
	"strings"

	"github.com/kballard/go-shellquote"
)

// Metadata is the parsed form of LLAR C/C++ raw metadata flags.
type Metadata struct {
	CCFLAGS  []string
	CFLAGS   []string
	CXXFLAGS []string
	LDFLAGS  []string

	sysroot     string
	libraryDirs []string
}

type optionClass int

const (
	classC optionClass = iota
	classCXX
	classLD
	classLibraryDir
	classSysroot
	classStd
)

type parsedOption struct {
	class  optionClass
	values []string
	tokens []string
}

// Parse parses raw C/C++ metadata flags.
func Parse(raw string) (Metadata, error) {
	flags, err := shellquote.Split(raw)
	if err != nil {
		return Metadata{}, err
	}

	var meta Metadata
	for i := 0; i < len(flags); {
		opt, next, ok, err := parseOne(flags, i)
		if err != nil {
			return Metadata{}, err
		}
		if !ok {
			meta.CCFLAGS = append(meta.CCFLAGS, flags[i])
			i++
			continue
		}
		classify(&meta, opt)
		i = next
	}

	return meta, nil
}

// Sysroot returns the parsed sysroot directory.
func (m Metadata) Sysroot() string {
	return m.sysroot
}

// LibraryDirs returns parsed library search directories in flag order.
func (m Metadata) LibraryDirs() []string {
	return append([]string(nil), m.libraryDirs...)
}

func parseOne(args []string, index int) (parsedOption, int, bool, error) {
	arg := args[index]

	switch arg {
	case "--sysroot", "-sysroot", "-isysroot":
		return separateOption(args, index, classSysroot)
	}
	if value, ok := joinedValue(arg, "--sysroot="); ok {
		return joinedOption(args, index, classSysroot, value), index + 1, true, nil
	}
	if value, ok := joinedValue(arg, "-sysroot="); ok {
		return joinedOption(args, index, classSysroot, value), index + 1, true, nil
	}
	if value, ok := joinedValue(arg, "-isysroot"); ok {
		return joinedOption(args, index, classSysroot, value), index + 1, true, nil
	}

	switch arg {
	case "-L", "--library-directory":
		return separateOption(args, index, classLibraryDir)
	case "-l", "-Xlinker", "-z", "-lazy_framework", "-lazy_library":
		return separateOption(args, index, classLD)
	case "-std", "--std":
		return separateOption(args, index, classStd)
	}
	if value, ok := joinedValue(arg, "--library-directory="); ok {
		return joinedOption(args, index, classLibraryDir, value), index + 1, true, nil
	}
	if value, ok := joinedValue(arg, "-L"); ok {
		return joinedOption(args, index, classLibraryDir, value), index + 1, true, nil
	}
	if value, ok := joinedValue(arg, "-l"); ok {
		return joinedOption(args, index, classLD, value), index + 1, true, nil
	}
	if value, ok := joinedValue(arg, "-Wl,"); ok {
		return parsedOption{class: classLD, values: splitCommaValues(value), tokens: []string{arg}}, index + 1, true, nil
	}
	if value, ok := joinedValue(arg, "-std="); ok {
		return joinedOption(args, index, classStd, value), index + 1, true, nil
	}
	if value, ok := joinedValue(arg, "--std="); ok {
		return joinedOption(args, index, classStd, value), index + 1, true, nil
	}

	return parsedOption{}, index, false, nil
}

func separateOption(args []string, index int, class optionClass) (parsedOption, int, bool, error) {
	if index+1 >= len(args) {
		return parsedOption{}, index + 1, false, missingArgError(args[index])
	}
	return parsedOption{
		class:  class,
		values: []string{args[index+1]},
		tokens: []string{args[index], args[index+1]},
	}, index + 2, true, nil
}

func joinedOption(args []string, index int, class optionClass, value string) parsedOption {
	return parsedOption{class: class, values: []string{value}, tokens: []string{args[index]}}
}

func joinedValue(arg, prefix string) (string, bool) {
	if arg == prefix || !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}

func splitCommaValues(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func classify(meta *Metadata, opt parsedOption) {
	switch opt.class {
	case classSysroot:
		if len(opt.values) > 0 {
			meta.sysroot = opt.values[len(opt.values)-1]
		}
	case classLD:
		meta.LDFLAGS = append(meta.LDFLAGS, opt.tokens...)
	case classLibraryDir:
		meta.libraryDirs = append(meta.libraryDirs, opt.values...)
	case classStd:
		classifyStdFlag(meta, opt.tokens, opt.values)
	case classC:
		meta.CFLAGS = append(meta.CFLAGS, opt.tokens...)
	case classCXX:
		meta.CXXFLAGS = append(meta.CXXFLAGS, opt.tokens...)
	default:
		meta.CCFLAGS = append(meta.CCFLAGS, opt.tokens...)
	}
}

func classifyStdFlag(meta *Metadata, tokens, values []string) {
	if len(values) == 0 {
		meta.CCFLAGS = append(meta.CCFLAGS, tokens...)
		return
	}
	value := values[0]
	switch {
	case isCXXStd(value):
		meta.CXXFLAGS = append(meta.CXXFLAGS, tokens...)
	case isCStd(value):
		meta.CFLAGS = append(meta.CFLAGS, tokens...)
	default:
		meta.CCFLAGS = append(meta.CCFLAGS, tokens...)
	}
}

func isCXXStd(value string) bool {
	return strings.HasPrefix(value, "c++") || strings.HasPrefix(value, "gnu++")
}

func isCStd(value string) bool {
	switch {
	case strings.HasPrefix(value, "c") && !strings.HasPrefix(value, "c++"):
		return true
	case strings.HasPrefix(value, "gnu") && !strings.HasPrefix(value, "gnu++"):
		return true
	default:
		return false
	}
}

func missingArgError(flag string) error {
	return fmt.Errorf("%s requires an argument", flag)
}
