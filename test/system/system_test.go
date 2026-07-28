//go:build system

package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

// TestSystem is the single owner of the suite's server and database. Journeys
// run as ordered subtests — never t.Parallel() — and each scopes its fixtures
// with h.runID so no journey depends on another's business data.
func TestSystem(t *testing.T) {
	h := newHarness(t)
	t.Run("readiness", h.testReadiness)
	t.Run("startup_and_auth", h.testStartupAndAuth)
	t.Run("chat_sse", h.testChatSSE)
	t.Run("chat_provider_error", h.testChatProviderError)
	t.Run("goal_lifecycle", h.testGoalLifecycle)
	t.Run("embedded_restart", h.testEmbeddedRestart)
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

	// The subprocess, not the harness, must have created and migrated its
	// embedded database: goose records applied versions in goose_db_version.
	var migrations int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM goose_db_version").Scan(&migrations); err != nil {
		t.Fatalf("query goose_db_version: %v", err)
	}
	if migrations == 0 {
		t.Fatal("goose_db_version is empty: subprocess did not migrate the database")
	}

	h.assertCandidateIdentity(t, ctx)
}

// assertCandidateIdentity proves that a release-tag run is serving the version
// and commit recorded by the shared immutable Run rather than a locally rebuilt
// binary. Non-release system-test invocations still assert a healthy status.
func (h *harness) assertCandidateIdentity(t *testing.T, ctx context.Context) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/status", nil)
	if err != nil {
		t.Fatalf("build status request: %v", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/status: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/status = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}
	var status struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /api/status: %v", err)
	}
	if status.Status != "ok" {
		t.Fatalf("/api/status status = %q, want ok", status.Status)
	}

	run, present, err := releasecontract.RunFromEnv()
	if err != nil {
		t.Fatalf("load release metadata: %v", err)
	}
	if !present {
		return
	}
	versionMatches := status.Version == run.Version || status.Version == strings.TrimPrefix(run.Version, "v")
	if !versionMatches {
		t.Fatalf("/api/status version = %q, want release candidate %q", status.Version, run.Version)
	}
	if status.Commit == "" || !strings.HasPrefix(run.Commit, status.Commit) {
		t.Fatalf("/api/status commit = %q, want prefix of %s", status.Commit, run.Commit)
	}
}

// testEmbeddedRestart stops the exact candidate and starts it again with the
// same STELLA_HOME. Readiness, migrations, and the existing session must
// survive while the candidate owns both application and PostgreSQL lifecycles.
func (h *harness) testEmbeddedRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if code := h.statusOf(t, ctx, h.newAuthedGet(t, ctx, nil)); code != http.StatusOK {
		t.Fatalf("GET /api/auth/me before restart = %d, want %d", code, http.StatusOK)
	}

	h.proc.stop(t)
	h.db.Close()
	h.db = nil

	proc, baseURL := startServer(t, h.runID+"-restart", h.home, h.vaultKey)
	h.proc = proc
	h.baseURL = baseURL

	db, err := pgxpool.New(ctx, embeddedDSN(t, h.home))
	if err != nil {
		t.Fatalf("connect embedded PostgreSQL after restart: %v", err)
	}
	h.db = db

	if code := h.statusOf(t, ctx, h.newAuthedGet(t, ctx, nil)); code != http.StatusOK {
		t.Fatalf("GET /api/auth/me after restart = %d, want %d\n%s", code, http.StatusOK, h.proc.logTail(40))
	}
	var migrations int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM goose_db_version").Scan(&migrations); err != nil {
		t.Fatalf("query migrations after restart: %v", err)
	}
	if migrations == 0 {
		t.Fatal("goose_db_version is empty after restart")
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
	proc := startServerProcess(t, "early-exit-"+runID, env)
	t.Cleanup(func() { proc.stop(t) })

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
