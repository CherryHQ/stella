package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/config"
)

// TestResolveSessionRequiresUserRoot tests that resolveSession fails without a UserRoot.
func TestResolveSessionRequiresUserRoot(t *testing.T) {
	_, err := resolveSession(context.Background(), GoRunnerConfig{
		AgentRoot: "/workspace/agent",
		// UserRoot intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error when UserRoot is missing")
	}
}

func TestResolveRunnerPathsDefaultsWorkDirToUserRoot(t *testing.T) {
	cfg := GoRunnerConfig{
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/agent/users/1",
	}

	paths := resolveRunnerPaths(cfg)
	if got := resolveSandboxWorkingDir(cfg, cfg.UserRoot); got != cfg.UserRoot {
		t.Fatalf("resolveSandboxWorkingDir() = %q, want %q", got, cfg.UserRoot)
	}
	if paths.UserRoot != cfg.UserRoot {
		t.Fatalf("UserRoot = %q, want %q", paths.UserRoot, cfg.UserRoot)
	}
}

func TestSandboxProcessEnvLeavesHomeUnsetForDocker(t *testing.T) {
	cfg := GoRunnerConfig{
		AnnaHome:  "/anna",
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/agent/users/1",
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}
	env := sandboxProcessEnv(paths)
	if _, ok := env["HOME"]; ok {
		t.Fatalf("HOME should not be set for docker backend; got %q", env["HOME"])
	}
	if got := env["ANNA_HOME"]; got != cfg.AnnaHome {
		t.Fatalf("ANNA_HOME = %q, want %q", got, cfg.AnnaHome)
	}
}

// TestRunnerSessionLifecycle tests the runnerSession lifecycle using a nil session
// (alwaysAlive=true path) to avoid requiring a live sandbox backend.
func TestRunnerSessionLifecycle(t *testing.T) {
	rs := &runnerSession{
		alwaysAlive: true,
	}

	// Test Alive
	if !rs.Alive() {
		t.Error("session should be alive")
	}

	// Test Done channel — nil session returns an already-closed channel.
	select {
	case <-rs.Done():
		// Expected: nil session Done() is immediately closed.
	default:
		t.Error("nil-session Done() should be closed")
	}

	// Test Close (no-op for nil inner session)
	if err := rs.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestResolveSessionDockerUnreachableDaemonReturnsError verifies that resolveSession
// routes to createDockerSession and fails with a docker-related error when the daemon
// is unreachable.
func TestResolveSessionDockerUnreachableDaemonReturnsError(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/anna-test-docker.sock")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	workspace := t.TempDir()
	userRoot := workspace + "/users/1"
	_, err := resolveSession(context.Background(), GoRunnerConfig{
		AgentRoot: workspace,
		UserRoot:  userRoot,
		Sandbox:   config.SandboxConfig{},
	})
	if err == nil {
		t.Fatal("expected error for docker backend with unreachable daemon")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected error to mention 'docker', got: %v", err)
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
