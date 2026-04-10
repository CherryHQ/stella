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

func skipIfWindowsIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Isolation tests require Linux/macOS boxsh")
	}
}

func writeIsolationMockBoxsh(t *testing.T, annaHome string) {
	t.Helper()
	_ = embedded.EnsureTools(annaHome)
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")

	script := `#!/bin/bash
set -euo pipefail

if [[ "${1:-}" == "--version" ]]; then
	echo "boxsh 2.0.1"
	exit 0
fi

SRC=""
while [[ $# -gt 0 ]]; do
	case "$1" in
		--src) SRC="$2"; shift 2 ;;
		--dst|--cwd|--allow) shift 2 ;;
		--rpc|--net=none|--net=allow|--net=whitelist) shift ;;
		*) shift ;;
	esac
done

json_escape() {
	printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'
}

is_allowed_path() {
	local requested="$1"
	if [[ -z "$requested" ]]; then
		return 1
	fi
	if [[ "$requested" == *".."* ]]; then
		return 1
	fi
	if [[ "$requested" = /* ]]; then
		[[ "$requested" == "$SRC" || "$requested" == "$SRC"/* ]]
		return $?
	fi
	return 0
}

while IFS= read -r line || [[ -n "$line" ]]; do
	method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	[[ -z "$method" ]] && continue

	case "$method" in
		ping)
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
			;;
		exec)
			command=$(echo "$line" | sed -n 's/.*"command":"\([^"]*\)".*/\1/p')
			if [[ "$command" =~ [[:space:]](/[^[:space:]]+) ]]; then
				candidate="${BASH_REMATCH[1]}"
				if ! is_allowed_path "$candidate"; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"\",\"stderr\":\"access denied: path outside workspace\",\"exit_code\":1},\"id\":$id}"
					continue
				fi
			fi
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"executed\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
			;;
		read|write|edit)
			path=$(echo "$line" | sed -n 's/.*"file_path":"\([^"]*\)".*/\1/p')
			if ! is_allowed_path "$path"; then
				echo "{\"jsonrpc\":\"2.0\",\"error\":{\"code\":-32000,\"message\":\"access denied: path outside workspace\"},\"id\":$id}"
				continue
			fi
			if [[ "$method" == "read" ]]; then
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":\"file content\",\"total_lines\":1,\"truncated\":false},\"id\":$id}"
			elif [[ "$method" == "write" ]]; then
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"bytes_written\":12,\"path\":$(json_escape "$path")},\"id\":$id}"
			else
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"path\":$(json_escape "$path"),\"replacements\":1},\"id\":$id}"
			fi
			;;
		quit|close)
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"bye\",\"id\":$id}"
			exit 0
			;;
	esac
done
`
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestIsolation_CrossWorkspaceAccessBlocked(t *testing.T) {
	skipIfWindowsIsolation(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	allowedRoot := filepath.Join(workspace, "users", "1", "data")
	siblingRoot := filepath.Join(workspace, "users", "2", "data")
	if err := os.MkdirAll(allowedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(siblingRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeIsolationMockBoxsh(t, annaHome)

	cfg := BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   workspace,
		UserDataDir: allowedRoot,
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

	if _, err := readTool.Execute(ctx, map[string]any{"file_path": filepath.Join(siblingRoot, "secret.txt")}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected sibling workspace denial, got %v", err)
	}
	if _, err := writeTool.Execute(ctx, map[string]any{"file_path": filepath.Join(siblingRoot, "hack.txt"), "content": "nope"}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected sibling workspace write denial, got %v", err)
	}
	if _, err := editTool.Execute(ctx, map[string]any{"file_path": filepath.Join(siblingRoot, "secret.txt"), "old_string": "a", "new_string": "b"}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected sibling workspace edit denial, got %v", err)
	}
	if _, err := bashTool.Execute(ctx, map[string]any{"command": "cat " + filepath.Join(siblingRoot, "secret.txt")}); err == nil {
		t.Fatal("expected bash denial for sibling workspace access")
	}

	if _, err := readTool.Execute(ctx, map[string]any{"file_path": filepath.Join(allowedRoot, "ok.txt")}); err != nil {
		t.Fatalf("expected in-root access to succeed, got %v", err)
	}
}

func TestIsolation_ParentDirectoryTraversalBlocked(t *testing.T) {
	skipIfWindowsIsolation(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	allowedRoot := filepath.Join(workspace, "users", "1", "data")
	if err := os.MkdirAll(allowedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeIsolationMockBoxsh(t, annaHome)

	cfg := BackendConfig{
		AnnaHome:    annaHome,
		Workspace:   workspace,
		UserDataDir: allowedRoot,
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
	if _, err := readTool.Execute(ctx, map[string]any{"file_path": "../other-agent/secrets.txt"}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected parent traversal denial, got %v", err)
	}
	if _, err := readTool.Execute(ctx, map[string]any{"file_path": filepath.Join(allowedRoot, "..", "..", "secret.txt")}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected absolute traversal denial, got %v", err)
	}
}

func TestIsolation_DifferentAgentsDifferentSessions(t *testing.T) {
	skipIfWindowsIsolation(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
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
	writeIsolationMockBoxsh(t, annaHome)

	cfg1 := BackendConfig{AnnaHome: annaHome, Workspace: workspace1, UserDataDir: userDataDir1, WorkDir: "/", Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}}}
	cfg2 := BackendConfig{AnnaHome: annaHome, Workspace: workspace2, UserDataDir: userDataDir2, WorkDir: "/", Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}}}

	backend1, err := NewSharedBackend(cfg1)
	if err != nil {
		t.Fatalf("NewSharedBackend 1: %v", err)
	}
	defer func() { _ = backend1.Close() }()
	backend2, err := NewSharedBackend(cfg2)
	if err != nil {
		t.Fatalf("NewSharedBackend 2: %v", err)
	}
	defer func() { _ = backend2.Close() }()

	if err := backend1.Start(ctx, cfg1); err != nil {
		t.Fatalf("backend1.Start: %v", err)
	}
	if err := backend2.Start(ctx, cfg2); err != nil {
		t.Fatalf("backend2.Start: %v", err)
	}

	if backend1.SessionDir() == "" || backend2.SessionDir() == "" {
		t.Fatal("both backends should have session dirs")
	}
	if backend1.SessionDir() == backend2.SessionDir() {
		t.Fatal("different agents should have different upperdirs")
	}
}

func TestIsolation_ValidateSandboxPath(t *testing.T) {
	tests := []struct {
		name        string
		sandboxRoot string
		path        string
		wantErr     bool
		errContain  string
	}{
		{name: "path inside sandbox", sandboxRoot: "/workspace", path: "/workspace/file.txt"},
		{name: "relative path resolved inside sandbox", sandboxRoot: "/workspace", path: "file.txt"},
		{name: "path outside sandbox", sandboxRoot: "/workspace", path: "/etc/passwd", wantErr: true, errContain: "outside sandbox"},
		{name: "parent traversal blocked", sandboxRoot: "/workspace", path: "../etc/passwd", wantErr: true, errContain: "outside sandbox"},
		{name: "empty sandbox root rejected", sandboxRoot: "", path: "/workspace/file.txt", wantErr: true, errContain: "required"},
		{name: "relative sandbox root rejected", sandboxRoot: "workspace", path: "/workspace/file.txt", wantErr: true, errContain: "must be absolute"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSandboxPath(tt.sandboxRoot, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errContain != "" && !strings.Contains(err.Error(), tt.errContain) {
					t.Fatalf("error %v should contain %q", err, tt.errContain)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
