package read

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTool_Definition(t *testing.T) {
	tool := NewReadTool("")
	def := tool.Definition()
	if def.Name != "read" {
		t.Errorf("expected name 'read', got %q", def.Name)
	}
}

func TestReadTool_BasicRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool("")
	result, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "line1") || !strings.Contains(result, "line3") {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestReadTool_ResolvesRelativePathFromWorkDir(t *testing.T) {
	workDir := t.TempDir()
	path := filepath.Join(workDir, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(workDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"path": "nested/file.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "hello") {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestReadTool_WithOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool("")
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":   path,
		"offset": float64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "line1") {
		t.Error("offset=2 should skip line1")
	}
	if !strings.Contains(result, "line2") {
		t.Error("expected line2 in result")
	}
}

func TestReadTool_WithLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool("")
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"limit": float64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "line1") {
		t.Error("expected line1 in result with limit=1")
	}
	if strings.Contains(result, "line3") {
		t.Error("limit=1 should not include line3")
	}
}

func TestReadTool_MissingPath(t *testing.T) {
	tool := NewReadTool("")
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error when path is missing")
	}
}

func TestReadTool_NonExistentFile(t *testing.T) {
	tool := NewReadTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path": "/nonexistent/path/file.txt",
	})
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestReadTool_BinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	if err := os.WriteFile(path, []byte("binary\x00\x00\x00data"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool("")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
	})
	if err == nil {
		t.Error("expected error for binary file")
	}
}

func TestIntArg(t *testing.T) {
	tests := []struct {
		args       map[string]any
		key        string
		defaultVal int
		want       int
	}{
		{map[string]any{"n": float64(5)}, "n", 0, 5},
		{map[string]any{"n": 3}, "n", 0, 3},
		{map[string]any{}, "n", 10, 10},
		{map[string]any{"n": "bad"}, "n", 7, 7},
	}
	for _, tc := range tests {
		got := intArg(tc.args, tc.key, tc.defaultVal)
		if got != tc.want {
			t.Errorf("intArg(%v, %q, %d) = %d, want %d", tc.args, tc.key, tc.defaultVal, got, tc.want)
		}
	}
}
