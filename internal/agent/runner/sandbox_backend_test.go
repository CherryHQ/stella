package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/config"
)

// stubVaultLoader is a test-only VaultEnvLoader that returns a fixed map.
type stubVaultLoader struct {
	env map[string]string
	err error
}

func (s *stubVaultLoader) LoadEnv(_ context.Context, _ int64) (map[string]string, error) {
	return s.env, s.err
}

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

// TestBuildSandboxEnv_vaultSecretsInjected verifies that vault secrets appear
// in the sandbox env and that runner-set vars (ANNA_HOME) take precedence over
// any same-named vault entry.
func TestBuildSandboxEnv_vaultSecretsInjected(t *testing.T) {
	cfg := GoRunnerConfig{
		AnnaHome:  "/anna",
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/users/1",
		UserID:    42,
		VaultEnvLoader: &stubVaultLoader{
			env: map[string]string{
				"MY_SECRET": "s3cr3t",
				"ANNA_HOME": "should-be-overridden", // runner var must win
			},
		},
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}

	env := buildSandboxEnv(context.Background(), cfg, paths)

	// Vault secret must be present.
	if got := env["MY_SECRET"]; got != "s3cr3t" {
		t.Errorf("MY_SECRET = %q, want %q", got, "s3cr3t")
	}

	// Runner var (ANNA_HOME) must override any same-named vault entry.
	if got := env["ANNA_HOME"]; got != cfg.AnnaHome {
		t.Errorf("ANNA_HOME = %q, want %q (runner var must take precedence)", got, cfg.AnnaHome)
	}
}

// TestBuildSandboxEnv_noVaultLoader verifies that buildSandboxEnv behaves
// correctly (returns runner env vars) when no vault loader is configured.
func TestBuildSandboxEnv_noVaultLoader(t *testing.T) {
	cfg := GoRunnerConfig{
		AnnaHome:  "/anna",
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/users/1",
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}

	env := buildSandboxEnv(context.Background(), cfg, paths)

	if got := env["ANNA_HOME"]; got != cfg.AnnaHome {
		t.Errorf("ANNA_HOME = %q, want %q", got, cfg.AnnaHome)
	}
}
