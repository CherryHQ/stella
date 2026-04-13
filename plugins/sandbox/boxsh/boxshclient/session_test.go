package boxshclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewSessionManager(t *testing.T) {
	baseDir := t.TempDir()
	manager, err := NewSessionManager(baseDir)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	if manager.baseDir != baseDir {
		t.Errorf("baseDir = %q, want %q", manager.baseDir, baseDir)
	}
}

func TestNewSessionManagerCreatesBaseDir(t *testing.T) {
	parentDir := t.TempDir()
	baseDir := filepath.Join(parentDir, "nested", "sessions")

	_, err := NewSessionManager(baseDir)
	if err != nil {
		t.Fatalf("NewSessionManager with nested path: %v", err)
	}

	if _, err := os.Stat(baseDir); err != nil {
		t.Errorf("baseDir should have been created: %v", err)
	}
}

func TestNewSessionManagerDefaultBaseDir(t *testing.T) {
	// Test with empty baseDir - should use default.
	manager, err := NewSessionManager("")
	if err != nil {
		t.Fatalf("NewSessionManager with empty baseDir: %v", err)
	}

	expectedBase := filepath.Join(DefaultAnnaHome(), "cache", "sandbox", "sessions")
	if manager.baseDir != expectedBase {
		t.Errorf("baseDir = %q, want %q", manager.baseDir, expectedBase)
	}
}

func TestSessionManagerCreateSession(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	workspace := t.TempDir()
	userDataDir := t.TempDir()

	opts := SessionOptions{
		Workspace:   workspace,
		UserDataDir: userDataDir,
		WorkDir:     "src",
		Sandbox: NetworkConfig{
			Mode: NetworkDisabled,
		},
	}

	session, err := manager.CreateSession(opts)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// User session should use UserDataDir as Src.
	if session.Src != userDataDir {
		t.Errorf("Src = %q, want %q", session.Src, userDataDir)
	}

	// Dst should be created under manager baseDir.
	if !hasPrefix(session.Dst, manager.baseDir) {
		t.Errorf("Dst = %q, should be under %q", session.Dst, manager.baseDir)
	}

	// Cwd should be resolved.
	expectedCwd := filepath.Join(userDataDir, "src")
	if session.Cwd != expectedCwd {
		t.Errorf("Cwd = %q, want %q", session.Cwd, expectedCwd)
	}

	if session.NetworkMode != NetworkDisabled {
		t.Errorf("NetworkMode = %q, want %q", session.NetworkMode, NetworkDisabled)
	}

	if !session.IsUserSession {
		t.Error("IsUserSession should be true")
	}

	// Cleanup.
	if err := manager.CleanupSession(session); err != nil {
		t.Errorf("CleanupSession: %v", err)
	}
}

func TestSessionManagerCreateSystemSession(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	workspace := t.TempDir()

	opts := SessionOptions{
		Workspace:   workspace,
		UserDataDir: "", // No user data dir - system session
		WorkDir:     "",
		Sandbox:     NetworkConfig{},
	}

	session, err := manager.CreateSession(opts)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// System session should use Workspace as Src.
	if session.Src != workspace {
		t.Errorf("Src = %q, want %q", session.Src, workspace)
	}

	// Cwd should default to Src.
	if session.Cwd != workspace {
		t.Errorf("Cwd = %q, want %q", session.Cwd, workspace)
	}

	if session.IsUserSession {
		t.Error("IsUserSession should be false for system session")
	}

	// Cleanup.
	if err := manager.CleanupSession(session); err != nil {
		t.Errorf("CleanupSession: %v", err)
	}
}

func TestSessionManagerCreateSessionRequiresWorkspace(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	opts := SessionOptions{
		Workspace:   "",
		UserDataDir: "",
	}

	_, err = manager.CreateSession(opts)
	if err == nil {
		t.Error("CreateSession should require workspace or user_data_dir")
	}
}

