package artifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGHCRUploadIntegrationMeteorsLiuLlar(t *testing.T) {
	token := os.Getenv("GHCR_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = os.Getenv("CR_PAT")
	}
	if token == "" {
		t.Skip("set GHCR_TOKEN, GITHUB_TOKEN, or CR_PAT to run live GHCR upload")
	}
	username := os.Getenv("GHCR_USERNAME")
	if username == "" {
		username = os.Getenv("GITHUB_ACTOR")
	}
	if username == "" {
		t.Skip("set GHCR_USERNAME or GITHUB_ACTOR to run live GHCR upload")
	}

	uploader := NewGHCR(GHCRConfig{Owner: "MeteorsLiu", Username: username, Token: token})
	artifactPath := buildZlibArtifact(t)
	artifact, err := os.Open(artifactPath)
	if err != nil {
		t.Fatalf("Open artifact: %v", err)
	}
	defer artifact.Close()

	tag := "llard-upload-test-" + time.Now().UTC().Format("20060102T150405Z")
	got, err := uploader.Upload(context.Background(), artifact, Options{
		Name: "MeteorsLiu/llar",
		Tag:  tag,
		Type: "tar.gz",
		Attrs: map[string]string{
			"org.llar.matrix": "madler-zlib-host",
			"os":              "linux",
			"arch":            "amd64",
		},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	t.Logf("uploaded tag: %s", tag)
	t.Logf("uploaded url: %s", got.URL)
	t.Logf("uploaded size: %d", got.Size)
	if !strings.HasPrefix(got.URL, "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:") {
		t.Fatalf("URL = %q", got.URL)
	}
}

func buildZlibArtifact(t *testing.T) string {
	t.Helper()

	llar, err := exec.LookPath("llar")
	if err != nil {
		t.Skip("llar not found in PATH")
	}

	dir := t.TempDir()
	outputDir := filepath.Join(dir, "artifact.tar.gz")
	cmd := exec.Command(llar, "make", "-o", outputDir, "madler/zlib")
	cmd.Env = append(os.Environ(), "HOME="+filepath.Join(dir, "home"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("llar make failed: %v\n%s", err, out)
	}
	info, err := os.Stat(outputDir)
	if err != nil {
		t.Fatalf("Stat output: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("llar make output %s is not a directory", outputDir)
	}

	metadata := strings.TrimSpace(string(out))
	if metadata == "" {
		metadata = "-lz"
	}
	writeArtifactMetadata(t, outputDir, metadata)

	archivePath := filepath.Join(dir, "upload-artifact.tar.gz")
	if err := writeTarGz(outputDir, archivePath); err != nil {
		t.Fatalf("write tar.gz: %v", err)
	}
	return archivePath
}

func writeArtifactMetadata(t *testing.T, root, metadata string) {
	t.Helper()

	metaDir := filepath.Join(root, ".llar")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll metadata dir: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"metadata": metadata,
	})
	if err != nil {
		t.Fatalf("Marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "metadata.json"), body, 0o644); err != nil {
		t.Fatalf("WriteFile metadata: %v", err)
	}
}

func writeTarGz(root, dest string) error {
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}
