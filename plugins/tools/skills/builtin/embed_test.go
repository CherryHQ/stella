package builtin

import (
	"io/fs"
	"testing"
)

func TestBuiltinSkillFS(t *testing.T) {
	fsys := BuiltinSkillFS()
	if fsys == nil {
		t.Fatal("expected non-nil FS")
	}
	// Verify the anna skill directory exists
	_, err := fs.Stat(fsys, "anna")
	if err != nil {
		t.Fatalf("anna dir not found in builtin FS: %v", err)
	}
}

func TestExtractSkills(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractSkills(dir); err != nil {
		t.Fatalf("ExtractSkills failed: %v", err)
	}
	// Check that SKILL.md was extracted
	entries, err := fs.ReadDir(BuiltinSkillFS(), "anna")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected files in anna/ skill")
	}
}

func TestExtractAgents(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractAgents(dir); err != nil {
		t.Fatalf("ExtractAgents failed: %v", err)
	}
}

func TestEnsureBuiltinSkills_EmptyDir(t *testing.T) {
	if err := EnsureBuiltinSkills(""); err != nil {
		t.Fatalf("empty dir should be no-op: %v", err)
	}
}

func TestEnsureBuiltinSkills_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
}
