package sandbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
	dockerclient "github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

// dockerAvailable probes whether the docker daemon is reachable.
// It constructs a client (requires binary on PATH) and calls Version with a short timeout.
func dockerAvailable(ctx context.Context) bool {
	client, err := dockerclient.New()
	if err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = client.Version(probeCtx)
	return err == nil
}

// dockerContractImage is the image the docker contract tests use. Alpine is
// sufficient because the contract tests exercise the generic Session/Host
// interface, not anna-sandbox-specific features, and alpine always pulls from
// the public registry.
const dockerContractImage = "alpine:3.20"

// dockerPreflightForTest pulls the contract-test image when it is not already
// present locally. CI hosts have a docker daemon but no pre-pulled image, so
// the contract tests (which call CreateSession directly, bypassing the public
// Preflight entry point) would otherwise fail with "No such image".
//
// A pull failure (no network, rate limit, …) is reported via t.Skip rather
// than t.Fatal: the test's concern is contract conformance, not registry
// availability. Daemon-reachability failures are already handled upstream.
func dockerPreflightForTest(t *testing.T, ctx context.Context) {
	t.Helper()
	cfg := dockerplugin.PreflightConfig{Docker: dockerplugin.Config{Image: dockerContractImage}}
	preflightCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := dockerplugin.Preflight(preflightCtx, cfg); err != nil {
		t.Skipf("docker preflight failed (image unavailable): %v", err)
	}
}

// Contract tests for Session interface.
// These tests verify that the docker backend satisfies the shared contract.

// TestSessionContract runs the full session contract against the docker factory.
func TestSessionContract(t *testing.T) {
	t.Run("DockerFactory", func(t *testing.T) {
		ctx := context.Background()
		if !dockerAvailable(ctx) {
			t.Skip("docker daemon not reachable; skipping DockerFactory contract test")
		}
		dockerPreflightForTest(t, ctx)
		testSessionContract(t, dockerplugin.NewFactory(dockerplugin.Config{Image: dockerContractImage}))
	})
}

func testSessionContract(t *testing.T, factory Factory) {
	ctx := context.Background()
	tempDir := t.TempDir()

	policy := Policy{
		Backend: factory.Name(),
		Filesystem: FilesystemPolicy{
			WorkingDir:   tempDir,
			AllowEscapes: false,
		},
		Network: NetworkPolicy{
			Mode: NetworkDisabled,
		},
		Process: ProcessPolicy{
			Timeout: 10 * time.Second,
		},
	}

	t.Run("CreateAndClose", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// Session should be alive initially
		if !session.Alive() {
			t.Error("session should be alive after creation")
		}

		// Policy should match
		if got := session.Policy(); got.Backend != policy.Backend {
			t.Errorf("Policy().Backend = %q, want %q", got.Backend, policy.Backend)
		}

		// Session should be non-nil and functional.
		if session == nil {
			t.Error("session should be non-nil")
		}

		// Close should succeed
		if err := session.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		// After close, session should not be alive
		if session.Alive() {
			t.Error("session should not be alive after Close()")
		}
	})

	t.Run("DoneChannel", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		done := session.Done()
		select {
		case <-done:
			t.Error("Done() should not be closed before Close()")
		case <-time.After(50 * time.Millisecond):
			// Expected
		}

		if err := session.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		select {
		case <-done:
			// Expected
		case <-time.After(time.Second):
			t.Error("Done() should be closed after Close()")
		}
	})

	t.Run("DoubleCloseIsSafe", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// First close
		if err := session.Close(); err != nil {
			t.Errorf("first Close: %v", err)
		}

		// Second close should be safe (may return error or nil)
		if err := session.Close(); err != nil {
			// Error on second close is acceptable
			t.Logf("second Close returned error (acceptable): %v", err)
		}
	})

	t.Run("HostConsistency", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		// WorkingDir should match policy
		if got := session.WorkingDir(); got != policy.Filesystem.WorkingDir {
			t.Errorf("WorkingDir() = %q, want %q", got, policy.Filesystem.WorkingDir)
		}

		// ResolvePath should work
		resolved, err := session.ResolvePath("test.txt")
		if err != nil {
			t.Errorf("ResolvePath: %v", err)
		}

		expected := filepath.Join(policy.Filesystem.WorkingDir, "test.txt")
		if resolved != expected {
			t.Errorf("ResolvePath(%q) = %q, want %q", "test.txt", resolved, expected)
		}
	})

	t.Run("Exec", func(t *testing.T) {
		session, err := factory.CreateSession(ctx, Policy{
			Backend: factory.Name(),
			Filesystem: FilesystemPolicy{
				WorkingDir:   tempDir,
				AllowEscapes: true,
			},
			Network: NetworkPolicy{
				Mode:    NetworkAllowAll,
				Timeout: 10 * time.Second,
			},
			Process: ProcessPolicy{
				Timeout: 10 * time.Second,
			},
		})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		defer func() { _ = session.Close() }()

		// Simple echo command
		execResult, err := session.Exec(ctx, "echo hello", ExecOptions{})
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}

		if execResult.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0", execResult.ExitCode)
		}

		if !containsSubstring(execResult.Stdout, "hello") {
			t.Errorf("Stdout = %q, should contain %q", execResult.Stdout, "hello")
		}
	})
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
