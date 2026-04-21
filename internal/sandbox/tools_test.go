package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	boxshplugin "github.com/vaayne/anna/plugins/sandbox/boxsh"
	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"

	pkgtools "github.com/vaayne/anna/pkg/tools"
)

// mustCreateBoxshSession creates a boxsh session for testing, skipping if boxsh
// is not available on the current platform or the managed binary is missing.
func mustCreateBoxshSession(t *testing.T, policy Policy) Session {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("boxsh not available on this platform")
	}
	annaHome := os.Getenv("ANNA_HOME")
	if annaHome == "" {
		t.Skip("ANNA_HOME not set; boxsh binary not available")
	}
	if _, err := boxshclient.ResolveManagedBoxshPath(annaHome); err != nil {
		t.Skipf("boxsh binary not available: %v", err)
	}
	factory := boxshplugin.NewFactory()
	session, err := factory.CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return session
}

func TestNewCoreToolsLocalParity(t *testing.T) {
	workspace := t.TempDir()
	binDir := t.TempDir()
	toolPath := filepath.Join(binDir, "hello-tool")
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\necho hello-from-tool\n"), 0o755); err != nil {
		t.Fatalf("WriteFile tool: %v", err)
	}

	session := mustCreateBoxshSession(t, Policy{
		Backend: "boxsh",
		Filesystem: FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    workspace,
			AllowEscapes:  false,
		},
		Process: ProcessPolicy{InheritEnv: true},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), binDir, workspace))

	writeResult, err := toolByName["write"].Execute(context.Background(), map[string]any{
		"path":    "nested/notes.txt",
		"content": "line1\nline2\nline3\n",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(writeResult, "Wrote nested/notes.txt") {
		t.Fatalf("unexpected write result: %q", writeResult)
	}

	readResult, err := toolByName["read"].Execute(context.Background(), map[string]any{
		"path":   "nested/notes.txt",
		"offset": 2,
		"limit":  1,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(readResult, "line2") || !strings.Contains(readResult, "offset=3") {
		t.Fatalf("unexpected read result: %q", readResult)
	}

	editResult, err := toolByName["edit"].Execute(context.Background(), map[string]any{
		"path":    "nested/notes.txt",
		"oldText": "line2",
		"newText": "updated",
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

func TestNewCoreToolsFallsBackToHostWorkingDirWithoutProjectRoot(t *testing.T) {
	workspace := t.TempDir()
	session := mustCreateBoxshSession(t, Policy{
		Backend:    "boxsh",
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
		Process:    ProcessPolicy{InheritEnv: true},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), "", ""))
	if _, err := toolByName["write"].Execute(context.Background(), map[string]any{"path": "fallback.txt", "content": "hello"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	readResult, err := toolByName["read"].Execute(context.Background(), map[string]any{"path": "fallback.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(readResult, "hello") {
		t.Fatalf("unexpected read result: %q", readResult)
	}
	bashResult, err := toolByName["bash"].Execute(context.Background(), map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(bashResult, workspace) {
		t.Fatalf("expected bash to use host working dir, got %q", bashResult)
	}
}

func TestNewCoreToolsReadRejectsBinaryFiles(t *testing.T) {
	workspace := t.TempDir()
	session := mustCreateBoxshSession(t, Policy{
		Backend:    "boxsh",
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), "", workspace))
	path := filepath.Join(workspace, "blob.bin")
	if err := os.WriteFile(path, []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := toolByName["read"].Execute(context.Background(), map[string]any{"path": path})
	if err == nil || !strings.Contains(err.Error(), "binary file detected") {
		t.Fatalf("expected binary file error, got %v", err)
	}
}

func TestNewCoreToolsReadLongFirstLineWithLimitKeepsContinuationAccurate(t *testing.T) {
	workspace := t.TempDir()
	session := mustCreateBoxshSession(t, Policy{
		Backend:    "boxsh",
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), "", workspace))
	path := filepath.Join(workspace, "long.txt")
	longLine := strings.Repeat("a", 60*1024)
	if err := os.WriteFile(path, []byte(longLine+"\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := toolByName["read"].Execute(context.Background(), map[string]any{
		"path":   path,
		"offset": 1,
		"limit":  3,
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
	session := mustCreateBoxshSession(t, Policy{
		Backend:    "boxsh",
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), "", workspace))
	path := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(path, []byte("same\nsame\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := toolByName["edit"].Execute(context.Background(), map[string]any{
		"path":       path,
		"old_string": "same",
		"new_string": "new",
	})
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("expected unique match error, got %v", err)
	}
}

func TestNewCoreToolsBashTimeout(t *testing.T) {
	workspace := t.TempDir()
	session := mustCreateBoxshSession(t, Policy{
		Backend:    "boxsh",
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: workspace},
	})
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), "", workspace))
	result, err := toolByName["bash"].Execute(context.Background(), map[string]any{
		"command": "sleep 2",
		"timeout": 1,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(result, "timed out") {
		t.Fatalf("expected timeout text, got %q", result)
	}
}

func mapToolsByName(items []pkgtools.Tool) map[string]pkgtools.Tool {
	out := make(map[string]pkgtools.Tool, len(items))
	for _, item := range items {
		out[item.Definition().Name] = item
	}
	return out
}
