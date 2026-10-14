package build

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goplus/llar/internal/vcs"
)

// mockRepo implements vcs.Repo interface for testing.
type mockRepo struct {
	testdataDir string
	syncRef     *string
}

type mockRefs struct{}

func (mockRefs) CompareFunc(a, b string, compareTag func(a, b string) int) int {
	return compareTag(a, b)
}

func (m *mockRepo) Tags(ctx context.Context) ([]vcs.Tag, error) {
	return []vcs.Tag{{Name: "v1.0.0"}, {Name: "v2.0.0"}}, nil
}

func (m *mockRepo) Latest(ctx context.Context) (string, error) {
	return "abc123", nil
}

func (m *mockRepo) Refs() vcs.Refs { return mockRefs{} }

func (m *mockRepo) At(ref, localDir string) fs.FS {
	return os.DirFS(m.testdataDir)
}

func (m *mockRepo) Sync(ctx context.Context, ref, path, destDir string) error {
	if m.syncRef != nil {
		*m.syncRef = ref
	}

	// Strip "github.com/" prefix if present
	path = strings.TrimPrefix(path, "github.com/")

	var srcDir string
	if path == "" {
		srcDir = m.testdataDir
	} else {
		srcDir = filepath.Join(m.testdataDir, path)
	}

	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return err
	}

	// Skip if destination already has files (init() already copied)
	if entries, err := os.ReadDir(destDir); err == nil && len(entries) > 0 {
		return nil
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	return os.CopyFS(destDir, os.DirFS(srcDir))
}

// newMockRepo creates a mock vcs.Repo for testing.
func newMockRepo(testdataDir string) *mockRepo {
	return &mockRepo{testdataDir: testdataDir}
}
