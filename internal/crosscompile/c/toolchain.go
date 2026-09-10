package c

// Toolchain contains prepared C-family commands.
type Toolchain struct {
	cc       []string
	cxx      []string
	linker   []string
	archiver string
	ranlib   string
	nm       string
	strip    string
}

// NewToolchain creates a Toolchain from prepared target commands.
func NewToolchain(cc, cxx, linker []string, archiver, ranlib, nm, strip string) Toolchain {
	return Toolchain{
		cc:       append([]string(nil), cc...),
		cxx:      append([]string(nil), cxx...),
		linker:   append([]string(nil), linker...),
		archiver: archiver,
		ranlib:   ranlib,
		nm:       nm,
		strip:    strip,
	}
}

func (t Toolchain) CC() []string     { return append([]string(nil), t.cc...) }
func (t Toolchain) CXX() []string    { return append([]string(nil), t.cxx...) }
func (t Toolchain) Linker() []string { return append([]string(nil), t.linker...) }
func (t Toolchain) Archiver() string { return t.archiver }
func (t Toolchain) Ranlib() string   { return t.ranlib }
func (t Toolchain) NM() string       { return t.nm }
func (t Toolchain) Strip() string    { return t.strip }
