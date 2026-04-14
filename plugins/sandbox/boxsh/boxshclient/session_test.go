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

	userRoot := t.TempDir()
	opts := SessionOptions{
		UserRoot: userRoot,
		WorkDir:  "src",
		Sandbox: NetworkConfig{
			Mode: NetworkDisabled,
		},
	}

	session, err := manager.CreateSession(opts)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.Src != userRoot {
		t.Errorf("Src = %q, want %q", session.Src, userRoot)
	}
	if !hasPrefix(session.Dst, manager.baseDir) {
		t.Errorf("Dst = %q, should be under %q", session.Dst, manager.baseDir)
	}
	if expectedCwd := filepath.Join(userRoot, "src"); session.Cwd != expectedCwd {
		t.Errorf("Cwd = %q, want %q", session.Cwd, expectedCwd)
	}
	if session.NetworkMode != NetworkDisabled {
		t.Errorf("NetworkMode = %q, want %q", session.NetworkMode, NetworkDisabled)
	}
	if err := manager.CleanupSession(session); err != nil {
		t.Errorf("CleanupSession: %v", err)
	}
}

func TestSessionManagerCreateSessionDefaultsWorkDirToUserRoot(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	userRoot := t.TempDir()
	session, err := manager.CreateSession(SessionOptions{UserRoot: userRoot})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.Src != userRoot {
		t.Errorf("Src = %q, want %q", session.Src, userRoot)
	}
	if session.Cwd != userRoot {
		t.Errorf("Cwd = %q, want %q", session.Cwd, userRoot)
	}
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
		UserRoot: "",
	}

	_, err = manager.CreateSession(opts)
	if err == nil {
		t.Error("CreateSession should require user root")
	}
}

func TestSessionManagerCreateSessionRequiresAbsoluteSrc(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	opts := SessionOptions{
		UserRoot: "relative/path",
	}

	_, err = manager.CreateSession(opts)
	if err == nil {
		t.Error("CreateSession should require absolute user root")
	}
}

func TestSessionManagerCreateSessionRequiresExistingSrc(t *testing.T) {
	manager, err := NewSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	opts := SessionOptions{
		UserRoot: "/nonexistent/path/that/does/not/exist",
	}

	_, err = manager.CreateSession(opts)
	if err == nil {
		t.Error("CreateSession should require existing user root")
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

func TestValidatePathWithinRoot(t *testing.T) {
	tests := []struct {
		name     string
		userRoot string
		path     string
		wantErr  bool
	}{
		{
			name:     "path under root",
			userRoot: "/workspace",
			path:     "/workspace/src/file.txt",
			wantErr:  false,
		},
		{
			name:     "path at root",
			userRoot: "/workspace",
			path:     "/workspace",
			wantErr:  false,
		},
		{
			name:     "path outside root",
			userRoot: "/workspace",
			path:     "/outside/file.txt",
			wantErr:  true,
		},
		{
			name:     "parent traversal",
			userRoot: "/workspace",
			path:     "/workspace/../outside",
			wantErr:  true,
		},
		{
			name:     "relative path resolved under root",
			userRoot: "/workspace",
			path:     "src/file.txt",
			wantErr:  false,
		},
		{
			name:     "empty user root",
			userRoot: "",
			path:     "/workspace/file.txt",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePathWithinRoot(tt.userRoot, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePathWithinRoot(%q, %q) error = %v, wantErr %v",
					tt.userRoot, tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidatePathWithinRootRequiresAbsoluteRoot(t *testing.T) {
	err := ValidatePathWithinRoot("relative/path", "/workspace/file.txt")
	if err == nil {
		t.Error("ValidatePathWithinRoot should require absolute user root")
	}
}

// Helper function.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
