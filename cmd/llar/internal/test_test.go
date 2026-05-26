package internal

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goplus/llar/internal/formula/repo"
)

// runTestCmd mirrors runMakeCmd for the `llar test` subcommand. It executes
// rootCmd in-process (so coverage is counted) and captures stdout through a
// pipe because build tooling writes through os.Stdout.
func runTestCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	defer os.Chdir(origDir)

	testVerbose = true

	// Set os.Args to match what Cobra will see, so resolveMatrixStr works.
	origArgs := os.Args
	os.Args = append([]string{"llar", "test"}, args...)
	defer func() { os.Args = origArgs }()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	var buf bytes.Buffer
	copyDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, r)
		copyDone <- copyErr
	}()

	cmd := rootCmd
	cmd.SetArgs(append([]string{"test"}, args...))
	err = cmd.Execute()

	_ = w.Close()
	if copyErr := <-copyDone; copyErr != nil {
		t.Fatalf("failed to capture stdout: %v", copyErr)
	}
	return buf.String(), err
}

// TestTest_InvalidDotSyntax covers the parseModuleArg error branch: passing
// ".@v1.0.0" must fail fast with a clear message telling the user to use
// "./@version" instead.
func TestTest_InvalidDotSyntax(t *testing.T) {
	_, err := runTestCmd(t, "-v", ".@v1.0.0")
	if err == nil {
		t.Fatal("expected error for .@version syntax")
	}
	want := `invalid local pattern ".@v1.0.0": use "./@version" instead of ".@version"`
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// TestTest_RemoteStoreError covers the newRemoteStore error branch: when
// remote store construction fails the command must surface that error
// verbatim rather than continuing with a nil store.
func TestTest_RemoteStoreError(t *testing.T) {
	orig := newRemoteStore
	newRemoteStore = func() (repo.Store, error) {
		return nil, fmt.Errorf("mock store creation failed")
	}
	defer func() { newRemoteStore = orig }()

	_, err := runTestCmd(t, "owner/repo@v1.0.0")
	if err == nil {
		t.Fatal("expected error when newRemoteStore fails")
	}
	if got := err.Error(); got != "mock store creation failed" {
		t.Errorf("error = %q, want %q", got, "mock store creation failed")
	}
}

// TestTestLocal_DotPattern exercises the local branch end-to-end: module
// resolution via modlocal + overlay store succeeds, but the actual build
// fails at git sync because the overlay has no real upstream. This verifies
// runTest wires the local code path, without requiring network.
func TestTestLocal_DotPattern(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(filepath.Join(formulaDir, "test", "liba"))
	defer os.Chdir(origDir)

	// runTest bypasses cache for the root, so pre-populated cache cannot
	// short-circuit the build; we expect failure at the sync stage.
	_, err := runTestCmd(t, "-v", "./@1.0.0")
	if err == nil {
		t.Fatal("expected build error for mock module under runTest")
	}
	if !strings.Contains(err.Error(), "failed to build test/liba@1.0.0") {
		t.Errorf("expected 'failed to build test/liba@1.0.0', got: %v", err)
	}
}

// TestTestLocal_NotFound covers the modlocal.Resolve error branch: when the
// local pattern does not point to any formula, Resolve surfaces a parse
// error that must propagate up through runTest.
func TestTestLocal_NotFound(t *testing.T) {
	formulaDir := setupLocalFormulas(t)
	withMockRemoteStore(t, repo.New(formulaDir, &noopVCSRepo{}))

	origDir, _ := os.Getwd()
	os.Chdir(formulaDir)
	defer os.Chdir(origDir)

	_, err := runTestCmd(t, "-v", "./nonexistent/repo@1.0.0")
	if err == nil {
		t.Fatal("expected error for nonexistent local module")
	}
	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("expected 'failed to parse' error, got: %v", err)
	}
}
