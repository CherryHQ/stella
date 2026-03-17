package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillTool(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	content := "---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\nDo the thing."
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewLoadSkillTool([]SkillEntry{
		{Name: "test-skill", BaseDir: dir, Path: skillPath},
	})

	t.Run("loads existing skill", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), map[string]any{"name": "test-skill"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "# Test Skill") {
			t.Error("expected skill content in result")
		}
		if !strings.Contains(result, "base_dir=") {
			t.Error("expected base_dir in result")
		}
		if !strings.Contains(result, dir) {
			t.Errorf("expected base_dir to contain %q", dir)
		}
	})

	t.Run("unknown skill", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), map[string]any{"name": "nonexistent"})
		if err == nil {
			t.Fatal("expected error for unknown skill")
		}
		if !strings.Contains(err.Error(), "unknown skill") {
			t.Errorf("expected 'unknown skill' error, got: %v", err)
		}
	})

	t.Run("missing name", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing name")
		}
	})
}

func TestLoadSkillToolDefinition(t *testing.T) {
	tool := NewLoadSkillTool(nil)
	def := tool.Definition()
	if def.Name != "load_skill" {
		t.Errorf("expected name 'load_skill', got %q", def.Name)
	}
}
