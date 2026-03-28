package tool

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/vaayne/anna/internal/toolspec"
)

type closeRecorderTool struct {
	closed bool
}

func (t *closeRecorderTool) Definition() toolspec.Definition {
	return toolspec.Definition{Name: "close-recorder"}
}

func (t *closeRecorderTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

func (t *closeRecorderTool) Close() error {
	t.closed = true
	return nil
}

func TestRegistryDefinitions(t *testing.T) {
	reg := NewRegistry("")
	defs := reg.Definitions()
	if len(defs) != 5 {
		t.Fatalf("expected 5 tool definitions, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, name := range []string{"read", "bash", "edit", "write", "webfetch"} {
		if !names[name] {
			t.Errorf("missing tool definition: %s", name)
		}
	}
}

func TestRegistryExecuteUnknown(t *testing.T) {
	reg := NewRegistry("")
	_, err := reg.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestSandboxToolCloseDelegates(t *testing.T) {
	inner := &closeRecorderTool{}
	wrapped := wrapWithSandbox(inner, t.TempDir(), "file_path")

	closer, ok := wrapped.(closeableTool)
	if !ok {
		t.Fatal("wrapped sandbox tool should expose Close")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !inner.closed {
		t.Fatal("expected sandbox wrapper to delegate Close to inner tool")
	}
}

func TestReadTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"file_path": path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello world" {
		t.Errorf("result = %q, want %q", result, "hello world")
	}
}

func TestReadToolMissingFile(t *testing.T) {
	tool := &ReadTool{}
	_, err := tool.Execute(context.Background(), map[string]any{"file_path": "/nonexistent/file.txt"})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadToolMissingArg(t *testing.T) {
	tool := &ReadTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing file_path")
	}
}

func TestBashTool(t *testing.T) {
	tool := &BashTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello\n") {
		t.Errorf("result should contain output, got: %q", result)
	}
	if !strings.Contains(result, "[exit:0 |") {
		t.Errorf("result should contain metadata footer, got: %q", result)
	}
}

func TestBashToolWorkDir(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{workDir: dir}
	result, err := tool.Execute(context.Background(), map[string]any{"command": "pwd -P"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Resolve symlinks (macOS /tmp → /private/tmp).
	resolved, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(result, resolved) {
		t.Errorf("result should contain %q, got: %q", resolved, result)
	}
	if !strings.Contains(result, "[exit:0 |") {
		t.Errorf("result should contain metadata footer, got: %q", result)
	}
}

func TestBashToolFailure(t *testing.T) {
	tool := &BashTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"command": "exit 42"})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(result, "[exit:42 |") {
		t.Errorf("failed command should have exit code in footer, got: %q", result)
	}
}

func TestBashToolMetadataFooter(t *testing.T) {
	tool := &BashTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"command": "echo test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "[exit:0 |") {
		t.Errorf("result should contain exit:0 footer, got: %q", result)
	}
}

func TestBashToolStderrVisibleOnFailure(t *testing.T) {
	tool := &BashTool{}
	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "echo 'some output' && echo 'error detail' >&2 && exit 1",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result, "some output") {
		t.Errorf("stdout should be preserved on failure, got: %q", result)
	}
	if !strings.Contains(result, "error detail") {
		t.Errorf("stderr should be preserved on failure, got: %q", result)
	}
}

func TestReadToolBinaryGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.dat")
	// Write binary content with null bytes.
	data := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x00, 0x00, 0x00}
	data = append(data, make([]byte, 100)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	_, err := tool.Execute(context.Background(), map[string]any{"file_path": path})
	if err == nil {
		t.Fatal("expected error for binary file")
	}
	if !strings.Contains(err.Error(), "binary file detected") {
		t.Errorf("error should mention binary detection, got: %v", err)
	}
}

func TestReadToolTextFilePassesBinaryGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "text.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"file_path": path})
	if err != nil {
		t.Fatalf("text file should pass binary guard: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("should contain file content, got: %q", result)
	}
}

func TestBashToolMissingArg(t *testing.T) {
	tool := &BashTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestEditTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
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
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello Go" {
		t.Errorf("file content = %q, want %q", string(data), "hello Go")
	}
}

func TestEditToolNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "missing",
		"new_string": "replacement",
	})
	if err == nil {
		t.Fatal("expected error when old_string not found")
	}
}

func TestEditToolAmbiguous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("aa bb aa"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "aa",
		"new_string": "cc",
	})
	if err == nil {
		t.Fatal("expected error when old_string matches multiple times")
	}
}

func TestEditToolMissingArgs(t *testing.T) {
	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing args")
	}
}

func TestWriteTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "test.txt")

	tool := &WriteTool{}
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": path,
		"content":   "new content",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new content" {
		t.Errorf("file content = %q, want %q", string(data), "new content")
	}
}

func TestWriteToolOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &WriteTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path": path,
		"content":   "new",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("file content = %q, want %q", string(data), "new")
	}
}

func TestWriteToolMissingArg(t *testing.T) {
	tool := &WriteTool{}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing file_path")
	}
}

func TestWebFetchToolNoContent(t *testing.T) {
	// Serve minimal HTML that readability cannot extract content from (nil Node).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Empty body with just a title — readability will parse but produce nil Node.
		_, _ = fmt.Fprint(w, `<html><head><title>Test Page</title></head><body><script>app()</script></body></html>`)
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	result, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("expected no error for nil-Node page, got: %v", err)
	}
	if !strings.Contains(result, "No readable content") {
		t.Errorf("expected fallback message, got: %q", result)
	}
	if !strings.Contains(result, "Test Page") {
		t.Errorf("expected title in fallback, got: %q", result)
	}
}

func TestWebFetchToolSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>Article</title></head><body><article><p>Hello world. This is a test article with enough content for readability to extract.</p><p>Second paragraph with more details about the topic at hand.</p></article></body></html>`)
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	result, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestBuildNoContentMessage(t *testing.T) {
	msg := buildNoContentMessage("https://example.com/page", readability.Article{})
	if !strings.Contains(msg, "No readable content") {
		t.Error("missing header")
	}
	if !strings.Contains(msg, "https://example.com/page") {
		t.Error("missing URL")
	}
	if !strings.Contains(msg, "JavaScript") {
		t.Error("missing guidance hint")
	}
}
