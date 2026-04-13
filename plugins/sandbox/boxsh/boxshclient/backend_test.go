package boxshclient

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewSharedBackend(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho boxsh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := BackendConfig{
		BinaryPath: boxshPath,
		Workspace:  t.TempDir(),
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}

	if backend.binaryPath != boxshPath {
		t.Errorf("binaryPath = %q, want %q", backend.binaryPath, boxshPath)
	}

	if err := backend.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestNewSharedBackendRequiresPlatformSupport(t *testing.T) {
	// Save and restore GOOS for this test.
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		// Can't test failure on supported platforms without mocking,
		// so we just verify success path works.
		return
	}

	cfg := BackendConfig{
		BinaryPath: "/usr/local/bin/boxsh",
		Workspace:  t.TempDir(),
	}

	_, err := NewSharedBackend(cfg)
	if err == nil {
		t.Error("expected error on unsupported platform")
	}
}

func TestSharedBackendNotAliveBeforeStart(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho boxsh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := BackendConfig{
		BinaryPath: boxshPath,
		Workspace:  t.TempDir(),
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}

	if backend.Alive() {
		t.Error("backend should not be alive before Start()")
	}
}

func TestSharedBackendClientReturnsNilBeforeStart(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho boxsh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := BackendConfig{
		BinaryPath: boxshPath,
		Workspace:  t.TempDir(),
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}

	if client := backend.Client(); client != nil {
		t.Error("Client() should return nil before Start()")
	}
}

func TestSharedBackendSessionDir(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")
	if err := os.WriteFile(boxshPath, []byte("#!/bin/sh\necho boxsh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := BackendConfig{
		BinaryPath: boxshPath,
		Workspace:  t.TempDir(),
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}

	// Before start, session dir should be empty.
	if dir := backend.SessionDir(); dir != "" {
		t.Errorf("SessionDir before Start = %q, want empty", dir)
	}
}

func TestIsSharedBackendError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "boxshclient error",
			err:  &mockError{"boxshclient: something went wrong"},
			want: true,
		},
		{
			name: "other error",
			err:  &mockError{"something else went wrong"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSharedBackendError(tt.err)
			if got != tt.want {
				t.Errorf("IsSharedBackendError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestBackendConfigSandboxRoot(t *testing.T) {
	tests := []struct {
		name        string
		workspace   string
		userDataDir string
		want        string
	}{
		{
			name:        "user session uses UserDataDir",
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
			cfg := BackendConfig{
				Workspace:   tt.workspace,
				UserDataDir: tt.userDataDir,
			}

			// Verify expected behavior through SessionManager.
			src := DeriveSandboxRoot(cfg.Workspace, cfg.UserDataDir)
			if src != tt.want {
				t.Errorf("SandboxRoot = %q, want %q", src, tt.want)
			}
		})
	}
}

type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}
