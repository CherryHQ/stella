package resources

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func legacyInventoryFixture(t *testing.T) (*Registry, string, string) {
	registry, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	legacy := filepath.Join(home, ".agents", "skills")
	return registry, legacy, registry.BuiltinSkills()[0].Root
}

func TestInventoryLegacySkillsAllowsKnownProjectionPathsWithOldBytes(t *testing.T) {
	registry, legacy, owned := legacyInventoryFixture(t)
	if err := os.MkdirAll(filepath.Join(legacy, filepath.FromSlash(owned)), 0o755); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(legacy, filepath.FromSlash(owned), "SKILL.md")
	if err := os.WriteFile(known, []byte("old derived bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(known, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(known)
	if err != nil {
		t.Fatal(err)
	}

	blockers, err := registry.InventoryLegacySkills(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 0 {
		t.Fatalf("blockers = %#v, want none", blockers)
	}
	after, err := os.Stat(known)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(known)
	if err != nil || string(bytes) != "old derived bytes" {
		t.Fatalf("known projection bytes changed: %q, %v", bytes, err)
	}
	if before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("known projection metadata changed: before=%v/%v after=%v/%v", before.Mode(), before.ModTime(), after.Mode(), after.ModTime())
	}
}

func TestInventoryLegacySkillsAllowsNestedKnownProjectionPath(t *testing.T) {
	registry, legacy, _ := legacyInventoryFixture(t)
	for _, skill := range registry.BuiltinSkills() {
		for _, file := range skill.Files {
			if file.Path == "SKILL.md" {
				continue
			}
			filename := filepath.Join(legacy, filepath.FromSlash(skill.Root), filepath.FromSlash(file.Path))
			if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, []byte("old nested bytes"), 0o700); err != nil {
				t.Fatal(err)
			}
			blockers, err := registry.InventoryLegacySkills(legacy)
			if err != nil || len(blockers) != 0 {
				t.Fatalf("nested known projection blockers = %#v, %v", blockers, err)
			}
			return
		}
	}
	t.Fatal("manifest has no nested builtin file")
}

func TestInventoryLegacySkillsFindsExtraFileAndNestedCustomSkill(t *testing.T) {
	registry, legacy, owned := legacyInventoryFixture(t)
	root := filepath.Join(legacy, filepath.FromSlash(owned))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra.txt"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(root, "nested", "custom")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "SKILL.md"), []byte("custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockers, err := registry.InventoryLegacySkills(legacy)
	if err != nil {
		t.Fatal(err)
	}
	want := []LegacySkillBlocker{{Path: pathJoin(owned, "extra.txt"), Kind: "residual_path"}, {Path: pathJoin(owned, "nested/custom"), Kind: "skill_root"}}
	if !reflect.DeepEqual(blockers, want) {
		t.Fatalf("blockers = %#v, want %#v", blockers, want)
	}
	if got, err := os.ReadFile(filepath.Join(custom, "SKILL.md")); err != nil || string(got) != "custom" {
		t.Fatalf("custom skill changed: %q, %v", got, err)
	}
}

func TestInventoryLegacySkillsRejectsInvalidRootAndManifestPath(t *testing.T) {
	registry, legacy, owned := legacyInventoryFixture(t)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.InventoryLegacySkills(legacy); err == nil {
		t.Fatal("expected regular legacy root to fail closed")
	}

	link := filepath.Join(t.TempDir(), "skills-link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := registry.InventoryLegacySkills(link); err == nil {
		t.Fatal("expected symlink legacy root to fail closed")
	}

	legacy = filepath.Join(t.TempDir(), ".agents", "skills")
	root := filepath.Join(legacy, filepath.FromSlash(owned))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "SKILL.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := registry.InventoryLegacySkills(legacy); err == nil {
		t.Fatal("expected manifest-owned symlink to fail closed")
	}
}

func TestInventoryLegacySkillsOrdersBlockersStably(t *testing.T) {
	registry, legacy, owned := legacyInventoryFixture(t)
	root := filepath.Join(legacy, filepath.FromSlash(owned))
	if err := os.MkdirAll(filepath.Join(root, "middle-custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zzz-extra", "aaa-extra", "middle-custom/SKILL.md"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []LegacySkillBlocker{
		{Path: pathJoin(owned, "aaa-extra"), Kind: "residual_path"},
		{Path: pathJoin(owned, "middle-custom"), Kind: "skill_root"},
		{Path: pathJoin(owned, "zzz-extra"), Kind: "residual_path"},
	}
	for i := range 3 {
		got, err := registry.InventoryLegacySkills(legacy)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d blockers = %#v, %v; want %#v", i, got, err, want)
		}
	}
}

func pathJoin(base, child string) string { return filepath.ToSlash(filepath.Join(base, child)) }
