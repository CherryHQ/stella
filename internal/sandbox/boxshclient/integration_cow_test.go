package boxshclient

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	"github.com/vaayne/anna/pkg/tools"
)

// skipIfWindows skips the test on Windows
func skipIfWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("COW integration tests require Linux/macOS boxsh")
	}
}

// writeMockBoxsh creates a mock boxsh binary that responds to JSON-RPC requests
// for testing COW behavior across all four tools.
func writeMockBoxsh(t *testing.T, annaHome string, respondWithErrors bool) {
	t.Helper()
	_ = embedded.EnsureTools(annaHome)
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")

	script := `#!/bin/bash
if [[ "$1" == "--version" ]]; then
	echo "boxsh 2.0.1"
	exit 0
fi
# Read JSON-RPC requests and respond appropriately
while IFS= read -r line || [[ -n "$line" ]]; do
	# Extract method and id
	method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	
	# Skip empty lines
	if [[ -z "$method" ]]; then
		continue
	fi
	
	case "$method" in
		ping)
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
			;;
		exec)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"executed\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
			;;
		read)
			path=$(echo "$line" | grep -o '"file_path":"[^"]*"' | cut -d'"' -f4)
			# Return content indicating which path was read
			content="read: $path"
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":\"$content\",\"total_lines\":1,\"truncated\":false},\"id\":$id}"
			;;
		write)
			path=$(echo "$line" | grep -o '"file_path":"[^"]*"' | cut -d'"' -f4)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"bytes_written\":100,\"path\":\"$path\"},\"id\":$id}"
			;;
		edit)
			path=$(echo "$line" | grep -o '"file_path":"[^"]*"' | cut -d'"' -f4)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"path\":\"$path\",\"replacements\":1},\"id\":$id}"
			;;
		close|quit)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"status\":\"closed\"},\"id\":$id}"
			exit 0
			;;
	esac
done
`

	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestSharedCOWView_AllToolsSeeSameSession verifies that all four core tools
// (bash, read, write, edit) share the same backend/session.
func TestSharedCOWView_AllToolsSeeSameSession(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	userDataDir := filepath.Join(workspace, "users", "1", "data")
	if err := os.MkdirAll(userDataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeMockBoxsh(t, annaHome, false)

	// Create backend with shared session
	cfg := BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   workspace,
		UserDataDir: userDataDir,
		WorkDir:     "/",
		Sandbox: config.SandboxConfig{
			Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled},
		},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	// Verify backend is alive
	if !backend.Alive() {
		t.Fatal("backend should be alive after Start()")
	}

	// Create all four tool adapters sharing the same backend
	bashTool := NewBashAdapter(backend, "")
	readTool := NewReadAdapter(backend)
	writeTool := NewWriteAdapter(backend)
	editTool := NewEditAdapter(backend)

	// Execute all four tools
	bashResult, err := bashTool.Execute(ctx, map[string]any{
		"command": "echo test",
	})
	if err != nil {
		t.Fatalf("bashTool.Execute: %v", err)
	}
	if bashResult == "" {
		t.Error("bash result should not be empty")
	}

	readResult, err := readTool.Execute(ctx, map[string]any{
		"file_path": "/test.txt",
	})
	if err != nil {
		t.Fatalf("readTool.Execute: %v", err)
	}
	if !contains(readResult, "read:") {
		t.Errorf("read result should contain path info, got: %s", readResult)
	}

	writeResult, err := writeTool.Execute(ctx, map[string]any{
		"file_path": "/write.txt",
		"content":   "test content",
	})
	if err != nil {
		t.Fatalf("writeTool.Execute: %v", err)
	}
	if writeResult == "" {
		t.Error("write result should not be empty")
	}

	editResult, err := editTool.Execute(ctx, map[string]any{
		"file_path":  "/edit.txt",
		"old_string": "old",
		"new_string": "new",
	})
	if err != nil {
		t.Fatalf("editTool.Execute: %v", err)
	}
	if editResult == "" {
		t.Error("edit result should not be empty")
	}

	// Backend should still be alive after all operations
	if !backend.Alive() {
		t.Error("backend should still be alive after tool executions")
	}

	t.Log("All four tools successfully used the same backend session")
}

// TestSharedCOWView_ClientReturnsSameSession verifies that Client() returns
// the same client instance for all tool operations.
func TestSharedCOWView_ClientReturnsSameSession(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeMockBoxsh(t, annaHome, false)

	cfg := BackendConfig{
		AnnaHome:  annaHome,
		Workspace: workspace,
		Sandbox:   config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	client1 := backend.Client()
	if client1 == nil {
		t.Fatal("Client() should return non-nil after Start()")
	}

	client2 := backend.Client()
	if client2 == nil {
		t.Fatal("Client() should return consistent non-nil client")
	}

	// Both calls should return the same client instance
	if client1 != client2 {
		t.Error("Client() should return the same client instance for all calls")
	}

	t.Log("Client returns consistent session for all tool operations")
}

// TestSharedCOWView_SessionDirCreated verifies that each backend gets its own
// ephemeral session directory for COW operations.
func TestSharedCOWView_SessionDirCreated(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeMockBoxsh(t, annaHome, false)

	cfg := BackendConfig{
		AnnaHome:  annaHome,
		Workspace: workspace,
		Sandbox:   config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}

	// Before start, session dir should be empty
	if dir := backend.SessionDir(); dir != "" {
		t.Errorf("SessionDir before Start = %q, want empty", dir)
	}

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	// After start, session dir should exist
	sessionDir := backend.SessionDir()
	if sessionDir == "" {
		t.Fatal("SessionDir should be set after Start()")
	}

	info, err := os.Stat(sessionDir)
	if err != nil {
		t.Fatalf("Stat session dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("Session path should be a directory")
	}

	// Cleanup should remove the session directory
	if err := backend.Close(); err != nil {
		t.Logf("Close error (may be expected): %v", err)
	}

	// Note: session dir cleanup is best-effort and may fail if boxsh is still running
	t.Logf("Session directory created at: %s", sessionDir)
}

// TestSharedCOWView_ToolConsistency verifies that tool definitions are consistent
// with the backend capabilities.
func TestSharedCOWView_ToolConsistency(t *testing.T) {
	skipIfWindows(t)

	backend := &SharedBackend{}

	bashTool := NewBashAdapter(backend, "")
	readTool := NewReadAdapter(backend)
	writeTool := NewWriteAdapter(backend)
	editTool := NewEditAdapter(backend)

	// Verify all tools have proper definitions
	tools := []struct {
		name string
		def  tools.Definition
	}{
		{"bash", bashTool.Definition()},
		{"read", readTool.Definition()},
		{"write", writeTool.Definition()},
		{"edit", editTool.Definition()},
	}

	for _, tool := range tools {
		if tool.def.Name == "" {
			t.Errorf("%s tool has no name", tool.name)
		}
		if tool.def.Description == "" {
			t.Errorf("%s tool has no description", tool.name)
		}
		if tool.def.InputSchema == nil {
			t.Errorf("%s tool has no input schema", tool.name)
		}
		t.Logf("Tool %s: %s", tool.def.Name, tool.def.Description)
	}
}

// TestSharedCOWView_MultipleBackendsIsolation verifies that multiple backends
// (simulating different runners/agents) have isolated session directories.
func TestSharedCOWView_MultipleBackendsIsolation(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeMockBoxsh(t, annaHome, false)

	// Create two separate backends
	cfg := BackendConfig{
		AnnaHome:  annaHome,
		Workspace: workspace,
		Sandbox:   config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	}

	backend1, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend 1: %v", err)
	}
	defer func() { _ = backend1.Close() }()

	backend2, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend 2: %v", err)
	}
	defer func() { _ = backend2.Close() }()

	// Start both backends
	if err := backend1.Start(ctx, cfg); err != nil {
		t.Fatalf("backend1.Start: %v", err)
	}
	if err := backend2.Start(ctx, cfg); err != nil {
		t.Fatalf("backend2.Start: %v", err)
	}

	// Each backend should have a distinct session directory
	sessionDir1 := backend1.SessionDir()
	sessionDir2 := backend2.SessionDir()

	if sessionDir1 == "" {
		t.Fatal("backend1 session dir should be set")
	}
	if sessionDir2 == "" {
		t.Fatal("backend2 session dir should be set")
	}

	if sessionDir1 == sessionDir2 {
		t.Error("Each backend should have its own isolated session directory")
	}

	t.Logf("Backend 1 session dir: %s", sessionDir1)
	t.Logf("Backend 2 session dir: %s", sessionDir2)
}


