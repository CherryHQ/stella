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
	client := New("/usr/local/bin/boxsh", SessionConfig{
		Src: "/workspace",
		Dst: "/tmp/session",
		Cwd: "/workspace",
	})

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

	if !contains(filepath.Base(sessionDir), "boxsh-session-") {
		t.Errorf("Session dir name %q doesn't contain 'boxsh-session-'", filepath.Base(sessionDir))
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

	_, err := CreateSessionDir(baseDir)
	if err != nil {
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
		{
			name:        "empty workdir uses sandbox root",
			sandboxRoot: "/workspace",
			workDir:     "",
			want:        "/workspace",
		},
		{
			name:        "relative workdir resolved against root",
			sandboxRoot: "/workspace",
			workDir:     "src/project",
			want:        "/workspace/src/project",
		},
		{
			name:        "absolute workdir under root used as-is",
			sandboxRoot: "/workspace",
			workDir:     "/workspace/src/project",
			want:        "/workspace/src/project",
		},
		{
			name:        "hidden child under root stays valid",
			sandboxRoot: "/workspace",
			workDir:     "/workspace/.hidden",
			want:        "/workspace/.hidden",
		},
		{
			name:        "workdir outside root defaults to root",
			sandboxRoot: "/workspace",
			workDir:     "/outside",
			want:        "/workspace",
		},
		{
			name:        "parent traversal blocked",
			sandboxRoot: "/workspace",
			workDir:     "../outside",
			want:        "/workspace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveSandboxCwd(tt.sandboxRoot, tt.workDir)
			if got != tt.want {
				t.Errorf("ResolveSandboxCwd(%q, %q) = %q, want %q",
					tt.sandboxRoot, tt.workDir, got, tt.want)
			}
		})
	}
}

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name string
		cfg  SessionConfig
		want []string
	}{
		{
			name: "basic config",
			cfg: SessionConfig{
				Src:         "/src",
				Dst:         "/dst",
				Cwd:         "/src",
				NetworkMode: "disabled",
			},
			want: []string{"--rpc", "--src", "/src", "--dst", "/dst", "--cwd", "/src", "--net=none"},
		},
		{
			name: "whitelist network mode",
			cfg: SessionConfig{
				Src:              "/src",
				Dst:              "/dst",
				Cwd:              "/src",
				NetworkMode:      "whitelist",
				NetworkAllowlist: []string{"example.com", "10.0.0.0/8"},
			},
			want: []string{"--rpc", "--src", "/src", "--dst", "/dst", "--cwd", "/src", "--net=whitelist", "--allow", "example.com", "--allow", "10.0.0.0/8"},
		},
		{
			name: "allow_all network mode",
			cfg: SessionConfig{
				Src:         "/src",
				Dst:         "/dst",
				Cwd:         "/src",
				NetworkMode: "allow_all",
			},
			want: []string{"--rpc", "--src", "/src", "--dst", "/dst", "--cwd", "/src", "--net=allow"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New("/bin/boxsh", tt.cfg)
			got := client.buildArgs()
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
			errCh <- fmt.Errorf("exec: %w", err)
			return
		}
		if result.Stdout != "echo first" {
			errCh <- fmt.Errorf("exec stdout = %q, want %q", result.Stdout, "echo first")
		}
	}()

	go func() {
		defer wg.Done()
		result, err := client.Stat(context.Background(), StatParams{Path: "/workspace/file.txt"})
		if err != nil {
			errCh <- fmt.Errorf("stat: %w", err)
			return
		}
		if !result.Exists || result.ModTime != "1970-01-01T00:00:00Z" {
			errCh <- fmt.Errorf("unexpected stat result: %+v", result)
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
	if [[ "$line" == *'"method":"ping"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		if [[ "$mode" == "bad-handshake" ]]; then
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"nope\",\"id\":$id}"
		else
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
		fi
		if [[ "$mode" == "exit-after-handshake" ]]; then
			sleep 0.05
			exit 0
		fi
	elif [[ "$line" == *'"method":"quit"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":\"bye\",\"id\":$id}"
		exit 0
	elif [[ "$line" == *'"method":"exec"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		cmd=$(echo "$line" | sed 's/.*"command":"\([^"]*\)".*/\1/')
		if [[ "$mode" == "out-of-order" ]]; then
			(sleep 0.05; echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"$cmd\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}") &
		else
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"$cmd\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
		fi
	elif [[ "$line" == *'"method":"stat"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"exists\":true,\"is_dir\":false,\"size\":0,\"mod_time\":\"1970-01-01T00:00:00Z\"},\"id\":$id}"
	fi
done
`, mode)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper wrapper: %v", err)
	}

	root := t.TempDir()
	return New(wrapper, SessionConfig{
		Src:         root,
		Dst:         t.TempDir(),
		Cwd:         root,
		NetworkMode: "disabled",
	})
}

// Helper functions.

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
