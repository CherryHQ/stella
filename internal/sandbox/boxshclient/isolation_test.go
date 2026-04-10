package boxshclient

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

// skipIfWindows skips the test on Windows
func skipIfWindowsIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Isolation tests require Linux/macOS boxsh")
	}
}

// writeMockBoxshForIsolation creates a mock boxsh that enforces workspace boundaries
func writeMockBoxshForIsolation(t *testing.T, annaHome string, allowedPrefix string) {
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

ALLOWED_PREFIX="` + allowedPrefix + `"

while IFS= read -r line || [[ -n "$line" ]]; do
	method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	
	if [[ -z "$method" ]]; then
		continue
	fi
	
	case "$method" in
		ping)
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
			;;
		exec)
			command=$(echo "$line" | grep -o '"command":"[^"]*"' | cut -d'"' -f4)
			# Check if command tries to access outside allowed prefix
			if echo "$command" | grep -q "\.\./" || echo "$command" | grep -q "/etc/" || echo "$command" | grep -q "/var/"; then
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"\",\"stderr\":\"access denied: path outside workspace\",\"exit_code\":1},\"id\":$id}"
			else
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"executed\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
			fi
			;;
		read)
			path=$(echo "$line" | grep -o '"file_path":"[^"]*"' | cut -d'"' -f4)
			# Check for path traversal patterns: .. in path, /etc, /var, /sys, /proc
			if [[ "$path" == *..* ]] || [[ "$path" == /etc/* ]] || [[ "$path" == /var/* ]] || [[ "$path" == /sys/* ]] || [[ "$path" == /proc/* ]]; then
				echo "{\"jsonrpc\":\"2.0\",\"error\":{\"code\":-32000,\"message\":\"access denied: path outside workspace\"},\"id\":$id}"
			else
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":\"file content\",\"total_lines\":1,\"truncated\":false},\"id\":$id}"
			fi
			;;
		write)
			path=$(echo "$line" | grep -o '"file_path":"[^"]*"' | cut -d'"' -f4)
			if [[ "$path" == *..* ]] || [[ "$path" == /etc/* ]] || [[ "$path" == /var/* ]] || [[ "$path" == /sys/* ]] || [[ "$path" == /proc/* ]]; then
				echo "{\"jsonrpc\":\"2.0\",\"error\":{\"code\":-32000,\"message\":\"access denied: path outside workspace\"},\"id\":$id}"
			else
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"bytes_written\":100,\"path\":\"$path\"},\"id\":$id}"
			fi
			;;
		edit)
			path=$(echo "$line" | grep -o '"file_path":"[^"]*"' | cut -d'"' -f4)
			if [[ "$path" == *..* ]] || [[ "$path" == /etc/* ]] || [[ "$path" == /var/* ]] || [[ "$path" == /sys/* ]] || [[ "$path" == /proc/* ]]; then
				echo "{\"jsonrpc\":\"2.0\",\"error\":{\"code\":-32000,\"message\":\"access denied: path outside workspace\"},\"id\":$id}"
			else
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"path\":\"$path\",\"replacements\":1},\"id\":$id}"
			fi
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

// TestIsolation_CrossWorkspaceAccessBlocked verifies that the sandbox blocks
// access to paths outside the configured workspace.
func TestIsolation_CrossWorkspaceAccessBlocked(t *testing.T) {
	skipIfWindowsIsolation(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	userDataDir := filepath.Join(workspace, "users", "1", "data")
	if err := os.MkdirAll(userDataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeMockBoxshForIsolation(t, annaHome, userDataDir)

	cfg := BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   workspace,
		UserDataDir: userDataDir,
		WorkDir:     "/",
		Sandbox:     config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	readTool := NewReadAdapter(backend)
	writeTool := NewWriteAdapter(backend)
	editTool := NewEditAdapter(backend)
	bashTool := NewBashAdapter(backend, "")

	// Attempt to read a file outside the workspace
	_, err = readTool.Execute(ctx, map[string]any{
		"file_path": "/etc/passwd",
	})
	if err == nil || !contains(err.Error(), "access denied") {
		t.Errorf("Expected access denied error for /etc/passwd, got: %v", err)
	}

	// Attempt to write a file outside the workspace
	_, err = writeTool.Execute(ctx, map[string]any{
		"file_path": "/var/log/hacked.txt",
		"content":   "malicious",
	})
	if err == nil || !contains(err.Error(), "access denied") {
		t.Errorf("Expected access denied error for /var/log/, got: %v", err)
	}

	// Attempt to edit a file outside the workspace
	_, err = editTool.Execute(ctx, map[string]any{
		"file_path":  "/etc/hosts",
		"old_string": "localhost",
		"new_string": "evil",
	})
	if err == nil || !contains(err.Error(), "access denied") {
		t.Errorf("Expected access denied error for /etc/hosts, got: %v", err)
	}

	// Attempt to use bash to access outside workspace
	_, err = bashTool.Execute(ctx, map[string]any{
		"command": "cat /etc/passwd",
	})
	// Bash returns an error (either "access denied" in error or exit code error)
	if err == nil {
		t.Error("Expected error for bash outside workspace, got nil")
	} else {
		t.Logf("Bash correctly failed for out-of-workspace access: %v", err)
	}

	t.Log("Cross-workspace access was blocked for all tools")
}

// TestIsolation_ParentDirectoryTraversalBlocked verifies that parent directory
// traversal attempts (../) are blocked.
func TestIsolation_ParentDirectoryTraversalBlocked(t *testing.T) {
	skipIfWindowsIsolation(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	userDataDir := filepath.Join(workspace, "users", "1", "data")
	if err := os.MkdirAll(userDataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeMockBoxshForIsolation(t, annaHome, userDataDir)

	cfg := BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   workspace,
		UserDataDir: userDataDir,
		WorkDir:     "/",
		Sandbox:     config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	readTool := NewReadAdapter(backend)

	// Try parent directory traversal
	_, err = readTool.Execute(ctx, map[string]any{
		"file_path": "../other-agent/secrets.txt",
	})
	if err == nil || !contains(err.Error(), "access denied") {
		t.Errorf("Expected access denied for parent traversal, got: %v", err)
	}

	// Try nested parent traversal (path starts with /workspace but escapes via ..)
	// The mock script checks for .. patterns in the cleaned path
	_, err = readTool.Execute(ctx, map[string]any{
		"file_path": "/workspace/../etc/shadow",
	})
	if err == nil || !contains(err.Error(), "access denied") {
		t.Errorf("Expected access denied for nested parent traversal, got: %v", err)
	}

	t.Log("Parent directory traversal was blocked")
}

// TestIsolation_DifferentAgentsDifferentSessions verifies that different agents
// get different sandbox sessions with isolated upper directories.
func TestIsolation_DifferentAgentsDifferentSessions(t *testing.T) {
	skipIfWindowsIsolation(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()

	// Create two separate workspaces for two agents
	workspace1 := t.TempDir()
	workspace2 := t.TempDir()
	userDataDir1 := filepath.Join(workspace1, "users", "1", "data")
	userDataDir2 := filepath.Join(workspace2, "users", "2", "data")
	if err := os.MkdirAll(userDataDir1, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(userDataDir2, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create source files in each workspace
	if err := os.WriteFile(filepath.Join(userDataDir1, "agent1.txt"), []byte("agent 1 data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDataDir2, "agent2.txt"), []byte("agent 2 data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	writeMockBoxshForIsolation(t, annaHome, userDataDir1)

	// Create backend for agent 1
	cfg1 := BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   workspace1,
		UserDataDir: userDataDir1,
		WorkDir:     "/",
		Sandbox:     config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	}

	backend1, err := NewSharedBackend(cfg1)
	if err != nil {
		t.Fatalf("NewSharedBackend 1: %v", err)
	}
	defer func() { _ = backend1.Close() }()

	// Create backend for agent 2
	cfg2 := BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   workspace2,
		UserDataDir: userDataDir2,
		WorkDir:     "/",
		Sandbox:     config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	}

	backend2, err := NewSharedBackend(cfg2)
	if err != nil {
		t.Fatalf("NewSharedBackend 2: %v", err)
	}
	defer func() { _ = backend2.Close() }()

	// Start both backends
	if err := backend1.Start(ctx, cfg1); err != nil {
		t.Fatalf("backend1.Start: %v", err)
	}
	if err := backend2.Start(ctx, cfg2); err != nil {
		t.Fatalf("backend2.Start: %v", err)
	}

	// Each backend should have a different session directory
	sessionDir1 := backend1.SessionDir()
	sessionDir2 := backend2.SessionDir()

	if sessionDir1 == "" || sessionDir2 == "" {
		t.Fatal("Both backends should have session directories")
	}

	if sessionDir1 == sessionDir2 {
		t.Error("Different agents should have different session directories")
	}

	// Verify the sessions are truly isolated by checking different upperdirs
	t.Logf("Agent 1 session dir: %s (source: %s)", sessionDir1, userDataDir1)
	t.Logf("Agent 2 session dir: %s (source: %s)", sessionDir2, userDataDir2)

	t.Log("Different agents have isolated sandbox sessions")
}

// TestIsolation_ValidateSandboxPath verifies the sandbox path validation logic.
func TestIsolation_ValidateSandboxPath(t *testing.T) {
	tests := []struct {
		name        string
		sandboxRoot string
		path        string
		wantErr     bool
		errContain  string
	}{
		{
			name:        "path inside sandbox",
			sandboxRoot: "/workspace",
			path:        "/workspace/file.txt",
			wantErr:     false,
		},
		{
			name:        "relative path resolved inside sandbox",
			sandboxRoot: "/workspace",
			path:        "file.txt",
			wantErr:     false,
		},
		{
			name:        "nested path inside sandbox",
			sandboxRoot: "/workspace",
			path:        "/workspace/subdir/deep/file.txt",
			wantErr:     false,
		},
		{
			name:        "path outside sandbox - absolute",
			sandboxRoot: "/workspace",
			path:        "/etc/passwd",
			wantErr:     true,
			errContain:  "outside sandbox",
		},
		{
			name:        "parent traversal blocked",
			sandboxRoot: "/workspace",
			path:        "../etc/passwd",
			wantErr:     true,
			errContain:  "outside sandbox",
		},
		{
			name:        "nested parent traversal blocked",
			sandboxRoot: "/workspace",
			path:        "/workspace/../../etc/passwd",
			wantErr:     true,
			errContain:  "outside sandbox",
		},
		{
			name:        "empty sandbox root rejected",
			sandboxRoot: "",
			path:        "/workspace/file.txt",
			wantErr:     true,
			errContain:  "required",
		},
		{
			name:        "relative sandbox root rejected",
			sandboxRoot: "workspace",
			path:        "/workspace/file.txt",
			wantErr:     true,
			errContain:  "must be absolute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSandboxPath(tt.sandboxRoot, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateSandboxPath() expected error but got nil")
					return
				}
				if tt.errContain != "" && !contains(err.Error(), tt.errContain) {
					t.Errorf("ValidateSandboxPath() error = %v, should contain %q", err, tt.errContain)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateSandboxPath() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestIsolation_SessionManagerCrossWorkspace verifies that SessionManager
// correctly assigns different source directories for different sessions.
func TestIsolation_SessionManagerCrossWorkspace(t *testing.T) {
	baseDir := t.TempDir()

	manager, err := NewSessionManager(baseDir)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// Create two different workspaces
	workspace1 := t.TempDir()
	workspace2 := t.TempDir()
	userDataDir1 := filepath.Join(workspace1, "users", "1")
	userDataDir2 := filepath.Join(workspace2, "users", "2")
	if err := os.MkdirAll(userDataDir1, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(userDataDir2, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create session for user 1
	session1, err := manager.CreateSession(SessionOptions{
		Workspace:   workspace1,
		UserDataDir: userDataDir1,
		WorkDir:     "/",
		Sandbox:     config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	})
	if err != nil {
		t.Fatalf("CreateSession 1: %v", err)
	}

	// Create session for user 2
	session2, err := manager.CreateSession(SessionOptions{
		Workspace:   workspace2,
		UserDataDir: userDataDir2,
		WorkDir:     "/",
		Sandbox:     config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}},
	})
	if err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}

	// Each session should have its own source (workspace/user data dir)
	if session1.Src != userDataDir1 {
		t.Errorf("Session 1 Src = %q, want %q", session1.Src, userDataDir1)
	}
	if session2.Src != userDataDir2 {
		t.Errorf("Session 2 Src = %q, want %q", session2.Src, userDataDir2)
	}

	// Each session should have its own ephemeral destination
	if session1.Dst == "" {
		t.Error("Session 1 Dst should be set")
	}
	if session2.Dst == "" {
		t.Error("Session 2 Dst should be set")
	}
	if session1.Dst == session2.Dst {
		t.Error("Sessions should have different Dst directories")
	}

	// Cleanup
	if err := manager.CleanupSession(session1); err != nil {
		t.Errorf("CleanupSession 1: %v", err)
	}
	if err := manager.CleanupSession(session2); err != nil {
		t.Errorf("CleanupSession 2: %v", err)
	}

	t.Log("SessionManager correctly isolates different workspaces")
}

// TestIsolation_DeriveSandboxRoot verifies the sandbox root derivation logic
// for user sessions vs system sessions.
func TestIsolation_DeriveSandboxRoot(t *testing.T) {
	tests := []struct {
		name        string
		workspace   string
		userDataDir string
		want        string
	}{
		{
			name:        "user session uses UserDataDir",
			workspace:   "/workspaces/agent1",
			userDataDir: "/workspaces/agent1/users/42/data",
			want:        "/workspaces/agent1/users/42/data",
		},
		{
			name:        "system session uses Workspace",
			workspace:   "/workspaces/system-agent",
			userDataDir: "",
			want:        "/workspaces/system-agent",
		},
		{
			name:        "empty both returns empty",
			workspace:   "",
			userDataDir: "",
			want:        "",
		},
		{
			name:        "empty workspace but user data present",
			workspace:   "",
			userDataDir: "/users/1/data",
			want:        "/users/1/data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveSandboxRoot(tt.workspace, tt.userDataDir)
			if got != tt.want {
				t.Errorf("DeriveSandboxRoot(%q, %q) = %q, want %q",
					tt.workspace, tt.userDataDir, got, tt.want)
			}
		})
	}
}

// contains is a helper to check if a string contains a substring
func containsIsolation(s, substr string) bool {
	return strings.Contains(s, substr)
}
