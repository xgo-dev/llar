package internal

import (
	"os"
	"runtime"
	"testing"

	"github.com/spf13/cobra"
)

func makeCmdForTest() *cobra.Command {
	cmd := &cobra.Command{Use: "make"}
	cmd.Flags().BoolP("verbose", "v", false, "")
	cmd.Flags().StringP("output", "o", "", "")
	cmd.Flags().BoolP("help", "h", false, "")
	return cmd
}

func TestExtractMatrixFlags_UnknownLong(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"mod@v1", "--os", "linux", "--arch=amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected matrix, got nil")
	}
	got := m.Combinations()[0]
	if got != "amd64-linux" {
		t.Fatalf("matrix = %q, want %q", got, "amd64-linux")
	}
}

func TestExtractMatrixFlags_KnownFlagsSkipped(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"--output", "out", "-v", "--os", "linux", "--arch", "amd64", "mod@v1"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.Combinations()[0]
	if got != "amd64-linux" {
		t.Fatalf("matrix = %q, want %q", got, "amd64-linux")
	}
}

func TestExtractMatrixFlags_RequireFlag(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"mod@v1", "--arch", "amd64", "--os", "linux", "--require", "output=custom", "--require=debug=true"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.Combinations()[0]
	// Keys sorted: arch, debug, os, output.
	want := "amd64-true-linux-custom"
	if got != want {
		t.Fatalf("matrix = %q, want %q", got, want)
	}
}

func TestExtractMatrixFlags_OptionFlag(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"mod@v1", "--arch", "amd64", "--os", "linux", "--option", "zlib=system", "--option=ssl=openssl"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.Combinations()[0]
	// Require keys sorted: arch, os. Option keys sorted: ssl, zlib.
	want := "amd64-linux|openssl-system"
	if got != want {
		t.Fatalf("matrix = %q, want %q", got, want)
	}
}

func TestExtractMatrixFlags_RegisteredAssignmentFlagsAreNotUnknownRequire(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"mod@v1", "--require", "feature=on", "--option", "feature=off"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.Combinations()[0]
	if got != "on|off" {
		t.Fatalf("matrix = %q, want %q", got, "on|off")
	}
}

func TestExtractMatrixFlags_NoMatrixReturnsNil(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"-v", "--output", "out", "mod@v1"})
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatalf("expected nil matrix, got %+v", m)
	}
}

func TestExtractMatrixFlags_DuplicateKeyLastWins(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"mod@v1", "--os", "darwin", "--os", "linux", "--arch", "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.Combinations()[0]
	if got != "amd64-linux" {
		t.Fatalf("matrix = %q, want %q", got, "amd64-linux")
	}
}

func TestExtractMatrixFlags_UnknownShortFlagErrors(t *testing.T) {
	cmd := makeCmdForTest()
	_, err := extractMatrixFlags(cmd, []string{"mod@v1", "-x", "linux"})
	if err == nil {
		t.Fatal("expected error for unknown short flag")
	}
}

func TestExtractMatrixFlags_MissingValueErrors(t *testing.T) {
	cmd := makeCmdForTest()
	_, err := extractMatrixFlags(cmd, []string{"mod@v1", "--os"})
	if err == nil {
		t.Fatal("expected error for missing value")
	}
}

func TestExtractMatrixFlags_InvalidMatrixKeyErrors(t *testing.T) {
	cmd := makeCmdForTest()
	_, err := extractMatrixFlags(cmd, []string{"mod@v1", "--require", "=value"})
	if err == nil {
		t.Fatal("expected error for empty matrix key")
	}
	_, err = extractMatrixFlags(cmd, []string{"mod@v1", "--require", "@bad=value"})
	if err == nil {
		t.Fatal("expected error for invalid matrix key")
	}
	_, err = extractMatrixFlags(cmd, []string{"mod@v1", "--option", "=value"})
	if err == nil {
		t.Fatal("expected error for empty option key")
	}
	_, err = extractMatrixFlags(cmd, []string{"mod@v1", "--option", "@bad=value"})
	if err == nil {
		t.Fatal("expected error for invalid option key")
	}
}

func TestExtractMatrixFlags_InvalidAssignmentErrors(t *testing.T) {
	cmd := makeCmdForTest()
	_, err := extractMatrixFlags(cmd, []string{"mod@v1", "--require", "debug"})
	if err == nil {
		t.Fatal("expected error for require assignment without equals")
	}
	_, err = extractMatrixFlags(cmd, []string{"mod@v1", "--option", "zlib="})
	if err == nil {
		t.Fatal("expected error for option assignment without value")
	}
}

func TestExtractMatrixFlags_DoubleDashStopsParsing(t *testing.T) {
	cmd := makeCmdForTest()
	m, err := extractMatrixFlags(cmd, []string{"mod@v1", "--os", "linux", "--", "--arch", "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.Combinations()[0]
	if got != "linux" {
		t.Fatalf("matrix = %q, want %q (--arch after -- should be ignored)", got, "linux")
	}
}

func TestResolveMatrixStr_NoFlags(t *testing.T) {
	cmd := makeCmdForTest()
	os.Args = []string{"llar", "make", "mod@v1"}
	got, err := resolveMatrixStr(cmd)
	if err != nil {
		t.Fatal(err)
	}
	want := runtime.GOARCH + "-" + runtime.GOOS
	if got != want {
		t.Fatalf("matrixStr = %q, want host %q", got, want)
	}
}

func TestResolveMatrixStr_WithFlags(t *testing.T) {
	cmd := makeCmdForTest()
	os.Args = []string{"llar", "make", "mod@v1", "--os", "linux", "--arch", "amd64"}
	got, err := resolveMatrixStr(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got != "amd64-linux" {
		t.Fatalf("matrixStr = %q, want %q", got, "amd64-linux")
	}
}
