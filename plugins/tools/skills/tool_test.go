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
	tool := NewTool("/tmp/anna", "/tmp/agents", "", "", nil)
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
	tool := NewTool("/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "bogus"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestExecuteDispatch(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "search"})
	if err == nil {
		t.Error("expected error for search without query")
	}
	_, err = tool.Execute(context.Background(), map[string]any{"action": "install"})
	if err == nil {
		t.Error("expected error for install without source")
	}
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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, "", nil)
	result, err := tool.list(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var skills []installedSkill
	if err := json.Unmarshal([]byte(result), &skills); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, "", nil)
	_, err := tool.remove(context.Background(), map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestRemoveMissingName(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.remove(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestRemoveInvalidName(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.remove(context.Background(), map[string]any{"name": "../../../etc"})
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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, filepath.Join(dir, ".agents", "skills"), nil)
	result, err := tool.remove(context.Background(), map[string]any{"name": "my-skill", "scope": "user"})
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
	tool := NewTool("/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.search(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing query")
	}
}

func TestInstallMissingSource(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "", "", nil)
	_, err := tool.install(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing source")
	}
}

func TestInstallFromLocalDirViaToolDefaultsToUserScope(t *testing.T) {
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
	userRoot := filepath.Join(t.TempDir(), "users", "7")
	userSkillsDir := filepath.Join(userRoot, ".agents", "skills")
	if err := os.MkdirAll(userSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(projectDir, ".agents"), projectDir, userSkillsDir, nil)
	result, err := tool.install(context.Background(), map[string]any{"source": srcDir})
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if !strings.Contains(result, "scope=user") {
		t.Fatalf("expected user scope in result, got %q", result)
	}

	installed := filepath.Join(userSkillsDir, "test-skill", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed SKILL.md not found in user scope: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".agents", "skills", "test-skill")); !os.IsNotExist(err) {
		t.Fatal("skill should not be installed into project scope by default")
	}
}

func TestInstallFromLocalDirViaToolProjectScope(t *testing.T) {
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
	userRoot := filepath.Join(t.TempDir(), "users", "7")
	userSkillsDir := filepath.Join(userRoot, ".agents", "skills")
	if err := os.MkdirAll(userSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(projectDir, ".agents"), projectDir, userSkillsDir, nil)
	result, err := tool.install(context.Background(), map[string]any{"source": srcDir, "scope": "project"})
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if !strings.Contains(result, "scope=project") {
		t.Fatalf("expected project scope in result, got %q", result)
	}

	installed := filepath.Join(projectDir, ".agents", "skills", "test-skill", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed SKILL.md not found in project scope: %v", err)
	}
	if _, err := os.Stat(filepath.Join(userSkillsDir, "test-skill")); !os.IsNotExist(err) {
		t.Fatal("skill should not be installed into user scope when project scope is requested")
	}
}

func TestInstallProjectScopeRequiresProjectRoot(t *testing.T) {
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

	userRoot := filepath.Join(t.TempDir(), "users", "7")
	userSkillsDir := filepath.Join(userRoot, ".agents", "skills")
	if err := os.MkdirAll(userSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", t.TempDir(), "", userSkillsDir, nil)
	_, err := tool.install(context.Background(), map[string]any{"source": srcDir, "scope": "project"})
	if err == nil {
		t.Fatal("expected error when project scope is requested without ProjectRoot")
	}
	if !strings.Contains(err.Error(), "ProjectRoot") {
		t.Fatalf("expected ProjectRoot error, got %v", err)
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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, "", nil)

	t.Run("loads existing skill", func(t *testing.T) {
		result, err := tool.load(context.Background(), map[string]any{"name": "test-skill"})
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
		_, err := tool.load(context.Background(), map[string]any{"name": "nonexistent"})
		if err == nil {
			t.Fatal("expected error for unknown skill")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		_, err := tool.load(context.Background(), map[string]any{})
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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir, filepath.Join(dir, ".agents", "skills"), nil)
	_, err := tool.remove(context.Background(), map[string]any{"name": "x", "scope": "user"})
	if err != nil {
		t.Fatalf("unexpected error removing single-char skill: %v", err)
	}
}

func TestPerUserSkillsInstallAndRemove(t *testing.T) {
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	if err := os.MkdirAll(filepath.Join(agentWS, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	userSkillsDir := filepath.Join(agentWS, "users", "42", ".agents", "skills")
	if err := os.MkdirAll(userSkillsDir, 0o755); err != nil {
		t.Fatal(err)
	}

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

	tool := NewTool("/tmp/anna", agentWS, "", userSkillsDir, nil)
	result, err := tool.install(context.Background(), map[string]any{"source": srcDir})
	if err != nil {
		t.Fatalf("install error: %v", err)
	}
	if !strings.Contains(result, "my-skill") {
		t.Error("expected skill name in result")
	}

	if _, err := os.Stat(filepath.Join(userSkillsDir, "my-skill", "SKILL.md")); err != nil {
		t.Error("skill not installed in user dir")
	}
	if _, err := os.Stat(filepath.Join(agentWS, "skills", "my-skill")); !os.IsNotExist(err) {
		t.Error("skill should NOT be in agent-level dir")
	}

	_, err = tool.remove(context.Background(), map[string]any{"name": "my-skill"})
	if err != nil {
		t.Fatalf("remove error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(userSkillsDir, "my-skill")); !os.IsNotExist(err) {
		t.Error("skill should be removed from user dir")
	}
}

func TestPerUserSkillsList(t *testing.T) {
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

	tool := NewTool("/tmp/anna", agentWS, "", filepath.Join(agentWS, "users", "42", ".agents", "skills"), nil)
	result, err := tool.list(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if !strings.Contains(result, "user-skill") {
		t.Error("expected user-skill in list")
	}
	if !strings.Contains(result, "agent-skill") {
		t.Error("expected agent-skill in list")
	}
}

func TestTargetSkillsDirDefaultsToUserScope(t *testing.T) {
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	userSkillsDir := filepath.Join(agentWS, "users", "7", ".agents", "skills")

	tool := NewTool("/tmp/anna", agentWS, filepath.Join(base, "project"), userSkillsDir, nil)
	scope, got, err := tool.targetSkillsDir(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope != skillScopeUser {
		t.Fatalf("scope = %q, want %q", scope, skillScopeUser)
	}
	if got != userSkillsDir {
		t.Errorf("targetSkillsDir() = %q, want %q", got, userSkillsDir)
	}
}

func TestTargetSkillsDirProjectScope(t *testing.T) {
	base := t.TempDir()
	agentWS := filepath.Join(base, "workspaces", "agent-1")
	projectRoot := filepath.Join(base, "project")

	tool := NewTool("/tmp/anna", agentWS, projectRoot, filepath.Join(agentWS, "users", "7", ".agents", "skills"), nil)
	scope, got, err := tool.targetSkillsDir(context.Background(), "project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope != skillScopeProject {
		t.Fatalf("scope = %q, want %q", scope, skillScopeProject)
	}
	want := filepath.Join(projectRoot, ".agents", "skills")
	if got != want {
		t.Errorf("targetSkillsDir() = %q, want %q", got, want)
	}
}
