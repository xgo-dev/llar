package build

import (
	"os"
	"strings"
	"testing"
)

func TestRemoteBuildE2EPassesGHCRUsernameToUploader(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/remote-build-e2e/main.go")
	if err != nil {
		t.Fatalf("read remote build E2E runner: %v", err)
	}
	source := string(data)
	if !strings.Contains(source, `"ghcr-username"`) {
		t.Fatal("remote build E2E should accept GHCR username from the CLI")
	}
	if !strings.Contains(source, "Username: cfg.ghcrUsername") {
		t.Fatal("remote build E2E should pass the CLI GHCR username to the uploader")
	}
}
