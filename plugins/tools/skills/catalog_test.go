package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillsWithConfigUsesConfiguredHomeDir(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	commonDir := filepath.Join(home, ".agents", "skills", "common-skill")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "SKILL.md"), []byte(`---
description: Common skill
status: active
---
body
`), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := LoadSkillsWithConfig(context.Background(), LoadSkillsConfig{
		HomeDir: home,
	})
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "common-skill" {
		t.Fatalf("skill name = %q, want common-skill", skills[0].Name)
	}
	if skills[0].Source != "common" {
		t.Fatalf("skill source = %q, want common", skills[0].Source)
	}
}
