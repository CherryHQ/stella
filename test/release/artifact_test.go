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
