package boxshclient

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

	// Verify directory was created.
	info, err := os.Stat(sessionDir)
	if err != nil {
		t.Fatalf("Stat session dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("Session path is not a directory")
	}

	// Verify pattern in name.
	if !contains(filepath.Base(sessionDir), "boxsh-session-") {
		t.Errorf("Session dir name %q doesn't contain 'boxsh-session-'", filepath.Base(sessionDir))
	}

	// Cleanup.
	if err := CleanupSessionDir(sessionDir); err != nil {
		t.Fatalf("CleanupSessionDir: %v", err)
	}

	// Verify cleanup.
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
