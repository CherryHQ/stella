package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
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

func TestWorkerAuthorizerRevokedExecutorFailsClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	goal := h.createRoot(KindComposite, AcceptanceContract{})
	worker := newWorkerAuthorizer(h.bundle.authz, h.bundle.agents)

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
	if err := worker.authorize(ctx, goal, goal.AgentID); err == nil {
		t.Fatal("worker authorized a revoked executor")
	}
}

func TestGoalListPropagatesDecisionError(t *testing.T) {
	h := newHarness(t)
	h.createRoot(KindComposite, AcceptanceContract{})
	h.bundle.authz = &erroringAuthorizer{Authorizer: policy.New(), pass: 1}

	_, _, _, err := h.begin(t, h.userAuth(t, h.userID)).ListGoals(context.Background(), GoalFilter{}, 10, 0)
	if err == nil || errors.Is(err, authz.ErrNotFound) || errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("ListGoals err=%v, want a propagated backend error", err)
	}
}

type erroringAuthorizer struct {
	authz.Authorizer
	pass int
}

func (a *erroringAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	evaluation, err := a.Authorizer.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	return &erroringEvaluation{Evaluation: evaluation, remaining: a.pass}, nil
}

type erroringEvaluation struct {
	authz.Evaluation
	remaining int
}

func (e *erroringEvaluation) Decide(request authz.Request) (authz.Decision, error) {
	if e.remaining <= 0 {
		return authz.Decision{}, errors.New("pdp backend unavailable")
	}
	e.remaining--
	return e.Evaluation.Decide(request)
}
