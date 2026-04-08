package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinition(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd", 0)
	def := tool.Definition()

	if def.Name != "skills" {
		t.Errorf("expected name 'skills', got %q", def.Name)
	}
	if def.Description == "" {
		t.Error("expected non-empty description")
	}
	if def.InputSchema == nil {
		t.Error("expected non-nil input schema")
	}
}

func TestExecuteUnknownAction(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd", 0)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "bogus"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestExecuteDispatch(t *testing.T) {
	// search — missing query → error
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd", 0)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "search"})
	if err == nil {
		t.Error("expected error for search without query")
	}

	// install — missing source → error
	_, err = tool.Execute(context.Background(), map[string]any{"action": "install"})
	if err == nil {
		t.Error("expected error for install without source")
	}

	// remove — missing name → error
	_, err = tool.Execute(context.Background(), map[string]any{"action": "remove"})
	if err == nil {
		t.Error("expected error for remove without name")
	}
}

func TestListWithSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: test-skill
description: A test skill
---
# Test Skill
`), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, 0)
	result, err := tool.list()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var skills []installedSkill
	if err := json.Unmarshal([]byte(result), &skills); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	// Find the test skill among results (user-level skills may also appear)
	var found bool
	for _, s := range skills {
		if s.Name == "test-skill" {
			found = true
			if s.Description != "A test skill" {
				t.Errorf("expected description 'A test skill', got %q", s.Description)
			}
			break
		}
	}
	if !found {
		t.Error("expected test-skill to appear in list results")
	}
}

func TestRemoveNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, 0)
	_, err := tool.remove(map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestRemoveMissingName(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd", 0)
	_, err := tool.remove(map[string]any{})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestRemoveInvalidName(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd", 0)
	_, err := tool.remove(map[string]any{"name": "../../../etc"})
	if err == nil {
		t.Error("expected error for path traversal name")
	}
}

func TestRemoveSuccess(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, 0)
	result, err := tool.remove(map[string]any{"name": "my-skill"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Error("expected skill directory to be removed")
	}
}

func TestSearchMissingQuery(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd", 0)
	_, err := tool.search(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing query")
	}
}

func TestInstallMissingSource(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd", 0)
	_, err := tool.install(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing source")
	}
}

func TestInstallFromLocalDir(t *testing.T) {
	// Create a local skill directory
	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: A great skill
---
# My Skill
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Install from local path
	targetDir := filepath.Join(t.TempDir(), "skills")
	name, err := Install(context.Background(), srcDir, targetDir)
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if name != "my-skill" {
		t.Errorf("expected skill name 'my-skill', got %q", name)
	}

	// Verify installed
	installed := filepath.Join(targetDir, "my-skill", "SKILL.md")
	data, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("installed SKILL.md not found: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty SKILL.md")
	}
}

func TestInstallFromLocalDirViaTool(t *testing.T) {
	// Create a local skill directory
	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: test-skill
description: Test
---
# Test
`), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	workspace := filepath.Join(projectDir, ".agents")
	if err := os.MkdirAll(filepath.Join(workspace, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", workspace, projectDir, 0)
	result, err := tool.install(context.Background(), map[string]any{
		"source": srcDir,
	})
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	// Verify installed
	installed := filepath.Join(workspace, "skills", "test-skill", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed SKILL.md not found: %v", err)
	}
}

func TestLoadSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\nDo the thing."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, 0)

	t.Run("loads existing skill", func(t *testing.T) {
		result, err := tool.load(map[string]any{"name": "test-skill"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "# Test Skill") {
			t.Error("expected skill content in result")
		}
		if !strings.Contains(result, "base_dir=") {
			t.Error("expected base_dir in result")
		}
		if !strings.Contains(result, skillDir) {
			t.Errorf("expected base_dir to contain %q", skillDir)
		}
	})

	t.Run("unknown skill", func(t *testing.T) {
		_, err := tool.load(map[string]any{"name": "nonexistent"})
		if err == nil {
			t.Fatal("expected error for unknown skill")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		_, err := tool.load(map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing name")
		}
	})
}

func TestRemoveSingleCharName(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "x")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, 0)
	_, err := tool.remove(map[string]any{"name": "x"})
	if err != nil {
		t.Fatalf("unexpected error removing single-char skill: %v", err)
	}
}

func TestPerUserSkillsInstallAndRemove(t *testing.T) {
	// Setup: simulate workspaces/{agentID}/ layout.
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	if err := os.MkdirAll(filepath.Join(agentWS, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	userID := int64(42)
	userSkillsDir := filepath.Join(agentWS, "users", "42", ".agents", "skills")
	if err := os.MkdirAll(userSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a local skill to install.
	srcDir := t.TempDir()
	skillSrc := filepath.Join(srcDir, "my-skill")
	if err := os.MkdirAll(skillSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte(`---
name: my-skill
description: User skill
---
# My Skill
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Install via per-user tool.
	tool := NewTool("/tmp/anna", agentWS, "", userID)
	result, err := tool.install(context.Background(), map[string]any{
		"source": srcDir,
	})
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if !strings.Contains(result, "my-skill") {
		t.Error("expected skill name in result")
	}

	// Verify installed in user's dir, not agent's.
	if _, err := os.Stat(filepath.Join(userSkillsDir, "my-skill", "SKILL.md")); err != nil {
		t.Error("skill not installed in user dir")
	}
	if _, err := os.Stat(filepath.Join(agentWS, "skills", "my-skill")); !os.IsNotExist(err) {
		t.Error("skill should NOT be in agent-level dir")
	}

	// Remove via per-user tool.
	_, err = tool.remove(map[string]any{"name": "my-skill"})
	if err != nil {
		t.Fatalf("remove error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(userSkillsDir, "my-skill")); !os.IsNotExist(err) {
		t.Error("skill should be removed from user dir")
	}
}

func TestPerUserSkillsList(t *testing.T) {
	// Setup agent workspace with an agent-level skill.
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	agentSkill := filepath.Join(agentWS, "skills", "agent-skill")
	if err := os.MkdirAll(agentSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentSkill, "SKILL.md"), []byte(`---
name: agent-skill
description: Agent-level shared skill
---
# Agent Skill
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Setup user skills.
	userSkillsDir := filepath.Join(agentWS, "users", "42", ".agents", "skills", "user-skill")
	if err := os.MkdirAll(userSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userSkillsDir, "SKILL.md"), []byte(`---
name: user-skill
description: User-specific skill
---
# User Skill
`), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", agentWS, "", int64(42))
	result, err := tool.list()
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	// Both should appear.
	if !strings.Contains(result, "user-skill") {
		t.Error("expected user-skill in list")
	}
	if !strings.Contains(result, "agent-skill") {
		t.Error("expected agent-skill in list")
	}
}

func TestAgentLevelToolBackwardCompat(t *testing.T) {
	// userID=0 should use workspace/skills/ as before.
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	if err := os.MkdirAll(filepath.Join(agentWS, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", agentWS, "", 0)
	got := tool.skillsDir()
	want := filepath.Join(agentWS, "skills")
	if got != want {
		t.Errorf("skillsDir() = %q, want %q", got, want)
	}
}

func TestPerUserToolSkillsDir(t *testing.T) {
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")

	tool := NewTool("/tmp/anna", agentWS, "", int64(7))
	got := tool.skillsDir()
	want := filepath.Join(agentWS, "users", "7", ".agents", "skills")
	if got != want {
		t.Errorf("skillsDir() = %q, want %q", got, want)
	}
}
