package pkgconfig

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Spec describes the variables and properties written to a pkg-config file.
type Spec struct {
	// Variables defines pkg-config variables. Values override the default
	// prefix, exec_prefix, libdir, and includedir variables with the same name.
	Variables map[string]string

	// Name: The displayed name of the package.
	Name string
	// Description: A description of the package.
	Description string
	// Version: The version of the package.
	Version string
	// URL: A URL to a webpage for the package. This is used to recommend where
	// newer versions of the package can be acquired.
	URL string
	// Requires: Required dependencies that must be met for the package to be
	// usable. All dependencies must be satisfied or the pkg-config
	// implementation must not use the package.
	Requires []string
	// Libs: Required linking flags for this package.
	Libs []string
	// Cflags: Required compiler flags. These flags are always used, regardless
	// of whether static compilation is requested.
	Cflags []string
}

type fragments struct {
	public  []string
	private []string
	shared  []string
}

// File is pkg-config metadata ready to be written.
type File struct {
	variables   map[string]string
	name        string
	description string
	version     string
	url         string
	requires    []string
	libs        fragments
	cflags      fragments
}

// New creates a relocatable pkg-config file.
func New(spec *Spec) (*File, error) {
	if spec == nil {
		return nil, fmt.Errorf("pkgconfig: spec is required")
	}
	if spec.Name == "" {
		return nil, fmt.Errorf("pkgconfig: name is required")
	}
	if spec.Description == "" {
		return nil, fmt.Errorf("pkgconfig: description is required")
	}
	if spec.Version == "" {
		return nil, fmt.Errorf("pkgconfig: version is required")
	}

	return &File{
		variables:   maps.Clone(spec.Variables),
		name:        spec.Name,
		description: spec.Description,
		version:     spec.Version,
		url:         spec.URL,
		requires:    slices.Clone(spec.Requires),
		libs:        fragments{public: slices.Clone(spec.Libs)},
		cflags:      fragments{public: slices.Clone(spec.Cflags)},
	}, nil
}

// Libs returns the linking fragments.
func (f *File) Libs() *fragments {
	return &f.libs
}

// Cflags returns the compiler fragments.
func (f *File) Cflags() *fragments {
	return &f.cflags
}

// String returns the public fragments in pkg-config syntax.
func (f *fragments) String() string {
	return strings.Join(f.public, " ")
}

// Private sets fragments used for static compilation or linking only.
func (f *fragments) Private(values []string) {
	f.private = slices.Clone(values)
}

// Shared sets fragments used for shared compilation or linking only.
func (f *fragments) Shared(values []string) {
	f.shared = slices.Clone(values)
}

// WriteTo encodes the pkg-config file and writes it to w.
func (f *File) WriteTo(w io.Writer) (n int64, err error) {
	var out strings.Builder
	writeVariable := func(name, value string) {
		if override, ok := f.variables[name]; ok {
			value = override
		}
		fmt.Fprintf(&out, "%s=%s\n", name, value)
	}
	writeVariable("prefix", "${pcfiledir}/../..")
	writeVariable("exec_prefix", "${prefix}")
	writeVariable("libdir", "${prefix}/lib")
	writeVariable("includedir", "${prefix}/include")

	customVariables := make([]string, 0, len(f.variables))
	for name := range f.variables {
		switch name {
		case "prefix", "exec_prefix", "libdir", "includedir":
			continue
		}
		customVariables = append(customVariables, name)
	}
	sort.Strings(customVariables)
	for _, name := range customVariables {
		fmt.Fprintf(&out, "%s=%s\n", name, f.variables[name])
	}
	out.WriteByte('\n')
	escapeLiteral := func(value string) string {
		return strings.ReplaceAll(value, "${", "$${")
	}
	fmt.Fprintf(&out, "Name: %s\n", escapeLiteral(f.name))
	fmt.Fprintf(&out, "Description: %s\n", escapeLiteral(f.description))
	fmt.Fprintf(&out, "Version: %s\n", escapeLiteral(f.version))
	if f.url != "" {
		fmt.Fprintf(&out, "URL: %s\n", escapeLiteral(f.url))
	}
	writeFragments := func(name string, values []string, always bool) {
		if len(values) == 0 && !always {
			return
		}
		out.WriteString(name)
		out.WriteByte(':')
		if len(values) > 0 {
			out.WriteByte(' ')
			out.WriteString(strings.Join(values, " "))
		}
		out.WriteByte('\n')
	}
	if len(f.requires) > 0 {
		fmt.Fprintf(&out, "Requires: %s\n", strings.Join(f.requires, ", "))
	}
	writeFragments("Libs", f.libs.public, true)
	writeFragments("Libs.private", f.libs.private, false)
	writeFragments("Libs.shared", f.libs.shared, false)
	writeFragments("Cflags", f.cflags.public, true)
	writeFragments("Cflags.private", f.cflags.private, false)
	writeFragments("Cflags.shared", f.cflags.shared, false)

	data := []byte(out.String())
	written, err := w.Write(data)
	if written > len(data) {
		panic("pkgconfig.File.WriteTo: invalid Write count")
	}
	if written != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	return int64(written), err
}