func TestSessionManagerCreateSessionRequiresAbsoluteSrc(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	opts := SessionOptions{
		Workspace:   "relative/path",
		UserDataDir: "",
	}

	_, err = manager.CreateSession(opts)
	if err == nil {
		t.Error("CreateSession should require absolute workspace path")
	}
}

func TestSessionManagerCreateSessionRequiresExistingSrc(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	opts := SessionOptions{
		Workspace:   "/nonexistent/path/that/does/not/exist",
		UserDataDir: "",
	}

	_, err = manager.CreateSession(opts)
	if err == nil {
		t.Error("CreateSession should require existing workspace")
	}
}

func TestSessionManagerCleanupSessionWithNil(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	if err := manager.CleanupSession(nil); err != nil {
		t.Errorf("CleanupSession(nil) should not error: %v", err)
	}
}

func TestSessionManagerCleanupSessionWithEmptyDst(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	session := &SessionInfo{
		Src: t.TempDir(),
		Dst: "", // empty Dst
	}

	if err := manager.CleanupSession(session); err != nil {
		t.Errorf("CleanupSession with empty Dst should not error: %v", err)
	}
}

func TestBuildSessionConfig(t *testing.T) {
	info := &SessionInfo{
		Src:              "/src",
		Dst:              "/dst",
		Cwd:              "/cwd",
		NetworkMode:      NetworkWhitelist,
		NetworkAllowlist: []string{"example.com"},
	}

	cfg := BuildSessionConfig(info)

	if cfg.Src != info.Src {
		t.Errorf("Src = %q, want %q", cfg.Src, info.Src)
	}
	if cfg.Dst != info.Dst {
		t.Errorf("Dst = %q, want %q", cfg.Dst, info.Dst)
	}
	if cfg.Cwd != info.Cwd {
		t.Errorf("Cwd = %q, want %q", cfg.Cwd, info.Cwd)
	}
	if cfg.NetworkMode != info.NetworkMode {
		t.Errorf("NetworkMode = %q, want %q", cfg.NetworkMode, info.NetworkMode)
	}
	if len(cfg.NetworkAllowlist) != 1 || cfg.NetworkAllowlist[0] != "example.com" {
		t.Errorf("NetworkAllowlist = %v, want [example.com]", cfg.NetworkAllowlist)
	}
}

func TestDeriveSandboxRoot(t *testing.T) {
	tests := []struct {
		name        string
		workspace   string
		userDataDir string
		want        string
	}{
		{
			name:        "user session prioritizes UserDataDir",
			workspace:   "/workspace",
			userDataDir: "/users/1/data",
			want:        "/users/1/data",
		},
		{
			name:        "system session uses Workspace",
			workspace:   "/workspace",
			userDataDir: "",
			want:        "/workspace",
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

func TestValidateSandboxPath(t *testing.T) {
	tests := []struct {
		name        string
		sandboxRoot string
		path        string
		wantErr     bool
	}{
		{
			name:        "path under root",
			sandboxRoot: "/workspace",
			path:        "/workspace/src/file.txt",
			wantErr:     false,
		},
		{
			name:        "path at root",
			sandboxRoot: "/workspace",
			path:        "/workspace",
			wantErr:     false,
		},
		{
			name:        "path outside root",
			sandboxRoot: "/workspace",
			path:        "/outside/file.txt",
			wantErr:     true,
		},
		{
			name:        "parent traversal",
			sandboxRoot: "/workspace",
			path:        "/workspace/../outside",
			wantErr:     true,
		},
		{
			name:        "relative path resolved under root",
			sandboxRoot: "/workspace",
			path:        "src/file.txt",
			wantErr:     false,
		},
		{
			name:        "empty sandbox root",
			sandboxRoot: "",
			path:        "/workspace/file.txt",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSandboxPath(tt.sandboxRoot, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSandboxPath(%q, %q) error = %v, wantErr %v",
					tt.sandboxRoot, tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSandboxPathRequiresAbsoluteRoot(t *testing.T) {
	err := ValidateSandboxPath("relative/path", "/workspace/file.txt")
	if err == nil {
		t.Error("ValidateSandboxPath should require absolute sandbox root")
	}
}

// Helper function.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
