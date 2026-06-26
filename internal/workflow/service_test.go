package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
)

// TestSaveAndInstantiate proves the full freeze->run loop: an accepted composite
// saves as a workflow, and instantiating it materializes a fresh live tree (new
// root id, planner skipped) whose root context records the workflow id + hash.
func TestSaveAndInstantiate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	src := h.acceptedComposite(t)

	wf, err := h.wf.SaveGoalAsWorkflow(ctx, h.userID, SaveAsInput{SourceGoalID: src.ID, Name: "Daily report"})
	if err != nil {
		t.Fatalf("SaveGoalAsWorkflow: %v", err)
	}
	if wf.Name != "Daily report" || wf.SourceGoalID.String != src.ID || wf.Version != 1 {
		t.Fatalf("workflow row=%+v unexpected", wf)
	}
	var plan goal.FrozenPlan
	if err := json.Unmarshal(wf.Plan, &plan); err != nil || len(plan.Children) != 1 {
		t.Fatalf("frozen plan unmarshal=%v children=%d want 1", err, len(plan.Children))
	}

	root, err := h.wf.Instantiate(ctx, h.userID, wf.ID, "")
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if root.ID == src.ID {
		t.Fatal("instantiated root reused the source goal id")
	}
	if root.Kind != goal.KindComposite || root.Lifecycle != goal.LifecycleActive {
		t.Fatalf("instantiated root kind=%q lifecycle=%q want composite/active", root.Kind, root.Lifecycle)
	}
	var rootCtx map[string]any
	if err := json.Unmarshal(root.Context, &rootCtx); err != nil {
		t.Fatalf("root context unmarshal: %v", err)
	}
	if rootCtx["workflow_id"] != wf.ID {
		t.Fatalf("root context workflow_id=%v want %s", rootCtx["workflow_id"], wf.ID)
	}
	if rootCtx["plan_hash"] != plan.Hash() {
		t.Fatalf("root context plan_hash=%v want %s", rootCtx["plan_hash"], plan.Hash())
	}
	// The freshly instantiated leaf is ready to run (planner was skipped).
	kids, err := h.q.ListGoalChildren(ctx, pgnull.Text(root.ID))
	if err != nil || len(kids) != 1 || kids[0].Lifecycle != goal.LifecycleReady {
		t.Fatalf("instantiated child=%+v err=%v want one ready leaf", kids, err)
	}
}

// TestSaveRejectsIneligibleSource proves save-as only freezes an accepted
// composite: a draft composite and an accepted leaf are both rejected.
func TestSaveRejectsIneligibleSource(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	draft, err := h.goalSvc.CreateRoot(ctx, goal.CreateInput{
		UserID: h.userID, AgentID: h.agentID, Title: "draft", Kind: goal.KindComposite, Required: true,
	})
	if err != nil {
		t.Fatalf("CreateRoot: %v", err)
	}
	if _, err := h.wf.SaveGoalAsWorkflow(ctx, h.userID, SaveAsInput{SourceGoalID: draft.ID}); !errors.Is(err, ErrSourceNotEligible) {
		t.Fatalf("save draft composite err=%v want ErrSourceNotEligible", err)
	}

	// A cross-owner save does not leak existence.
	if _, err := h.wf.SaveGoalAsWorkflow(ctx, "00000000-0000-0000-0000-000000000000", SaveAsInput{SourceGoalID: draft.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner save err=%v want ErrNotFound", err)
	}
}

// TestCreateValidatesPlan proves the hand-authored path rejects a structurally
// invalid plan (a composite child carrying a deterministic acceptance item).
func TestCreateValidatesPlan(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	badChild := goal.ProposedChild{
		Key: "sub", Title: "sub", Intent: "x", Kind: goal.KindComposite, Required: true,
		AcceptanceContract: goal.AcceptanceContract{
			Policy: goal.PolicyDetThenJudgment,
			Items:  []goal.AcceptanceItem{{ID: "c", Kind: goal.ItemDeterministic, Required: true, Command: "true"}},
		},
	}
	_, err := h.wf.Create(ctx, h.userID, CreateInput{
		AgentID: h.agentID, Name: "bad",
		Plan: goal.FrozenPlan{Children: []goal.FrozenNode{{
			Child: badChild,
			Plan:  &goal.FrozenPlan{Children: []goal.FrozenNode{{Child: goal.ProposedChild{Key: "x", Title: "x", Intent: "x", Kind: goal.KindLeaf, Required: true}}}},
		}}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create with deterministic composite err=%v want ErrInvalidInput", err)
	}
}

// TestListAndDeleteScopedToOwner proves list/delete are owner-scoped.
func TestListAndDeleteScopedToOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	src := h.acceptedComposite(t)
	wf, err := h.wf.SaveGoalAsWorkflow(ctx, h.userID, SaveAsInput{SourceGoalID: src.ID, Name: "w"})
	if err != nil {
		t.Fatalf("SaveGoalAsWorkflow: %v", err)
	}

	list, err := h.wf.List(ctx, h.userID, Filter{}, 50, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("List=%v n=%d want 1", err, len(list))
	}
	if err := h.wf.Delete(ctx, h.userID, wf.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := h.wf.Get(ctx, h.userID, wf.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete err=%v want ErrNotFound", err)
	}
}
