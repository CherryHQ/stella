package write

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTool_Definition(t *testing.T) {
	tool := NewWriteTool("")
	def := tool.Definition()
	if def.Name != "write" {
		t.Errorf("expected name 'write', got %q", def.Name)
	}
}

func TestWriteTool_CreateNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")

	tool := NewWriteTool("")
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("Wrote %s (%d bytes)", path, len("hello world"))
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Errorf("expected file content 'hello world', got %q", string(data))
	}
}

func TestWriteTool_ResolvesRelativePathFromProjectRoot(t *testing.T) {
	projectRoot := t.TempDir()
	tool := NewWriteTool(projectRoot)

	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "nested/file.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(projectRoot, "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected written content, got %q", string(data))
	}
}

func TestWriteTool_RejectsRelativePathWithoutProjectRoot(t *testing.T) {
	tool := NewWriteTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    "nested/file.txt",
		"content": "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "requires a project root") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestWriteTool_OverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewWriteTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "new content",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Errorf("expected 'new content', got %q", string(data))
	}
}

func TestWriteTool_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "file.txt")

	tool := NewWriteTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "nested",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected file to be created in nested dirs")
	}
}

func TestWriteTool_MissingPath(t *testing.T) {
	tool := NewWriteTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"content": "data",
	})
	if err == nil {
		t.Error("expected error when path is missing")
	}
}
