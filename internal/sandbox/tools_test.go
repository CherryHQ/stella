package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// stubHost is a minimal Host that satisfies the Session/Host interface for unit
// tests that don't need real sandbox execution.
type stubHost struct {
	sandboxpkg.Session
	resolvePath func(path string) (string, error)
}

func (s *stubHost) ResolvePath(path string) (string, error) {
	if s.resolvePath != nil {
		return s.resolvePath(path)
	}
	return path, nil
}

func TestToolIntArg(t *testing.T) {
	tests := []struct {
		args map[string]any
		key  string
		def  int
		want int
	}{
		{map[string]any{"n": float64(5)}, "n", 0, 5},
		{map[string]any{"n": 7}, "n", 0, 7},
		{map[string]any{"n": int32(3)}, "n", 0, 3},
		{map[string]any{"n": int64(9)}, "n", 0, 9},
		{map[string]any{}, "n", 42, 42},
		{map[string]any{"n": "bad"}, "n", 99, 99},
	}
	for _, tc := range tests {
		got := toolIntArg(tc.args, tc.key, tc.def)
		if got != tc.want {
			t.Errorf("toolIntArg(%v, %q, %d) = %d, want %d", tc.args, tc.key, tc.def, got, tc.want)
		}
	}
}

func TestPaginateReadContent(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\n"
	tests := []struct {
		offset    int
		limit     int
		wantLines int
		wantTotal int
	}{
		{1, 0, 5, 5},
		{2, 2, 2, 5},
		{6, 0, 0, 5}, // offset beyond end
		{1, 100, 5, 5},
	}
	for _, tc := range tests {
		got, total := paginateReadContent(content, tc.offset, tc.limit)
		if total != tc.wantTotal {
			t.Errorf("paginateReadContent(offset=%d, limit=%d): total=%d, want %d", tc.offset, tc.limit, total, tc.wantTotal)
		}
		lines := 0
		if got != "" {
			for _, c := range got {
				if c == '\n' {
					lines++
				}
			}
		}
		if lines != tc.wantLines {
			t.Errorf("paginateReadContent(offset=%d, limit=%d): got %d newlines in %q, want %d", tc.offset, tc.limit, lines, got, tc.wantLines)
		}
	}
}

func TestResolveToolPath_withProjectRoot(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "main.go")
	if err := os.WriteFile(f, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{}
	got, err := resolveToolPath(host, root, "main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("got %q, want %q", got, f)
	}
}

func TestResolveToolPath_withoutProjectRoot(t *testing.T) {
	called := false
	host := &stubHost{
		resolvePath: func(path string) (string, error) {
			called = true
			return "/resolved/" + path, nil
		},
	}
	got, err := resolveToolPath(host, "", "foo.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected host.ResolvePath to be called")
	}
	if got != "/resolved/foo.go" {
		t.Errorf("got %q", got)
	}
}

func TestCoreToolDefinitions(t *testing.T) {
	host := &stubHost{}
	for _, tool := range NewCoreTools(host, "", "") {
		def := tool.Definition()
		if def.Name == "" {
			t.Error("tool has empty name")
		}
		if def.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema", def.Name)
		}
	}
}

func TestNewCoreTools_nilHost(t *testing.T) {
	if got := NewCoreTools(nil, "", ""); got != nil {
		t.Errorf("expected nil for nil host, got %v", got)
	}
}

func TestWriteTool_execute(t *testing.T) {
	root := t.TempDir()
	host := &stubHost{
		resolvePath: func(path string) (string, error) {
			return filepath.Join(root, path), nil
		},
	}
	tool := newWriteTool(host, "")
	out, err := tool.Execute(context.Background(), map[string]any{
		"path":    "hello.txt",
		"content": "hello world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestReadTool_execute(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "test.txt")
	if err := os.WriteFile(f, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{
		resolvePath: func(path string) (string, error) { return f, nil },
	}
	tool := newReadTool(host, "")
	out, err := tool.Execute(context.Background(), map[string]any{"path": "test.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestEditTool_execute(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "code.go")
	if err := os.WriteFile(f, []byte("package main\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{
		resolvePath: func(path string) (string, error) { return f, nil },
	}
	tool := newEditTool(host, "")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":       "code.go",
		"old_string": "func Foo() {}",
		"new_string": "func Bar() {}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, _ := os.ReadFile(f)
	if string(data) != "package main\n\nfunc Bar() {}\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}
