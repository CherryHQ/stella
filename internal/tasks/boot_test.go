package tasks

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// seedAgent inserts an extra agent row and returns its id, for tests that need
// an executor distinct from the task owner.
func (h *testHarness) seedAgent(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO agent (id, name, workspace) VALUES (?, ?, '/tmp')`,
		id, name); err != nil {
		t.Fatalf("seed agent %s: %v", name, err)
	}
	return id
}

// TestSessionOwnerResolver_ReusesLatestRunExecutor proves the resolver returns
// the executor of the task's latest worker run — not merely the task owner — so a
// task first dispatched to another agent via a hint keeps that executor on
// retry. (CR-002)
func TestSessionOwnerResolver_ReusesLatestRunExecutor(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	execB := h.seedAgent(t, "executor-b")

	id := h.createTask(t, StatusReady) // owner is h.agentID
	if _, err := h.svc.Claim(ctx, ClaimParams{
		TaskID: id, ExecutorAgentID: execB, SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	r := sessionOwnerResolver(h.q, slog.Default())
	got, ok := r(ctx, h.getTask(t, id))
	if !ok || got != execB {
		t.Fatalf("resolver = (%q,%v), want (%q,true)", got, ok, execB)
	}
}

// TestSessionOwnerResolver_NoSessionFalls: a task that has never run has no
// worker run to derive an executor from, so the resolver declines and the
// dispatcher falls back to the owner agent.
func TestSessionOwnerResolver_NoSessionFalls(t *testing.T) {
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	r := sessionOwnerResolver(h.q, slog.Default())
	if _, ok := r(context.Background(), h.getTask(t, id)); ok {
		t.Fatal("resolver should decline a task with no session_id")
	}
}

// TestSessionOwnerResolver_NoExecutorOnRunFalls: a session exists but its run
// recorded no executor (owner-dispatched, executor agent unknown), so the
// resolver declines rather than inventing one.
func TestSessionOwnerResolver_NoExecutorOnRunFalls(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	id := h.createTask(t, StatusReady)
	if _, err := h.svc.Claim(ctx, ClaimParams{
		TaskID: id, ExecutorAgentID: "", SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	r := sessionOwnerResolver(h.q, slog.Default())
	if _, ok := r(ctx, h.getTask(t, id)); ok {
		t.Fatal("resolver should decline when the latest run carried no executor")
	}
}
