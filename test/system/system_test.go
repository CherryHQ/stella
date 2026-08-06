//go:build system

package system

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSystem is the single owner of the suite's server and database. Journeys
// run as ordered subtests — never t.Parallel() — and each scopes its fixtures
// with h.runID so no journey depends on another's business data.
func TestSystem(t *testing.T) {
	h := newHarness(t)
	t.Run("readiness", h.testReadiness)
	t.Run("pwa_assets_anonymous", h.testPWAAssetsAnonymous)
	t.Run("startup_and_auth", h.testStartupAndAuth)
	t.Run("chat_sse", h.testChatSSE)
	t.Run("agent_provider_credentials", h.testAgentProviderCredentials)
	t.Run("image_history", h.testImageHistory)
	t.Run("read_tool_image_history", h.testReadToolImageHistory)
	t.Run("chat_provider_error", h.testChatProviderError)
	t.Run("webhook_sync_persistent", h.testWebhookSyncPersistent)
	t.Run("goal_lifecycle", h.testGoalLifecycle)
	t.Run("github_webhook_compatibility", h.testGitHubWebhookCompatibility)
	t.Run("scheduler_one_time_job_survives_forced_restart", h.testSchedulerOneTimeJobSurvivesForcedRestart)
	// graceful_drain MUST run last: it sends SIGTERM to the shared server and
	// asserts the process exits, consuming the server no later journey can use.
	t.Run("graceful_drain", h.testGracefulDrain)
}

// testReadiness proves the boot premise end to end: the production binary
// migrated the shared database, bound a real TCP listener, and reports ready
// over the wire.
func (h *harness) testReadiness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/readyz", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /readyz: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}

	// The subprocess, not the harness, must have migrated the database it was
	// handed: goose records applied versions in goose_db_version.
	var migrations int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM goose_db_version").Scan(&migrations); err != nil {
		t.Fatalf("query goose_db_version: %v", err)
	}
	if migrations == 0 {
		t.Fatal("goose_db_version is empty: subprocess did not migrate the database")
	}
}

// TestHarnessEarlyExit proves the harness detects a subprocess that dies
// during startup quickly — long before the readiness timeout — and that the
// failure carries the server log path. It boots the binary without a vault
// key, which stellad rejects before touching the database, so this test needs
// neither PostgreSQL nor its runtime.
func TestHarnessEarlyExit(t *testing.T) {
	skipUnsupportedHost(t)

	runID := newRunID(t)
	port := freePort(t)
	env := append(baseSubprocessEnv(),
		"STELLA_HOME="+t.TempDir(),
		"STELLA_DATABASE_URL=postgres://postgres:postgres@127.0.0.1:1/stella?sslmode=disable",
		"HOST=127.0.0.1",
		fmt.Sprintf("PORT=%d", port),
	)
	proc := startServerProcess(t, t, "early-exit-"+runID, env)

	start := time.Now()
	err := proc.waitReady(fmt.Sprintf("http://127.0.0.1:%d", port), readyTimeout)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("waitReady succeeded for a server that must refuse to start")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("early exit detected after %s; must not wait toward the %s readiness timeout", elapsed, readyTimeout)
	}
	if !strings.Contains(err.Error(), proc.logPath) {
		t.Errorf("early-exit error must include the server log path %s, got: %v", proc.logPath, err)
	}
	if !strings.Contains(err.Error(), "exited before ready") {
		t.Errorf("error must state the server exited early, got: %v", err)
	}
}
