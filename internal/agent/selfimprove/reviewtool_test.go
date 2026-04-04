package selfimprove

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewSkillsToolDefinition(t *testing.T) {
	tool := NewReviewSkillsTool(t.TempDir())
	def := tool.Definition()

	if def.Name != "review_skills" {
		t.Errorf("name = %q, want %q", def.Name, "review_skills")
	}

	// Verify only create/patch/deprecate are allowed.
	schema := def.InputSchema
	props, _ := schema["properties"].(map[string]any)
	actionProp, _ := props["action"].(map[string]any)
	enumVals, _ := actionProp["enum"].([]any)

	allowed := map[string]bool{"create": false, "patch": false, "deprecate": false}
	for _, v := range enumVals {
		s, _ := v.(string)
		if _, ok := allowed[s]; ok {
			allowed[s] = true
		} else {
			t.Errorf("unexpected action %q in schema enum", s)
		}
	}
	for action, found := range allowed {
		if !found {
			t.Errorf("expected action %q in schema enum", action)
		}
	}
}

func TestReviewSkillsToolCreate(t *testing.T) {
	dir := t.TempDir()
	tool := NewReviewSkillsTool(dir)

	result, err := tool.Execute(context.Background(), map[string]any{
		"action":      "create",
		"name":        "test-skill",
		"description": "A test skill",
		"content":     "# Steps\n1. Do the thing.",
	})
	if err != nil {
		t.Fatalf("Execute create: %v", err)
	}
	if !strings.Contains(result, "created") {
		t.Errorf("result = %q, want containing 'created'", result)
	}

	data, err := os.ReadFile(filepath.Join(dir, "test-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "name: test-skill") {
		t.Error("expected name in frontmatter")
	}
	if !strings.Contains(content, "description: A test skill") {
		t.Error("expected description in frontmatter")
	}
	if !strings.Contains(content, "status: draft") {
		t.Error("expected status: draft")
	}
	if !strings.Contains(content, "# Steps") {
		t.Error("expected body content")
	}
}

func TestReviewSkillsToolPatch(t *testing.T) {
	dir := t.TempDir()
	tool := NewReviewSkillsTool(dir)

	// Create first.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":      "create",
		"name":        "patch-me",
		"description": "Original",
		"content":     "# Old",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Patch.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":      "patch",
		"name":        "patch-me",
		"description": "Updated",
		"content":     "# New",
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !strings.Contains(result, "updated") {
		t.Errorf("result = %q, want containing 'updated'", result)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "patch-me", "SKILL.md"))
	content := string(data)
	if !strings.Contains(content, "description: Updated") {
		t.Error("expected updated description")
	}
	if !strings.Contains(content, "# New") {
		t.Error("expected new body")
	}
}

func TestReviewSkillsToolDeprecate(t *testing.T) {
	dir := t.TempDir()
	tool := NewReviewSkillsTool(dir)

	// Create first.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":      "create",
		"name":        "dep-skill",
		"description": "To deprecate",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Deprecate.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "deprecate",
		"name":   "dep-skill",
	})
	if err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	if !strings.Contains(result, "deprecated") {
		t.Errorf("result = %q, want containing 'deprecated'", result)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "dep-skill", "SKILL.md"))
	if !strings.Contains(string(data), "status: deprecated") {
		t.Error("expected status: deprecated")
	}
}

func TestReviewSkillsToolUnknownAction(t *testing.T) {
	tool := NewReviewSkillsTool(t.TempDir())

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "install",
		"name":   "bad",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error = %v, want containing 'unknown action'", err)
	}
}

func TestReviewSkillsToolMissingName(t *testing.T) {
	tool := NewReviewSkillsTool(t.TempDir())

	_, err := tool.Execute(context.Background(), map[string]any{
		"action": "create",
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error = %v, want containing 'name is required'", err)
	}
}
