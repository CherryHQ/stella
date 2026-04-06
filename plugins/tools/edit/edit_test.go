package edit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEditTool_Definition(t *testing.T) {
	tool := &EditTool{}
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

	tool := &EditTool{}
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "world",
		"new_string": "Go",
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

func TestEditTool_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "nonexistent",
		"new_string": "x",
	})
	if err == nil {
		t.Error("expected error when old_string not found")
	}
}

func TestEditTool_AmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("foo foo"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "foo",
		"new_string": "bar",
	})
	if err == nil {
		t.Error("expected error for ambiguous (multi-match) old_string")
	}
}

func TestEditTool_MissingFilePath(t *testing.T) {
	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"old_string": "x",
		"new_string": "y",
	})
	if err == nil {
		t.Error("expected error when file_path is missing")
	}
}

func TestEditTool_MissingOldString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"new_string": "y",
	})
	if err == nil {
		t.Error("expected error when old_string is missing")
	}
}

func TestEditTool_FileNotExist(t *testing.T) {
	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  "/nonexistent/path/file.txt",
		"old_string": "x",
		"new_string": "y",
	})
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}
