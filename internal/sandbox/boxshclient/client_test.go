package boxshclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	client := New("/usr/local/bin/boxsh", SessionConfig{Src: "/workspace", Dst: "/tmp/session", Cwd: "/tmp/session"})
	if client.binaryPath != "/usr/local/bin/boxsh" {
		t.Errorf("binaryPath = %q, want %q", client.binaryPath, "/usr/local/bin/boxsh")
	}
	if client.sessionConfig.Src != "/workspace" {
		t.Errorf("Src = %q, want %q", client.sessionConfig.Src, "/workspace")
	}
}

func TestPlatformSupportsBoxsh(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin":
		if !PlatformSupportsBoxsh() {
			t.Error("PlatformSupportsBoxsh() should return true on Linux/Darwin")
		}
	default:
		if PlatformSupportsBoxsh() {
			t.Error("PlatformSupportsBoxsh() should return false on non-Linux/Darwin")
		}
	}
}

func TestCreateAndCleanupSessionDir(t *testing.T) {
	baseDir := t.TempDir()
	sessionDir, err := CreateSessionDir(baseDir)
	if err != nil {
		t.Fatalf("CreateSessionDir: %v", err)
	}
	info, err := os.Stat(sessionDir)
	if err != nil {
		t.Fatalf("Stat session dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("Session path is not a directory")
	}
	if err := CleanupSessionDir(sessionDir); err != nil {
		t.Fatalf("CleanupSessionDir: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Error("Session dir should have been removed")
	}
}

func TestCreateSessionDirCreatesBaseDir(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "nested", "sessions")
	if _, err := CreateSessionDir(baseDir); err != nil {
		t.Fatalf("CreateSessionDir with nested base: %v", err)
	}
	if _, err := os.Stat(baseDir); err != nil {
		t.Errorf("CreateSessionDir should create base directory: %v", err)
	}
}

func TestResolveSandboxCwd(t *testing.T) {
	tests := []struct {
		name        string
		sandboxRoot string
		workDir     string
		want        string
	}{
		{"empty workdir uses sandbox root", "/workspace", "", "/workspace"},
		{"relative workdir resolved against root", "/workspace", "src/project", "/workspace/src/project"},
		{"absolute workdir under root used as-is", "/workspace", "/workspace/src/project", "/workspace/src/project"},
		{"hidden child under root stays valid", "/workspace", "/workspace/.hidden", "/workspace/.hidden"},
		{"workdir outside root defaults to root", "/workspace", "/outside", "/workspace"},
		{"parent traversal blocked", "/workspace", "../outside", "/workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveSandboxCwd(tt.sandboxRoot, tt.workDir)
			if got != tt.want {
				t.Errorf("ResolveSandboxCwd(%q, %q) = %q, want %q", tt.sandboxRoot, tt.workDir, got, tt.want)
			}
		})
	}
}

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SessionConfig
		want    []string
		wantErr string
	}{
		{
			name: "disabled network",
			cfg:  SessionConfig{Src: "/src", Dst: "/dst", Cwd: "/dst", NetworkMode: "disabled"},
			want: []string{"--rpc", "--sandbox", "--bind", "cow:/src:/dst", "--new-net-ns"},
		},
		{
			name: "allow_all network",
			cfg:  SessionConfig{Src: "/src", Dst: "/dst", Cwd: "/dst", NetworkMode: "allow_all"},
			want: []string{"--rpc", "--sandbox", "--bind", "cow:/src:/dst"},
		},
		{
			name:    "whitelist unsupported",
			cfg:     SessionConfig{Src: "/src", Dst: "/dst", Cwd: "/dst", NetworkMode: "whitelist", NetworkAllowlist: []string{"example.com"}},
			wantErr: "whitelist network mode is not supported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New("/bin/boxsh", tt.cfg)
			got, err := client.buildArgs()
			if tt.wantErr != "" {
				if err == nil || !contains(err.Error(), tt.wantErr) {
					t.Fatalf("buildArgs error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if !slicesEqual(got, tt.want) {
				t.Errorf("buildArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClientAliveWhenNotStarted(t *testing.T) {
	client := New("/bin/boxsh", SessionConfig{})
	if client.Alive() {
		t.Error("Client should not be alive before start")
	}
}

func TestCleanupSessionDirWithEmptyPath(t *testing.T) {
	if err := CleanupSessionDir(""); err != nil {
		t.Errorf("CleanupSessionDir with empty path should not error: %v", err)
	}
}

func TestClientStartAndClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper wrapper uses a POSIX shell")
	}
	client := newHelperClient(t, "normal")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !client.Alive() {
		t.Fatal("client should be alive after successful start")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client.Alive() {
		t.Fatal("client should not be alive after Close")
	}
}

func TestClientStartHandshakeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper wrapper uses a POSIX shell")
	}
	client := newHelperClient(t, "bad-handshake")
	if err := client.Start(context.Background()); err == nil {
		t.Fatal("expected handshake failure")
	}
}

func TestClientAliveDetectsExitedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper wrapper uses a POSIX shell")
	}
	client := newHelperClient(t, "exit-after-handshake")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = client.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for client.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if client.Alive() {
		t.Fatal("client should report dead after helper exits")
	}
}

