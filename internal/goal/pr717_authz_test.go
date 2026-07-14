package goal

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func (h *harness) seedSystemAgent(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.db.Exec(context.Background(), `INSERT INTO agent (id, name, workspace, scope) VALUES ($1, 'replacement-agent', '/tmp', 'system')`, id); err != nil {
		t.Fatalf("seed replacement agent: %v", err)
	}
	return id
}

func TestGoalIdempotencyConflictMatchesOnlyItsIndex(t *testing.T) {
	idempotency := &pgconn.PgError{Code: "23505", ConstraintName: "idx_agent_goal_idem"}
	primary := &pgconn.PgError{Code: "23505", ConstraintName: "agent_goal_pkey"}
	if !isGoalIdempotencyConflict(idempotency) || isGoalIdempotencyConflict(primary) {
		t.Fatal("goal idempotency conflict classifier matched the wrong unique index")
	}
}

func TestWorkerAuthorizerMissingAgentAccessFailsClosed(t *testing.T) {
	h := newHarness(t)
	goal := h.createRoot(KindComposite, AcceptanceContract{})
	if err := newWorkerAuthorizer(nil).authorize(context.Background(), goal, goal.AgentID); err == nil {
		t.Fatal("worker authorized without Agent access")
	}
}

func TestWorkerAuthorizerRevokedExecutorFailsClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	goal := h.createRoot(KindComposite, AcceptanceContract{})
	worker := newWorkerAuthorizer(h.bundle.agents)

	if err := worker.authorize(ctx, goal, goal.AgentID); err != nil {
		t.Fatalf("authorize active executor: %v", err)
	}
	replacement := h.seedSystemAgent(t)
	if _, err := h.db.Exec(ctx, `UPDATE agent_goal SET agent_id = $2 WHERE id = $1`, goal.ID, replacement); err != nil {
		t.Fatalf("detach executor from goal: %v", err)
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM agent WHERE id = $1`, goal.AgentID); err != nil {
		t.Fatalf("revoke executor: %v", err)
	}
	// The worker receives the freshly loaded durable row at dequeue, not the
	// stale row held before revocation. A stale test input would only test its own
	// artifact, not the worker contract.
	dequeued, err := h.q.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("reload dequeued goal: %v", err)
	}
	if err := worker.authorize(ctx, dequeued, goal.AgentID); err == nil {
		t.Fatal("worker authorized a revoked executor")
	}
}
