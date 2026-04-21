package resources

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestExtractSkills(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractSkills(dir); err != nil {
		t.Fatalf("ExtractSkills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "anna", "SKILL.md")); err != nil {
		t.Fatalf("anna/SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "system", "lark", "SKILL.md")); err != nil {
		t.Fatalf("system/lark/SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "system", "tap-web", "SKILL.md")); err != nil {
		t.Fatalf("system/tap-web/SKILL.md missing: %v", err)
	}
}

func TestExtractSubAgents(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractSubAgents(dir); err != nil {
		t.Fatalf("ExtractSubAgents: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected sub-agent files")
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
		t.Fatalf("first call: %v", err)
	}
	// Mutate the extracted file to verify Ensure doesn't re-extract.
	target := filepath.Join(dir, "anna", "SKILL.md")
	if err := os.WriteFile(target, []byte("sentinel"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(data) != "sentinel" {
		t.Fatalf("EnsureBuiltinSkills should be idempotent (once-per-dir), got content re-extracted")
	}
}

func TestExtractSkillsNestedRoots(t *testing.T) {
	fakeFS := fstest.MapFS{
		"system/lark/SKILL.md":        {Data: []byte("---\nname: lark\n---\n")},
		"system/lark/references/a.md": {Data: []byte("a")},
		"tap-web/SKILL.md":            {Data: []byte("---\nname: tap-web\n---\n")},
	}
	dir := t.TempDir()
	if err := extractSkillsFS(fakeFS, dir); err != nil {
		t.Fatalf("extractSkillsFS: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "system", "lark", "SKILL.md")); err != nil {
		t.Fatalf("system/lark/SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tap-web", "SKILL.md")); err != nil {
		t.Fatalf("tap-web/SKILL.md missing: %v", err)
	}
}

func TestExtractSkillsReplacesStaleEntries(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "anna", "stale.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if err := ExtractSkills(dir); err != nil {
		t.Fatalf("ExtractSkills: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file should be removed, err=%v", err)
	}
}

func TestSubFSContainsAnna(t *testing.T) {
	sub, ok := SubFS(KindSkill)
	if !ok {
		t.Fatal("skill SubFS not available")
	}
	if _, err := fs.Stat(sub, "anna"); err != nil {
		t.Fatalf("anna dir not found: %v", err)
	}
}