func TestClientRoutesOutOfOrderResponses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper wrapper uses a POSIX shell")
	}
	client := newHelperClient(t, "out-of-order")
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = client.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)

	go func() {
		defer wg.Done()
		result, err := client.Exec(context.Background(), ExecParams{Command: "echo first"})
		if err != nil {
			errCh <- fmt.Errorf("exec1: %w", err)
			return
		}
		if result.Stdout != "echo first" {
			errCh <- fmt.Errorf("exec1 stdout = %q, want %q", result.Stdout, "echo first")
		}
	}()

	go func() {
		defer wg.Done()
		result, err := client.Exec(context.Background(), ExecParams{Command: "echo second"})
		if err != nil {
			errCh <- fmt.Errorf("exec2: %w", err)
			return
		}
		if result.Stdout != "echo second" {
			errCh <- fmt.Errorf("exec2 stdout = %q, want %q", result.Stdout, "echo second")
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newHelperClient(t *testing.T, mode string) *Client {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "boxsh-helper.sh")
	script := fmt.Sprintf(`#!/bin/bash
mode=%q

if [[ "$1" == "--version" ]]; then
	echo boxsh 2.0.1
	exit 0
fi

while read -r line; do
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	if [[ "$line" == *'"method":"initialize"'* ]]; then
		if [[ "$mode" == "bad-handshake" ]]; then
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"serverInfo\":{\"name\":\"not-boxsh\",\"version\":\"0\"},\"protocolVersion\":\"2024-11-05\"},\"id\":$id}"
		else
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"serverInfo\":{\"name\":\"boxsh\",\"version\":\"2.0.1\"},\"protocolVersion\":\"2024-11-05\"},\"id\":$id}"
		fi
		if [[ "$mode" == "exit-after-handshake" ]]; then
			sleep 0.05
			exit 0
		fi
	elif [[ "$line" == *'"method":"tools/call"'* ]]; then
		cmd=$(echo "$line" | sed 's/.*"command":"\([^"]*\)".*/\1/')
		if [[ "$mode" == "out-of-order" && "$cmd" == "echo first" ]]; then
			(sleep 0.05; echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"$cmd\"}],\"structuredContent\":{\"stdout\":\"$cmd\",\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}") &
		else
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"$cmd\"}],\"structuredContent\":{\"stdout\":\"$cmd\",\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
		fi
	fi
done
`, mode)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper wrapper: %v", err)
	}
	root := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	return New(wrapper, SessionConfig{Src: root, Dst: dst, Cwd: dst, NetworkMode: "disabled"})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
