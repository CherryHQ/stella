package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestAuthorizedMethodsRejectUnauthenticatedIdentity(t *testing.T) {
	h := newHarness(t)
	g := h.createRoot(KindComposite, AcceptanceContract{})
	ctx := context.Background()

	if _, err := h.bundle.As(authz.Identity{}).GetGoal(ctx, g.ID); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("GetGoal err=%v, want unauthenticated", err)
	}
	if _, err := h.bundle.As(authz.Identity{}).ListGoals(ctx, GoalFilter{}, 10, 0); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("ListGoals err=%v, want unauthenticated", err)
	}
	if _, err := h.bundle.As(authz.Identity{}).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "x", Kind: KindComposite}); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("CreateGoal err=%v, want unauthenticated", err)
	}
	if err := h.bundle.As(authz.Identity{}).Cancel(ctx, g.ID, ""); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("Cancel err=%v, want unauthenticated", err)
	}
}

func TestCreateGoalIdempotencyReturnsExistingGoal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ident := authz.Identity{UserID: h.userID, AgentID: h.agentID, AgentScoped: true}
	first, err := h.bundle.As(ident).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "first", Kind: KindComposite, IdempotencyKey: "goal-key"})
	if err != nil {
		t.Fatalf("first CreateGoal: %v", err)
	}
	second, err := h.bundle.As(ident).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "second", Kind: KindComposite, IdempotencyKey: "goal-key"})
	if err != nil {
		t.Fatalf("second CreateGoal: %v", err)
	}
	if second.ID != first.ID || second.Title != first.Title {
		t.Fatalf("second=%+v first=%+v, want existing goal", second, first)
	}
}

func TestAuthorizedMethodsEnforceGoalUserAndAgentBoundaries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	g := h.createRoot(KindComposite, AcceptanceContract{})

	foreign := authz.Identity{UserID: uuid.NewString()}
	if _, err := h.bundle.As(foreign).GetGoal(ctx, g.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign GetGoal err=%v, want not found", err)
	}
	if err := h.bundle.As(foreign).Cancel(ctx, g.ID, ""); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Cancel err=%v, want not found", err)
	}

	otherAgentID := uuid.NewString()
	if _, err := h.db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'other-agent', '/tmp')`, otherAgentID); err != nil {
		t.Fatalf("seed other agent: %v", err)
	}
	otherAgentGoal, err := h.bundle.As(authz.Identity{UserID: h.userID}).CreateGoal(ctx, CreateInput{
		AgentID: otherAgentID,
		Title:   "other agent",
		Kind:    KindComposite,
	})
	if err != nil {
		t.Fatalf("CreateGoal other agent: %v", err)
	}

	scoped := authz.Identity{UserID: h.userID, AgentID: h.agentID, AgentScoped: true}
	if _, err := h.bundle.As(scoped).GetGoal(ctx, otherAgentGoal.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped GetGoal err=%v, want forbidden", err)
	}
	if _, err := h.bundle.As(scoped).ListGoals(ctx, GoalFilter{AgentID: otherAgentID}, 10, 0); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped ListGoals other agent err=%v, want forbidden", err)
	}
	if _, err := h.bundle.As(scoped).CreateGoal(ctx, CreateInput{AgentID: otherAgentID, Title: "bad", Kind: KindComposite}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("scoped CreateGoal other agent err=%v, want forbidden", err)
	}
}
