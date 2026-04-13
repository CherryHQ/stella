package runner

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/sandbox"
)

// TestResolveSessionRejectsUnknownBackend tests error handling.
func TestResolveSessionRejectsUnknownBackend(t *testing.T) {
	_, err := resolveSession(context.Background(), GoRunnerConfig{
		Sandbox: config.SandboxConfig{
			Backend: "unknown",
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestResolveSessionAutoFallsBackToLocalWhenBoxshUnsupported(t *testing.T) {
	previous := platformSupportsBoxsh
	platformSupportsBoxsh = func() bool { return false }
	t.Cleanup(func() { platformSupportsBoxsh = previous })

	rs, err := resolveSession(context.Background(), GoRunnerConfig{
		Workspace: t.TempDir(),
		WorkDir:   ".",
		Sandbox: config.SandboxConfig{
			Backend: config.SandboxBackendAuto,
		},
	})
	if err != nil {
		t.Fatalf("resolveSession: %v", err)
	}
	defer func() { _ = rs.Close() }()

	if got := rs.Policy().Backend; got != config.SandboxBackendLocal {
		t.Fatalf("Policy().Backend = %q, want %q", got, config.SandboxBackendLocal)
	}
}

func TestResolveSessionExplicitBoxshRejectsUnsupportedPlatform(t *testing.T) {
	previous := platformSupportsBoxsh
	platformSupportsBoxsh = func() bool { return false }
	t.Cleanup(func() { platformSupportsBoxsh = previous })

	_, err := resolveSession(context.Background(), GoRunnerConfig{
		Sandbox: config.SandboxConfig{
			Backend: config.SandboxBackendBoxsh,
		},
	})
	if err == nil {
		t.Fatal("expected error for explicit boxsh on unsupported platform")
	}
}

func TestSandboxWorkspaceRootUsesUserDataDir(t *testing.T) {
	cfg := GoRunnerConfig{
		Workspace:   "/workspace/agent",
		UserDataDir: "/workspace/agent/users/1/data",
	}

	if got := sandboxWorkspaceRoot(cfg); got != cfg.UserDataDir {
		t.Fatalf("sandboxWorkspaceRoot() = %q, want %q", got, cfg.UserDataDir)
	}
}

func TestSandboxWorkspaceRootFallsBackToWorkspace(t *testing.T) {
	cfg := GoRunnerConfig{
		Workspace: "/workspace/agent",
	}

	if got := sandboxWorkspaceRoot(cfg); got != cfg.Workspace {
		t.Fatalf("sandboxWorkspaceRoot() = %q, want %q", got, cfg.Workspace)
	}
}

// TestRunnerSessionLifecycle tests the runnerSession lifecycle.
func TestRunnerSessionLifecycle(t *testing.T) {
	// Create a local session for testing
	policy := sandbox.Policy{
		Backend: "local",
		Relaxed: true,
		Filesystem: sandbox.FilesystemPolicy{
			WorkingDir:   t.TempDir(),
			AllowEscapes: true,
		},
	}

	factory := sandbox.GlobalRegistry().Get("local")
	if factory == nil {
		t.Fatal("local factory not available")
	}

	session, err := factory.CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rs := &runnerSession{
		session: session,
		policy:  policy,
	}

	// Test Alive
	if !rs.Alive() {
		t.Error("session should be alive")
	}

	// Test Done channel
	select {
	case <-rs.Done():
		t.Error("Done() should not be closed before Close()")
	default:
		// Expected
	}

	// Test Close
	if err := rs.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// After close, should not be alive
	if rs.Alive() {
		t.Error("session should not be alive after Close()")
	}

	// Done channel should be closed
	select {
	case <-rs.Done():
		// Expected
	default:
		t.Error("Done() should be closed after Close()")
	}
}

// TestRunnerSessionNilHandling tests nil session safety.
func TestRunnerSessionNilHandling(t *testing.T) {
	var rs *runnerSession

	// Should handle nil gracefully
	if rs.Alive() {
		t.Error("nil session should not be alive")
	}

	// Done should return closed channel for nil
	done := rs.Done()
	select {
	case <-done:
		// Expected
	default:
		t.Error("nil session Done() should be closed")
	}

	// Close should not panic
	if err := rs.Close(); err != nil {
		t.Errorf("Close on nil: %v", err)
	}
}
