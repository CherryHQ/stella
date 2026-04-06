package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/pkg/tools"
)

// echoTool returns args as a string for inspection.
type echoTool struct{}

func (e *echoTool) Definition() tools.Definition {
	return tools.Definition{Name: "echo", Description: "echo"}
}

func (e *echoTool) Execute(_ context.Context, args map[string]any) (string, error) {
	if p, ok := args["file_path"].(string); ok {
		return p, nil
	}
	return "ok", nil
}

func TestWrapWithSandbox_NoDir(t *testing.T) {
	base := &echoTool{}
	wrapped := WrapWithSandbox(base, "", "file_path")
	// When allowedDir is empty, the original tool should be returned unchanged.
	if wrapped != base {
		t.Error("expected original tool when allowedDir is empty")
	}
}

func TestWrapWithSandbox_AllowsInsidePath(t *testing.T) {
	dir := t.TempDir()
	wrapped := WrapWithSandbox(&echoTool{}, dir, "file_path")

	args := map[string]any{"file_path": filepath.Join(dir, "test.txt")}
	result, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != filepath.Join(dir, "test.txt") {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestWrapWithSandbox_BlocksOutsidePath(t *testing.T) {
	dir := t.TempDir()
	wrapped := WrapWithSandbox(&echoTool{}, dir, "file_path")

	args := map[string]any{"file_path": "/etc/passwd"}
	_, err := wrapped.Execute(context.Background(), args)
	if err == nil {
		t.Error("expected error for path outside allowed dir")
	}
}

func TestWrapWithSandbox_NoPathKey(t *testing.T) {
	dir := t.TempDir()
	wrapped := WrapWithSandbox(&echoTool{}, dir, "file_path")

	// No file_path arg — sandbox should pass through.
	args := map[string]any{"other": "value"}
	_, err := wrapped.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWrapWithSandbox_Definition(t *testing.T) {
	dir := t.TempDir()
	base := &echoTool{}
	wrapped := WrapWithSandbox(base, dir, "file_path")

	if wrapped.Definition().Name != base.Definition().Name {
		t.Error("sandbox wrapper should delegate Definition()")
	}
}

func TestValidatePath_AllowsExactDir(t *testing.T) {
	dir := t.TempDir()
	// Requesting the allowed dir itself should fail (not a file inside it),
	// but validatePath allows it.
	if err := validatePath(dir, dir); err != nil {
		t.Errorf("unexpected error for exact dir: %v", err)
	}
}

func TestValidatePath_AllowsSubpath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub", "file.txt")
	if err := validatePath(dir, sub); err != nil {
		t.Errorf("unexpected error for sub-path: %v", err)
	}
}

func TestValidatePath_BlocksOutside(t *testing.T) {
	dir := t.TempDir()
	if err := validatePath(dir, "/tmp"); err == nil {
		t.Error("expected error for /tmp outside allowed dir")
	}
}

func TestValidatePath_EmptyDir(t *testing.T) {
	if err := validatePath("", "/any/path"); err != nil {
		t.Errorf("empty allowedDir should be no-op, got: %v", err)
	}
}

func TestValidatePath_TraversalAttempt(t *testing.T) {
	dir := t.TempDir()
	// Path traversal using ..
	outside := filepath.Join(dir, "..", "..", "etc", "passwd")
	if err := validatePath(dir, outside); err == nil {
		t.Error("expected error for path traversal attempt")
	}
}

func TestSandboxTool_Close_WithCloseableInner(t *testing.T) {
	dir := t.TempDir()
	inner := &struct {
		echoTool
	}{}

	// Wrap a non-closeable inner — Close should be a no-op.
	wrapped := WrapWithSandbox(inner, dir, "file_path").(*sandboxTool)
	if err := wrapped.Close(); err != nil {
		t.Errorf("unexpected error on Close: %v", err)
	}
}

func TestResolvePathBestEffort_ExistingPath(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "test-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	resolved, err := resolvePathBestEffort(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved == "" {
		t.Error("expected non-empty resolved path")
	}
}

func TestResolvePathBestEffort_NonExistentPath(t *testing.T) {
	dir := t.TempDir()
	// Non-existent file inside existing dir.
	path := filepath.Join(dir, "new_file.txt")
	resolved, err := resolvePathBestEffort(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved == "" {
		t.Error("expected non-empty resolved path")
	}
}
