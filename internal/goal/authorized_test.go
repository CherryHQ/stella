package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
)

// countingAuthorizer proves the PEP owns exactly one Begin per use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

func (h *harness) userAuth(t *testing.T, id string) authz.Authority {
	t.Helper()
	rs, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority(authz.UserID(id), rs, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (h *harness) agentAuth(t *testing.T, userID, agentID string) authz.Authority {
	t.Helper()
	a, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (h *harness) begin(t *testing.T, authority authz.Authority) *Access {
	t.Helper()
	acc, err := h.bundle.Begin(context.Background(), authority)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return acc
}

func TestGoalBeginRejectsInvalidAuthority(t *testing.T) {
	h := newHarness(t)
	if _, err := h.bundle.Begin(context.Background(), authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Begin(zero) err=%v, want forbidden", err)
	}
}

func TestGoalCreateIdempotencyReturnsExistingGoal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	auth := h.agentAuth(t, h.userID, h.agentID)
	first, err := h.begin(t, auth).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "first", Kind: KindComposite, IdempotencyKey: "goal-key"})
	if err != nil {
		t.Fatalf("first CreateGoal: %v", err)
	}
	second, err := h.begin(t, auth).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "second", Kind: KindComposite, IdempotencyKey: "goal-key"})
	if err != nil {
		t.Fatalf("second CreateGoal: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second=%s first=%s, want existing goal", second.ID, first.ID)
	}
}

func TestGoalEnforcesOwnerAndExecutorBoundaries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	az := &countingAuthorizer{Authorizer: policy.New()}
	h.bundle.authz = az

	g := h.createRoot(KindComposite, AcceptanceContract{})

	// A foreign user cannot read or cancel the goal (opaque not-found).
	foreign := h.userAuth(t, uuid.NewString())
	before := az.begins
	if _, err := h.begin(t, foreign).Get(ctx, g.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Get err=%v, want not found", err)
	}
	if az.begins != before+1 {
		t.Fatalf("Begin count = %d, want 1 per use case", az.begins-before)
	}
	if err := h.begin(t, foreign).Cancel(ctx, g.ID, ""); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Cancel err=%v, want not found", err)
	}

	// A second (system) agent and a goal owned by the same user but bound to it.
	otherAgentID := uuid.NewString()
	if _, err := h.db.Exec(ctx, `INSERT INTO agent (id, name, workspace, scope) VALUES ($1, 'other-agent', '/tmp', 'system')`, otherAgentID); err != nil {
		t.Fatalf("seed other agent: %v", err)
	}
	otherGoal, err := h.begin(t, h.userAuth(t, h.userID)).CreateGoal(ctx, CreateInput{AgentID: otherAgentID, Title: "other agent", Kind: KindComposite})
	if err != nil {
		t.Fatalf("CreateGoal other agent: %v", err)
	}

	// A delegated agent confined to h.agentID cannot read/create/list on another.
	scoped := h.agentAuth(t, h.userID, h.agentID)
	if _, err := h.begin(t, scoped).Get(ctx, otherGoal.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("scoped Get other-agent goal err=%v, want not found", err)
	}
	if _, _, _, err := h.begin(t, scoped).ListGoals(ctx, GoalFilter{AgentID: otherAgentID}, 10, 0); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped ListGoals other agent err=%v, want forbidden", err)
	}
	if _, err := h.begin(t, scoped).CreateGoal(ctx, CreateInput{AgentID: otherAgentID, Title: "bad", Kind: KindComposite}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped CreateGoal other agent err=%v, want forbidden", err)
	}

	// The owner reads their own goal; a delegated executor of h.agentID reads it.
	if _, err := h.begin(t, h.userAuth(t, h.userID)).Get(ctx, g.ID); err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	if _, err := h.begin(t, scoped).Get(ctx, g.ID); err != nil {
		t.Fatalf("delegated executor Get: %v", err)
	}
}
