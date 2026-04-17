package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func TestBuildPromptSectionIncludesVisibleSkills(t *testing.T) {
	annaHome := t.TempDir()
	workspace := t.TempDir()
	cwd := t.TempDir()

	projectSkillDir := filepath.Join(cwd, ".agents", "skills", "project-skill")
	if err := os.MkdirAll(projectSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectSkillDir, "SKILL.md"), []byte(`---
name: project-skill
description: Project skill
status: active
---
project body
`), 0o644); err != nil {
		t.Fatal(err)
	}

	userRoot := filepath.Join(workspace, "users", "42")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	userSkillDir := filepath.Join(userRoot, ".agents", "skills", "user-skill")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte(`---
name: user-skill
description: User skill
status: draft
---
user body
`), 0o644); err != nil {
		t.Fatal(err)
	}

	deprecatedDir := filepath.Join(workspace, ".agents", "skills", "old-skill")
	if err := os.MkdirAll(deprecatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deprecatedDir, "SKILL.md"), []byte(`---
name: old-skill
description: Old skill
status: deprecated
---
old body
`), 0o644); err != nil {
		t.Fatal(err)
	}

	homeDir := t.TempDir()
	section, err := BuildPromptSection(context.Background(), pkgplugins.SystemPromptContext{
		AnnaHome:    annaHome,
		HomeDir:     homeDir,
		AgentRoot:   workspace,
		ProjectRoot: cwd,
		UserRoot:    userRoot,
	})
	if err != nil {
		t.Fatal(err)
	}

	if section.Title != "Skills" {
		t.Fatalf("unexpected section title: %q", section.Title)
	}
	if !strings.Contains(section.Content, "<name>project-skill</name>") {
		t.Fatalf("expected project skill in prompt content: %s", section.Content)
	}
	if !strings.Contains(section.Content, "<name>user-skill</name>") {
		t.Fatalf("expected user skill in prompt content: %s", section.Content)
	}
	if strings.Contains(section.Content, "<name>old-skill</name>") {
		t.Fatalf("did not expect deprecated skill in prompt content: %s", section.Content)
	}
}

func TestBuildPromptSectionOmitsEmptySkillList(t *testing.T) {
	section, err := BuildPromptSection(context.Background(), pkgplugins.SystemPromptContext{
		AnnaHome:    "",
		HomeDir:     t.TempDir(),
		AgentRoot:   t.TempDir(),
		ProjectRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if section.Title != "" || section.Content != "" {
		t.Fatalf("expected empty section, got %#v", section)
	}
}
