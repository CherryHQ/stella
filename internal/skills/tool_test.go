package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDefinition(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd")
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
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd")
	_, err := tool.Execute(context.Background(), map[string]any{"action": "bogus"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestExecuteDispatch(t *testing.T) {
	// search — missing query → error
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd")
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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir)
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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir)
	_, err := tool.remove(map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestRemoveMissingName(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd")
	_, err := tool.remove(map[string]any{})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestRemoveInvalidName(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd")
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

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir)
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

func TestSearchSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "react" {
			t.Errorf("expected query 'react', got %q", r.URL.Query().Get("q"))
		}
		_ = json.NewEncoder(w).Encode(searchResponse{
			Count: 1,
			Skills: []SearchResult{
				{ID: "react-best-practices", Name: "React Best Practices", Installs: 100, Source: "vercel-labs/agent-skills"},
			},
		})
	}))
	defer server.Close()

	tool := &SkillsTool{searchURL: server.URL}
	result, err := tool.search(context.Background(), map[string]any{"query": "react"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestSearchNoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(searchResponse{Count: 0, Skills: []SearchResult{}})
	}))
	defer server.Close()

	tool := &SkillsTool{searchURL: server.URL}
	result, err := tool.search(context.Background(), map[string]any{"query": "nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "No skills found." {
		t.Errorf("expected 'No skills found.', got %q", result)
	}
}

func TestSearchMissingQuery(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd")
	_, err := tool.search(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing query")
	}
}

func TestSearchAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tool := &SkillsTool{searchURL: server.URL}
	_, err := tool.search(context.Background(), map[string]any{"query": "test"})
	if err == nil {
		t.Error("expected error for API error")
	}
}

func TestSearchWithLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "5" {
			t.Errorf("expected limit '5', got %q", r.URL.Query().Get("limit"))
		}
		_ = json.NewEncoder(w).Encode(searchResponse{Count: 0, Skills: []SearchResult{}})
	}))
	defer server.Close()

	tool := &SkillsTool{searchURL: server.URL}
	_, err := tool.search(context.Background(), map[string]any{"query": "test", "limit": float64(5)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	tool := &SkillsTool{searchURL: server.URL}
	_, err := tool.search(context.Background(), map[string]any{"query": "test"})
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestInstallMissingSource(t *testing.T) {
	tool := NewTool("/tmp/anna", "/tmp/agents", "/tmp/cwd")
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

	tool := NewTool("/tmp/anna", workspace, projectDir)
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

func TestRemoveSingleCharName(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "x")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewTool("/tmp/anna", filepath.Join(dir, ".agents"), dir)
	_, err := tool.remove(map[string]any{"name": "x"})
	if err != nil {
		t.Fatalf("unexpected error removing single-char skill: %v", err)
	}
}
