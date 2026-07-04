package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type stubHost struct {
	pkgsandbox.Session
	resolvePath func(path string) (string, error)
}

func (s *stubHost) ResolvePath(path string) (string, error) {
	if s.resolvePath != nil {
		return s.resolvePath(path)
	}
	return path, nil
}

func (s *stubHost) ResolveWritePath(path string) (string, error) {
	return s.ResolvePath(path)
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
		{6, 0, 0, 5},
		{1, 100, 5, 5},
	}
	for _, tc := range tests {
		paged, total := paginateReadContent(content, tc.offset, tc.limit)
		if total != tc.wantTotal {
			t.Errorf("offset=%d limit=%d: total=%d want %d", tc.offset, tc.limit, total, tc.wantTotal)
		}
		if tc.wantLines == 0 {
			if paged != "" {
				t.Errorf("offset=%d limit=%d: expected empty paged, got %q", tc.offset, tc.limit, paged)
			}
			continue
		}
		lines := splitLines(paged)
		if len(lines) != tc.wantLines {
			t.Errorf("offset=%d limit=%d: got %d lines, want %d", tc.offset, tc.limit, len(lines), tc.wantLines)
		}
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestReadTool_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(file, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{}
	tool := newReadTool(host, "")
	out, err := tool.Execute(context.Background(), map[string]any{"path": file})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestWriteTool_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "out.txt")
	host := &stubHost{}
	tool := newWriteTool(host, "")
	_, err := tool.Execute(context.Background(), map[string]any{"path": file, "content": "data"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("file content = %q, want %q", string(got), "data")
	}
}

func TestEditTool_EditsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(file, []byte("foo bar"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &stubHost{}
	tool := newEditTool(host, "")
	_, err := tool.Execute(context.Background(), map[string]any{"path": file, "old_string": "foo", "new_string": "baz"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "baz bar" {
		t.Errorf("file content = %q, want %q", string(got), "baz bar")
	}
}
