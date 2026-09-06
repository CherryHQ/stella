package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishCoreSkillMirrorRollsBackOnPublishFailure(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "resources", "skills", "core")
	stage := filepath.Join(root, ".core-skills-stage")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "old.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "new.md"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	publishErr := errors.New("injected publish failure")
	renames := 0
	rename := func(oldPath, newPath string) error {
		renames++
		if renames == 2 {
			return publishErr
		}
		return os.Rename(oldPath, newPath)
	}
	err := publishCoreSkillMirrorWithOps(destination, stage, rename, os.RemoveAll)
	if err == nil || !errors.Is(err, publishErr) {
		t.Fatalf("publishCoreSkillMirrorWithOps error = %v, want injected failure", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "old.md"))
	if err != nil {
		t.Fatalf("read restored mirror: %v", err)
	}
	if string(data) != "old" {
		t.Fatalf("restored mirror = %q, want old content", data)
	}
}
