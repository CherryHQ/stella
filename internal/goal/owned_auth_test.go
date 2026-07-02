package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/toolctx"
)

func TestOwnedMethodsRejectUnauthenticatedIdentity(t *testing.T) {
	h := newHarness(t)
	g := h.createRoot(KindComposite, AcceptanceContract{})
	ctx := context.Background()

	if _, err := h.bundle.GetGoalOwned(ctx, toolctx.Identity{}, g.ID); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("GetGoalOwned err=%v, want unauthenticated", err)
	}
	if _, err := h.bundle.ListGoalsOwned(ctx, toolctx.Identity{}, GoalFilter{}, 10, 0); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("ListGoalsOwned err=%v, want unauthenticated", err)
	}
	if _, err := h.bundle.CreateGoalOwned(ctx, toolctx.Identity{}, CreateInput{AgentID: h.agentID, Title: "x", Kind: KindComposite}); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("CreateGoalOwned err=%v, want unauthenticated", err)
	}
	if err := h.bundle.CancelOwned(ctx, toolctx.Identity{}, g.ID, ""); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("CancelOwned err=%v, want unauthenticated", err)
	}
}

func TestOwnedMethodsEnforceGoalUserAndAgentBoundaries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	g := h.createRoot(KindComposite, AcceptanceContract{})

	foreign := toolctx.Identity{UserID: uuid.NewString()}
	if _, err := h.bundle.GetGoalOwned(ctx, foreign, g.ID); !errors.Is(err, toolctx.ErrNotFound) {
		t.Fatalf("foreign GetGoalOwned err=%v, want not found", err)
	}
	if err := h.bundle.CancelOwned(ctx, foreign, g.ID, ""); !errors.Is(err, toolctx.ErrNotFound) {
		t.Fatalf("foreign CancelOwned err=%v, want not found", err)
	}

	otherAgentID := uuid.NewString()
	if _, err := h.db.Exec(ctx, `INSERT INTO agent (id, name, workspace) VALUES ($1, 'other-agent', '/tmp')`, otherAgentID); err != nil {
		t.Fatalf("seed other agent: %v", err)
	}
	otherAgentGoal, err := h.bundle.CreateGoalOwned(ctx, toolctx.Identity{UserID: h.userID}, CreateInput{
		AgentID: otherAgentID,
		Title:   "other agent",
		Kind:    KindComposite,
	})
	if err != nil {
		t.Fatalf("CreateGoalOwned other agent: %v", err)
	}

	scoped := toolctx.Identity{UserID: h.userID, AgentID: h.agentID, AgentScoped: true}
	if _, err := h.bundle.GetGoalOwned(ctx, scoped, otherAgentGoal.ID); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("scoped GetGoalOwned err=%v, want forbidden", err)
	}
	if _, err := h.bundle.ListGoalsOwned(ctx, scoped, GoalFilter{AgentID: otherAgentID}, 10, 0); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("scoped ListGoalsOwned other agent err=%v, want forbidden", err)
	}
	if _, err := h.bundle.CreateGoalOwned(ctx, scoped, CreateInput{AgentID: otherAgentID, Title: "bad", Kind: KindComposite}); !errors.Is(err, toolctx.ErrForbidden) {
		t.Fatalf("scoped CreateGoalOwned other agent err=%v, want forbidden", err)
	}
}
