package release

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateArtifactFilesChecksDigest(t *testing.T) {
	root := t.TempDir()
	result := validResult()
	content := []byte("server diagnostics")
	relative := RunRelativeDir(result.Run.ID) + "/artifacts/system/server.log"
	fullPath := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	result.Artifacts = []Artifact{{
		Kind:   "server-log",
		Path:   relative,
		SHA256: hex.EncodeToString(sum[:]),
	}}

	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateArtifactFiles(root, result); err != nil {
		t.Fatal(err)
	}

	result.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if err := ValidateArtifactFiles(root, result); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestValidateArtifactFilesRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	result := validResult()
	relative := RunRelativeDir(result.Run.ID) + "/artifacts/system/server.log"
	fullPath := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.log")
	if err := os.WriteFile(target, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fullPath); err != nil {
		t.Fatal(err)
	}
	result.Artifacts = []Artifact{{Kind: "server-log", Path: relative}}

	if err := ValidateArtifactFiles(root, result); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestArtifactForFileBuildsRunScopedReference(t *testing.T) {
	root := t.TempDir()
	runDir := RunDirectory(root, "release-1")
	artifactPath := filepath.Join(runDir, "artifacts", "runner", "evidence.log")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("release evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	artifact, err := ArtifactForFile(root, "release-1", "runner-log", artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "runner-log" ||
		artifact.Path != "dist/test-results/release/release-1/artifacts/runner/evidence.log" ||
		len(artifact.SHA256) != 64 {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
}

func TestArtifactForFileRejectsAnotherRun(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(RunDirectory(root, "release-2"), "evidence.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("evidence"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ArtifactForFile(root, "release-1", "runner-log", path)
	if err == nil || !strings.Contains(err.Error(), "must stay below") {
		t.Fatalf("expected run boundary error, got %v", err)
	}
}
