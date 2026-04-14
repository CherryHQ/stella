package boxshclient

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipIfWindowsIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Isolation tests require Linux/macOS boxsh")
	}
}

func writeIsolationMockBoxsh(t *testing.T, annaHome string) {
	t.Helper()
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
DST=""
while [[ $# -gt 0 ]]; do
	case "$1" in
		--bind)
			binding="$2"
			if [[ "$binding" == cow:* ]]; then
				payload="${binding#cow:}"
				SRC="${payload%%:*}"
				DST="${payload#*:}"
			fi
			shift 2 ;;
		--sandbox|--rpc|--new-net-ns) shift ;;
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
		[[ "$requested" == "$DST" || "$requested" == "$DST"/* ]]
		return $?
	fi
	return 0
}

while IFS= read -r line || [[ -n "$line" ]]; do
	method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	[[ -z "$method" ]] && continue

	case "$method" in
		initialize)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"serverInfo\":{\"name\":\"boxsh\",\"version\":\"2.0.1\"},\"protocolVersion\":\"2024-11-05\"},\"id\":$id}"
			;;
		tools/call)
			if [[ "$line" == *'"name":"bash"'* ]]; then
				command=$(echo "$line" | sed -n 's/.*"command":"\([^"]*\)".*/\1/p')
				if [[ "$command" =~ [[:space:]](/[^[:space:]]+) ]]; then
					candidate="${BASH_REMATCH[1]}"
					if ! is_allowed_path "$candidate"; then
						echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"access denied: path outside workspace\"}],\"structuredContent\":{\"stdout\":\"\",\"stderr\":\"access denied: path outside workspace\",\"exit_code\":1},\"isError\":true},\"id\":$id}"
						continue
					fi
				fi
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"executed\"}],\"structuredContent\":{\"stdout\":\"executed\",\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
			else
				path=$(echo "$line" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
				if ! is_allowed_path "$path"; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"access denied: path outside workspace\"}],\"isError\":true},\"id\":$id}"
					continue
				fi
				if [[ "$line" == *'"name":"read"'* ]]; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"file content\"}],\"structuredContent\":{\"truncation\":{\"line_count\":1,\"truncated\":false}}},\"id\":$id}"
				elif [[ "$line" == *'"name":"write"'* ]]; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"written 12 bytes\"}]},\"id\":$id}"
				else
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"OK\"}],\"structuredContent\":{\"diff\":\"diff\",\"firstChangedLine\":1}},\"id\":$id}"
				fi
			fi
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
		SandboxRoot: allowedRoot,
		WorkDir:     "/",
		Sandbox:     NetworkConfig{Mode: NetworkDisabled},
	}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	if _, err := testRead(ctx, backend, filepath.Join(siblingRoot, "secret.txt"), 1, 0); err == nil || (!strings.Contains(err.Error(), "access denied") && !strings.Contains(err.Error(), "outside sandbox")) {
		t.Fatalf("expected sibling workspace denial, got %v", err)
	}
	if _, err := testWrite(ctx, backend, filepath.Join(siblingRoot, "hack.txt"), "nope"); err == nil || (!strings.Contains(err.Error(), "access denied") && !strings.Contains(err.Error(), "outside sandbox")) {
		t.Fatalf("expected sibling workspace write denial, got %v", err)
	}
	if _, err := testEdit(ctx, backend, filepath.Join(siblingRoot, "secret.txt"), "a", "b"); err == nil || (!strings.Contains(err.Error(), "access denied") && !strings.Contains(err.Error(), "outside sandbox")) {
		t.Fatalf("expected sibling workspace edit denial, got %v", err)
	}
	result, err := testExec(ctx, backend, "cat "+filepath.Join(siblingRoot, "secret.txt"), 0)
	if err != nil {
		t.Fatalf("exec sibling cat: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected bash denial for sibling workspace access, got %#v", result)
	}

	if _, err := testRead(ctx, backend, filepath.Join(allowedRoot, "ok.txt"), 1, 0); err != nil {
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
		SandboxRoot: allowedRoot,
		WorkDir:     "/",
		Sandbox:     NetworkConfig{Mode: NetworkDisabled},
	}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	if _, err := testRead(ctx, backend, "../other-agent/secrets.txt", 1, 0); err == nil || (!strings.Contains(err.Error(), "access denied") && !strings.Contains(err.Error(), "outside sandbox")) {
		t.Fatalf("expected parent traversal denial, got %v", err)
	}
	if _, err := testRead(ctx, backend, filepath.Join(allowedRoot, "..", "..", "secret.txt"), 1, 0); err == nil || (!strings.Contains(err.Error(), "access denied") && !strings.Contains(err.Error(), "outside sandbox")) {
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

	cfg1 := BackendConfig{AnnaHome: annaHome, SandboxRoot: userDataDir1, WorkDir: "/", Sandbox: NetworkConfig{Mode: NetworkDisabled}}
	cfg2 := BackendConfig{AnnaHome: annaHome, SandboxRoot: userDataDir2, WorkDir: "/", Sandbox: NetworkConfig{Mode: NetworkDisabled}}

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
