package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgtools "github.com/vaayne/anna/pkg/tools"
)

func TestNewCoreToolsLocalParity(t *testing.T) {
	workspace := t.TempDir()
	binDir := t.TempDir()
	toolPath := filepath.Join(binDir, "hello-tool")
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\necho hello-from-tool\n"), 0o755); err != nil {
		t.Fatalf("WriteFile tool: %v", err)
	}

	session := mustCreateLocalSession(t, Policy{
		Backend: "local",
		Relaxed: true,
		Filesystem: FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    workspace,
			AllowEscapes:  false,
		},
		Process: ProcessPolicy{InheritEnv: true},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), binDir))

	writeResult, err := toolByName["write"].Execute(context.Background(), map[string]any{
		"file_path": "nested/notes.txt",
		"content":   "line1\nline2\nline3\n",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(writeResult, "Wrote nested/notes.txt") {
		t.Fatalf("unexpected write result: %q", writeResult)
	}

	readResult, err := toolByName["read"].Execute(context.Background(), map[string]any{
		"file_path": "nested/notes.txt",
		"offset":    2,
		"limit":     1,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(readResult, "line2") || !strings.Contains(readResult, "offset=3") {
		t.Fatalf("unexpected read result: %q", readResult)
	}

	editResult, err := toolByName["edit"].Execute(context.Background(), map[string]any{
		"file_path":  "nested/notes.txt",
		"old_string": "line2",
		"new_string": "updated",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(editResult, "Edited nested/notes.txt") {
		t.Fatalf("unexpected edit result: %q", editResult)
	}

	bashResult, err := toolByName["bash"].Execute(context.Background(), map[string]any{"command": "hello-tool"})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(bashResult, "hello-from-tool") || !strings.Contains(bashResult, "[exit:0") {
		t.Fatalf("unexpected bash result: %q", bashResult)
	}
}

func TestNewCoreToolsReadRejectsBinaryFiles(t *testing.T) {
	workspace := t.TempDir()
	session := mustCreateLocalSession(t, Policy{
		Backend:    "local",
		Relaxed:    true,
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), ""))
	path := filepath.Join(workspace, "blob.bin")
	if err := os.WriteFile(path, []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := toolByName["read"].Execute(context.Background(), map[string]any{"file_path": path})
	if err == nil || !strings.Contains(err.Error(), "binary file detected") {
		t.Fatalf("expected binary file error, got %v", err)
	}
}

func TestNewCoreToolsReadLongFirstLineWithLimitKeepsContinuationAccurate(t *testing.T) {
	workspace := t.TempDir()
	session := mustCreateLocalSession(t, Policy{
		Backend:    "local",
		Relaxed:    true,
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), ""))
	path := filepath.Join(workspace, "long.txt")
	longLine := strings.Repeat("a", 60*1024)
	if err := os.WriteFile(path, []byte(longLine+"\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := toolByName["read"].Execute(context.Background(), map[string]any{
		"file_path": path,
		"offset":    1,
		"limit":     3,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(result, "offset=2") {
		t.Fatalf("expected continuation to advance to line 2, got %q", result)
	}
}

func TestNewCoreToolsEditRequiresUniqueMatch(t *testing.T) {
	workspace := t.TempDir()
	session := mustCreateLocalSession(t, Policy{
		Backend:    "local",
		Relaxed:    true,
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), ""))
	path := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(path, []byte("same\nsame\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := toolByName["edit"].Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "same",
		"new_string": "new",
	})
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("expected unique match error, got %v", err)
	}
}

func mustCreateLocalSession(t *testing.T, policy Policy) Session {
	t.Helper()
	session, err := (&localFactory{}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return session
}

func mapToolsByName(items []pkgtools.Tool) map[string]pkgtools.Tool {
	out := make(map[string]pkgtools.Tool, len(items))
	for _, item := range items {
		out[item.Definition().Name] = item
	}
	return out
}
