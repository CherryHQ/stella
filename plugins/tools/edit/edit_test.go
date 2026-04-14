package edit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEditTool_Definition(t *testing.T) {
	tool := NewEditTool("")
	def := tool.Definition()
	if def.Name != "edit" {
		t.Errorf("expected name 'edit', got %q", def.Name)
	}
}

func TestEditTool_BasicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool("")
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":    path,
		"oldText": "world",
		"newText": "Go",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello Go" {
		t.Errorf("expected 'hello Go', got %q", string(data))
	}
	if result == "" {
		t.Error("expected non-empty result message")
	}
}

func TestEditTool_ResolvesRelativePathFromWorkDir(t *testing.T) {
	workDir := t.TempDir()
	path := filepath.Join(workDir, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool(workDir)
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":       "nested/file.txt",
		"old_string": "world",
		"new_string": "Anna",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello Anna" {
		t.Fatalf("expected edited content, got %q", string(data))
	}
}

func TestEditTool_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    path,
		"oldText": "nonexistent",
		"newText": "x",
	})
	if err == nil {
		t.Error("expected error when oldText not found")
	}
}

func TestEditTool_AmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("foo foo"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    path,
		"oldText": "foo",
		"newText": "bar",
	})
	if err == nil {
		t.Error("expected error for ambiguous (multi-match) oldText")
	}
}

func TestEditTool_MissingPath(t *testing.T) {
	tool := NewEditTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"oldText": "x",
		"newText": "y",
	})
	if err == nil {
		t.Error("expected error when path is missing")
	}
}

func TestEditTool_MissingOldText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    path,
		"newText": "y",
	})
	if err == nil {
		t.Error("expected error when oldText is missing")
	}
}

func TestEditTool_FileNotExist(t *testing.T) {
	tool := NewEditTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "/nonexistent/path/file.txt",
		"oldText": "x",
		"newText": "y",
	})
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}
