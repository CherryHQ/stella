package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
)

func (h *harness) userAuth(t *testing.T, id string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), false)
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
	h.bundle.agents = nil
	if _, err := h.bundle.Begin(context.Background(), h.userAuth(t, h.userID)); err == nil {
		t.Fatal("Begin authorized without Agent access")
	}
}

func TestGoalDirectRulesAdminAndNonUserActors(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	own := h.createRoot(KindComposite, AcceptanceContract{})
	foreignUserID := uuid.NewString()
	if _, err := h.db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, foreignUserID, "foreign-"+foreignUserID[:8]+"@example.com"); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	foreign, err := h.svc.CreateRoot(ctx, CreateInput{UserID: foreignUserID, AgentID: h.agentID, Title: "foreign", Kind: KindComposite})
	if err != nil {
		t.Fatalf("create foreign root: %v", err)
	}

	admin, err := authz.NewUserAuthority(authz.UserID(uuid.NewString()), true)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := h.begin(t, admin).ListGoals(ctx, GoalFilter{}, 10, 0)
	if err != nil {
		t.Fatalf("admin ListGoals: %v", err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if !seen[own.ID] || !seen[foreign.ID] {
		t.Fatalf("admin list=%v, want own %s and foreign %s", seen, own.ID, foreign.ID)
	}
	if got, err := h.begin(t, admin).CountGoals(ctx, GoalFilter{}); err != nil || got != 2 {
		t.Fatalf("admin CountGoals = %d, %v; want 2, nil", got, err)
	}

	group, err := authz.NewGroupAgentAuthority("group", authz.AgentID(h.agentID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.begin(t, group).Get(ctx, own.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("group Get err=%v, want opaque not found", err)
	}
	system, err := authz.NewSystemAuthority("test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.begin(t, system).Get(ctx, own.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("system Get err=%v, want opaque not found", err)
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

func TestGoalListPaginatesOnlyVisibleRowsAndCountMatches(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for range 3 {
		h.createRoot(KindComposite, AcceptanceContract{})
	}
	foreignUserID := uuid.NewString()
	if _, err := h.db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, foreignUserID, "foreign-page-"+foreignUserID[:8]+"@example.com"); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.svc.CreateRoot(ctx, CreateInput{UserID: foreignUserID, AgentID: h.agentID, Title: "foreign", Kind: KindComposite}); err != nil {
		t.Fatalf("create foreign root: %v", err)
	}

	acc := h.begin(t, h.userAuth(t, h.userID))
	first, next, more, err := acc.ListGoals(ctx, GoalFilter{}, 1, 0)
	if err != nil || len(first) != 1 || !more {
		t.Fatalf("page one = %d, next=%d, more=%v, err=%v; want one visible row and continuation", len(first), next, more, err)
	}
	second, _, _, err := acc.ListGoals(ctx, GoalFilter{}, 10, next)
	if err != nil {
		t.Fatalf("page two: %v", err)
	}
	if len(second) != 2 || second[0].ID == first[0].ID || second[1].ID == first[0].ID {
		t.Fatalf("page two=%v, want remaining two distinct owner rows", second)
	}
	if got, err := acc.CountGoals(ctx, GoalFilter{}); err != nil || got != 3 {
		t.Fatalf("CountGoals = %d, %v; want 3, nil", got, err)
	}
}

func TestGoalIdempotencyReplayUsesDurableGoalFacts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	key := "durable-replay"
	first, err := h.begin(t, h.agentAuth(t, h.userID, h.agentID)).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "first", Kind: KindComposite, IdempotencyKey: key})
	if err != nil {
		t.Fatalf("first CreateGoal: %v", err)
	}
	otherAgentID := h.seedSystemAgent(t)
	if _, err := h.db.Exec(ctx, `UPDATE agent_goal SET agent_id = $2 WHERE id = $1`, first.ID, otherAgentID); err != nil {
		t.Fatalf("change durable executor: %v", err)
	}
	if _, err := h.begin(t, h.agentAuth(t, h.userID, h.agentID)).CreateGoal(ctx, CreateInput{AgentID: h.agentID, Title: "replay", Kind: KindComposite, IdempotencyKey: key}); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("replay err=%v, want opaque denial from durable executor", err)
	}
}

func TestGoalEnforcesOwnerAndExecutorBoundaries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	g := h.createRoot(KindComposite, AcceptanceContract{})

	// A foreign user cannot read or cancel the goal (opaque not-found).
	foreign := h.userAuth(t, uuid.NewString())
	if _, err := h.begin(t, foreign).Get(ctx, g.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Get err=%v, want not found", err)
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
	rows, _, _, err := h.begin(t, scoped).ListGoals(ctx, GoalFilter{}, 10, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != g.ID {
		t.Fatalf("delegated executor ListGoals = %v, %v; want only %s", rows, err, g.ID)
	}
}
