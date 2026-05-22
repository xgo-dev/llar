package cc

import (
	"reflect"
	"testing"
)

func TestParseClassifiesMetadataFlags(t *testing.T) {
	meta, err := Parse("-I/include -DDEBUG -std=c11 -std=c++20 -L/lib -lz -Wl,--as-needed")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !reflect.DeepEqual(meta.CCFLAGS, []string{"-I/include", "-DDEBUG"}) {
		t.Fatalf("CCFLAGS = %#v", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.CFLAGS, []string{"-std=c11"}) {
		t.Fatalf("CFLAGS = %#v", meta.CFLAGS)
	}
	if !reflect.DeepEqual(meta.CXXFLAGS, []string{"-std=c++20"}) {
		t.Fatalf("CXXFLAGS = %#v", meta.CXXFLAGS)
	}
	if !reflect.DeepEqual(meta.LDFLAGS, []string{"-lz", "-Wl,--as-needed"}) {
		t.Fatalf("LDFLAGS = %#v", meta.LDFLAGS)
	}
	if !reflect.DeepEqual(meta.LibraryDirs(), []string{"/lib"}) {
		t.Fatalf("LibraryDirs = %#v", meta.LibraryDirs())
	}
	if got := meta.Sysroot(); got != "" {
		t.Fatalf("Sysroot = %q, want empty", got)
	}
}

func TestParseExtractsLibraryDirs(t *testing.T) {
	meta, err := Parse("-L /lib1 -L/lib2 --library-directory /lib3 --library-directory=/lib4 -l z -lfoo")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(meta.CCFLAGS) != 0 {
		t.Fatalf("CCFLAGS = %#v, want empty", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.LibraryDirs(), []string{"/lib1", "/lib2", "/lib3", "/lib4"}) {
		t.Fatalf("LibraryDirs = %#v", meta.LibraryDirs())
	}
	if !reflect.DeepEqual(meta.LDFLAGS, []string{"-l", "z", "-lfoo"}) {
		t.Fatalf("LDFLAGS = %#v", meta.LDFLAGS)
	}
}

func TestParseClassifiesSeparateLinkerFlags(t *testing.T) {
	meta, err := Parse("-Xlinker -rpath -Xlinker /lib -z now")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(meta.CCFLAGS) != 0 {
		t.Fatalf("CCFLAGS = %#v, want empty", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.LDFLAGS, []string{"-Xlinker", "-rpath", "-Xlinker", "/lib", "-z", "now"}) {
		t.Fatalf("LDFLAGS = %#v", meta.LDFLAGS)
	}
}

func TestParseClassifiesLongerLinkerSpellingBeforeShortL(t *testing.T) {
	meta, err := Parse("-lazy_framework Cocoa")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(meta.CCFLAGS) != 0 {
		t.Fatalf("CCFLAGS = %#v", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.LDFLAGS, []string{"-lazy_framework", "Cocoa"}) {
		t.Fatalf("LDFLAGS = %#v", meta.LDFLAGS)
	}
}

func TestParseChoosesLongestMatchingOptionName(t *testing.T) {
	meta, err := Parse("-lazy_library libsqlite3.dylib")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(meta.CCFLAGS) != 0 {
		t.Fatalf("CCFLAGS = %#v, want empty", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.LDFLAGS, []string{"-lazy_library", "libsqlite3.dylib"}) {
		t.Fatalf("LDFLAGS = %#v", meta.LDFLAGS)
	}
}

func TestParseJoinedOrSeparateFallsBackToShortL(t *testing.T) {
	meta, err := Parse("-lazyfoo")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(meta.CCFLAGS) != 0 {
		t.Fatalf("CCFLAGS = %#v", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.LDFLAGS, []string{"-lazyfoo"}) {
		t.Fatalf("LDFLAGS = %#v", meta.LDFLAGS)
	}
}

func TestParseSeparateRequiresExactSpelling(t *testing.T) {
	meta, err := Parse("-Xlinkerfoo")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !reflect.DeepEqual(meta.CCFLAGS, []string{"-Xlinkerfoo"}) {
		t.Fatalf("CCFLAGS = %#v", meta.CCFLAGS)
	}
	if len(meta.LDFLAGS) != 0 {
		t.Fatalf("LDFLAGS = %#v, want empty", meta.LDFLAGS)
	}
}

func TestParseClassifiesSeparateLanguageStdFlags(t *testing.T) {
	meta, err := Parse("-std c99 --std c++17")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(meta.CCFLAGS) != 0 {
		t.Fatalf("CCFLAGS = %#v, want empty", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.CFLAGS, []string{"-std", "c99"}) {
		t.Fatalf("CFLAGS = %#v", meta.CFLAGS)
	}
	if !reflect.DeepEqual(meta.CXXFLAGS, []string{"--std", "c++17"}) {
		t.Fatalf("CXXFLAGS = %#v", meta.CXXFLAGS)
	}
}

func TestParseExtractsSysrootDir(t *testing.T) {
	meta, err := Parse("--sysroot=/sdk -I/include -L/lib")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := meta.Sysroot(); got != "/sdk" {
		t.Fatalf("Sysroot = %q, want /sdk", got)
	}
	if !reflect.DeepEqual(meta.CCFLAGS, []string{"-I/include"}) {
		t.Fatalf("CCFLAGS = %#v", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.LibraryDirs(), []string{"/lib"}) {
		t.Fatalf("LibraryDirs = %#v", meta.LibraryDirs())
	}
	if len(meta.LDFLAGS) != 0 {
		t.Fatalf("LDFLAGS = %#v, want empty", meta.LDFLAGS)
	}
}

func TestParseSysrootFormsAndLastWins(t *testing.T) {
	meta, err := Parse("--sysroot /old -isysroot /middle -sysroot=/new")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := meta.Sysroot(); got != "/new" {
		t.Fatalf("Sysroot = %q, want /new", got)
	}
	if len(meta.CCFLAGS) != 0 {
		t.Fatalf("CCFLAGS = %#v, want empty", meta.CCFLAGS)
	}
	if len(meta.LDFLAGS) != 0 {
		t.Fatalf("LDFLAGS = %#v, want empty", meta.LDFLAGS)
	}
}

func TestParseSingleDashSysrootWithSeparateArg(t *testing.T) {
	meta, err := Parse("-sysroot /sdk -I/include")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := meta.Sysroot(); got != "/sdk" {
		t.Fatalf("Sysroot = %q, want /sdk", got)
	}
	if !reflect.DeepEqual(meta.CCFLAGS, []string{"-I/include"}) {
		t.Fatalf("CCFLAGS = %#v", meta.CCFLAGS)
	}
}

func TestParseReturnsErrorForMissingSysrootArg(t *testing.T) {
	if _, err := Parse("-I/include --sysroot"); err == nil {
		t.Fatal("Parse error = nil, want error")
	}
	if _, err := Parse("-isysroot"); err == nil {
		t.Fatal("Parse error = nil, want error")
	}
	if _, err := Parse("-sysroot"); err == nil {
		t.Fatal("Parse error = nil, want error")
	}
}

func TestParseReturnsErrorForMissingSeparateLinkerArg(t *testing.T) {
	for _, raw := range []string{"-L", "-l", "--library-directory", "-Xlinker", "-z", "-lazy_framework"} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("Parse(%q) error = nil, want error", raw)
		}
	}
}

func TestParseSplitsQuotedFlags(t *testing.T) {
	meta, err := Parse(`-DNAME="hello world" -Wl,-rpath,/path\ with\ space -lfoo`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !reflect.DeepEqual(meta.CCFLAGS, []string{`-DNAME=hello world`}) {
		t.Fatalf("CCFLAGS = %#v", meta.CCFLAGS)
	}
	if !reflect.DeepEqual(meta.LDFLAGS, []string{`-Wl,-rpath,/path with space`, "-lfoo"}) {
		t.Fatalf("LDFLAGS = %#v", meta.LDFLAGS)
	}
}
