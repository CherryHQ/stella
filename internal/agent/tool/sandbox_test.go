package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/toolspec"
)

// fakeTool is a test double that records calls.
type fakeTool struct {
	name       string
	called     bool
	calledArgs map[string]any
}

func (f *fakeTool) Definition() toolspec.Definition {
	return toolspec.Definition{Name: f.name}
}

func (f *fakeTool) Execute(_ context.Context, args map[string]any) (string, error) {
	f.called = true
	f.calledArgs = args
	return "ok", nil
}

func TestSandboxToolAllowed(t *testing.T) {
	dir := t.TempDir()
	inner := &fakeTool{name: "read"}
	wrapped := WrapWithSandbox(inner, dir, "file_path")

	_, err := wrapped.Execute(context.Background(), map[string]any{
		"file_path": filepath.Join(dir, "test.txt"),
	})
	if err != nil {
		t.Errorf("expected allowed path: %v", err)
	}
	if !inner.called {
		t.Error("inner tool should have been called")
	}
}

func TestSandboxToolBlocked(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	inner := &fakeTool{name: "read"}
	wrapped := WrapWithSandbox(inner, dir, "file_path")

	_, err := wrapped.Execute(context.Background(), map[string]any{
		"file_path": filepath.Join(other, "evil.txt"),
	})
	if err == nil {
		t.Error("expected sandbox error for path outside allowed dir")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("expected sandbox error, got: %v", err)
	}
	if inner.called {
		t.Error("inner tool should NOT have been called")
	}
}

func TestSandboxToolNoSandbox(t *testing.T) {
	// Empty allowed dir means no sandbox.
	inner := &fakeTool{name: "write"}
	wrapped := WrapWithSandbox(inner, "", "file_path")

	// Should be the original tool, not wrapped.
	if wrapped != inner {
		t.Error("expected original tool when sandbox is empty")
	}
}

func TestSandboxToolPreservesDefinition(t *testing.T) {
	inner := &fakeTool{name: "edit"}
	wrapped := WrapWithSandbox(inner, "/some/dir", "file_path")

	if wrapped.Definition().Name != "edit" {
		t.Errorf("expected definition name 'edit', got %q", wrapped.Definition().Name)
	}
}

func TestSandboxToolSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()

	// Create a symlink inside dir pointing outside.
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	inner := &fakeTool{name: "read"}
	wrapped := WrapWithSandbox(inner, dir, "file_path")

	_, err := wrapped.Execute(context.Background(), map[string]any{
		"file_path": filepath.Join(link, "secret.txt"),
	})
	if err == nil {
		t.Error("expected sandbox error for symlink escape")
	}
}

func TestNewRegistryWithSandbox(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry("", dir)

	// The registry should have tools.
	if !reg.Has("read") {
		t.Error("expected read tool")
	}
	if !reg.Has("bash") {
		t.Error("expected bash tool")
	}
	if !reg.Has("edit") {
		t.Error("expected edit tool")
	}
	if !reg.Has("write") {
		t.Error("expected write tool")
	}
}

func TestNewRegistryWithoutSandbox(t *testing.T) {
	reg := NewRegistry("")

	if !reg.Has("read") {
		t.Error("expected read tool")
	}
}
