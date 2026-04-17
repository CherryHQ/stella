package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateSkill(t *testing.T) {
	dir := t.TempDir()

	err := Create(context.Background(), nil, "my-skill", "A great skill", "# Instructions\nDo the thing.", dir)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	skillFile := filepath.Join(dir, "my-skill", "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "name: my-skill") {
		t.Error("expected name in frontmatter")
	}
	if !strings.Contains(content, "description: A great skill") {
		t.Error("expected description in frontmatter")
	}
	if !strings.Contains(content, "status: draft") {
		t.Error("expected status: draft in frontmatter")
	}
	if !strings.Contains(content, "created-at:") {
		t.Error("expected created-at in frontmatter")
	}
	if !strings.Contains(content, "# Instructions") {
		t.Error("expected body content")
	}
}

func TestCreateSkillAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	if err := Create(context.Background(), nil, "my-skill", "First", "", dir); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := Create(context.Background(), nil, "my-skill", "Second", "", dir)
	if err == nil {
		t.Fatal("expected error for duplicate skill")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestCreateSkillValidation(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		desc string
		err  string
	}{
		{"", "desc", "name is required"},
		{"../bad", "desc", "invalid skill name"},
		{"good-name", "", "description is required"},
		{"good-name", "   ", "description is required"},
	}

	for _, tt := range tests {
		err := Create(context.Background(), nil, tt.name, tt.desc, "", dir)
		if err == nil {
			t.Errorf("Create(%q, %q) expected error containing %q", tt.name, tt.desc, tt.err)
			continue
		}
		if !strings.Contains(err.Error(), tt.err) {
			t.Errorf("Create(%q, %q) error = %v, want containing %q", tt.name, tt.desc, err, tt.err)
		}
	}
}

func TestPatchSkillDescription(t *testing.T) {
	dir := t.TempDir()
	if err := Create(context.Background(), nil, "patch-me", "Original description", "# Body", dir); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := Patch(context.Background(), nil, "patch-me", map[string]string{"description": "Updated description"}, dir)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "patch-me", "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "description: Updated description") {
		t.Error("expected updated description")
	}
	if !strings.Contains(content, "# Body") {
		t.Error("expected body to be preserved")
	}
}

func TestPatchSkillStatus(t *testing.T) {
	dir := t.TempDir()
	if err := Create(context.Background(), nil, "status-skill", "A skill", "", dir); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := Patch(context.Background(), nil, "status-skill", map[string]string{"status": "active"}, dir)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "status-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "status: active") {
		t.Error("expected status: active")
	}
}

func TestPatchSkillContent(t *testing.T) {
	dir := t.TempDir()
	if err := Create(context.Background(), nil, "content-skill", "A skill", "# Old body", dir); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := Patch(context.Background(), nil, "content-skill", map[string]string{"content": "# New body\nWith details."}, dir)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "content-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "# Old body") {
		t.Error("old body should be replaced")
	}
	if !strings.Contains(content, "# New body") {
		t.Error("expected new body")
	}
}

func TestPatchSkillEmptyDescription(t *testing.T) {
	dir := t.TempDir()
	if err := Create(context.Background(), nil, "desc-skill", "A skill", "", dir); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := Patch(context.Background(), nil, "desc-skill", map[string]string{"description": ""}, dir)
	if err == nil {
		t.Fatal("expected error for empty description")
	}
}

func TestPatchSkillNotFound(t *testing.T) {
	dir := t.TempDir()
	err := Patch(context.Background(), nil, "nonexistent", map[string]string{"description": "x"}, dir)
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}

func TestPatchNoUpdates(t *testing.T) {
	dir := t.TempDir()
	err := Patch(context.Background(), nil, "any", map[string]string{}, dir)
	if err == nil {
		t.Fatal("expected error for empty updates")
	}
}

func TestPatchPathTraversal(t *testing.T) {
	dir := t.TempDir()
	err := Patch(context.Background(), nil, "../../evil", map[string]string{"description": "x"}, dir)
	if err == nil {
		t.Fatal("expected error for path traversal name")
	}
	if !strings.Contains(err.Error(), "invalid skill name") {
		t.Errorf("expected 'invalid skill name' error, got: %v", err)
	}
}

func TestDeprecateSkill(t *testing.T) {
	dir := t.TempDir()
	if err := Create(context.Background(), nil, "dep-skill", "A skill", "", dir); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := Deprecate(context.Background(), nil, "dep-skill", dir)
	if err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dep-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "status: "+SkillStatusDeprecated) {
		t.Error("expected status: deprecated")
	}
}

func TestDeprecateSkillNotFound(t *testing.T) {
	dir := t.TempDir()
	err := Deprecate(context.Background(), nil, "nonexistent", dir)
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}

func TestSplitFrontmatterAndBody(t *testing.T) {
	content := "---\nname: test\ndescription: A test\n---\n# Body\nContent here.\n"
	fm, body, err := splitFrontmatterAndBody(content)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if fm["name"] != "test" {
		t.Errorf("name = %q, want %q", fm["name"], "test")
	}
	if fm["description"] != "A test" {
		t.Errorf("description = %q, want %q", fm["description"], "A test")
	}
	if !strings.Contains(body, "# Body") {
		t.Error("expected body content")
	}
}

func TestSplitFrontmatterNoBody(t *testing.T) {
	content := "---\nname: test\n---\n"
	fm, body, err := splitFrontmatterAndBody(content)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if fm["name"] != "test" {
		t.Errorf("name = %q", fm["name"])
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("body = %q, want empty (or whitespace only)", body)
	}
}

func TestSplitFrontmatterInvalid(t *testing.T) {
	_, _, err := splitFrontmatterAndBody("no frontmatter")
	if err == nil {
		t.Fatal("expected error")
	}

	_, _, err = splitFrontmatterAndBody("---\nname: broken\n# no closing")
	if err == nil {
		t.Fatal("expected error for unclosed frontmatter")
	}
}
