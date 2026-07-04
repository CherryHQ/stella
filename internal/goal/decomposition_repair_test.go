package goal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestDecompositionRepairLoop_ReusesPlanningSessionAndMaterializes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	var firstSession string
	calls := 0
	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		calls++
		if req.Input.Title != "root" {
			t.Fatalf("planner input title=%q want root", req.Input.Title)
		}
		if firstSession == "" {
			firstSession = req.Attempt.SessionID
		} else if req.Attempt.SessionID != firstSession {
			t.Fatalf("repair session=%q want first session %q", req.Attempt.SessionID, firstSession)
		}
		switch calls {
		case 1:
			if len(req.Input.PriorErrors) != 0 {
				t.Fatalf("first call prior_errors=%d want 0", len(req.Input.PriorErrors))
			}
			return ExecutorResult{Submitted: true, Evidence: AttemptEvidence{Summary: "bad"}, Decomposition: &DecompositionContent{
				Children: []ProposedChild{cmp_child("a", false)},
			}}, nil
		case 2:
			if len(req.Input.PriorErrors) == 0 {
				t.Fatalf("repair call got no prior_errors")
			}
			if got := RenderErrorsJSON(req.Input.PriorErrors); got == "[]" {
				t.Fatalf("prior_errors JSON rendered empty")
			}
			return ExecutorResult{Submitted: true, Evidence: AttemptEvidence{Summary: "fixed"}, Decomposition: &DecompositionContent{
				Children: []ProposedChild{cmp_child("a", true)},
			}}, nil
		default:
			t.Fatalf("unexpected planner call %d", calls)
			return ExecutorResult{}, nil
		}
	}

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	if err := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("worker Run: %v", err)
	}
	if calls != 2 {
		t.Fatalf("planner calls=%d want 2", calls)
	}
	got := h.get(root.ID)
	if !got.PlannedAt.Valid || got.Lifecycle != LifecycleActive {
		t.Fatalf("root planned=%v lifecycle=%q want planned active", got.PlannedAt.Valid, got.Lifecycle)
	}
	if child := h.get(childID(root.ID, "a")); child.Lifecycle != LifecyclePending {
		t.Fatalf("materialized child lifecycle=%q want ready", child.Lifecycle)
	}
	attempts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: root.ID, Purpose: pgnull.Text(PurposeDecomposition)})
	if err != nil {
		t.Fatalf("ListAttemptByGoal: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != AttemptSubmitted || attempts[0].FailureClass != "" || attempts[0].RepairRounds != 1 {
		t.Fatalf("attempts=%+v want one submitted attempt without failure_class and repair_rounds=1", attempts)
	}
}

func TestDecompositionRepairLoop_ExhaustionRecordsModelFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})
	calls := 0
	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		calls++
		if calls > 1 && len(req.Input.PriorErrors) == 0 {
			t.Fatalf("repair call %d got no prior_errors", calls)
		}
		return ExecutorResult{Submitted: true, Evidence: AttemptEvidence{Summary: "bad"}, Decomposition: &DecompositionContent{
			Children: []ProposedChild{cmp_child("a", false)},
		}}, nil
	}

	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	if err := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("worker Run: %v", err)
	}
	if calls != defaultPlannerRepairMax+1 {
		t.Fatalf("planner calls=%d want %d", calls, defaultPlannerRepairMax+1)
	}
	got := h.get(root.ID)
	if got.Lifecycle != LifecycleDraft || got.BlockReason != "" {
		t.Fatalf("root lifecycle=%q reason=%q want draft/unblocked", got.Lifecycle, got.BlockReason)
	}
	if got.AttemptCount != 0 {
		t.Fatalf("attempt_count=%d want 0; decomposition budget is metered by attempt_no", got.AttemptCount)
	}
	var pol ConvergencePolicy
	if err := unmarshalJSON(got.ConvergencePolicy, &pol); err != nil {
		t.Fatalf("decode convergence_policy: %v", err)
	}
	if pol.MaxAttempts != defaultMaxAttempts {
		t.Fatalf("max_attempts=%d want %d; model planning failures consume attempt_no, not mutate the ceiling", pol.MaxAttempts, defaultMaxAttempts)
	}
	attempts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: root.ID, Purpose: pgnull.Text(PurposeDecomposition)})
	if err != nil {
		t.Fatalf("ListAttemptByGoal: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != AttemptFailed || attempts[0].FailureClass != FailureClassModel || attempts[0].RepairRounds != 2 {
		t.Fatalf("attempts=%+v want one failed model attempt with repair_rounds=2", attempts)
	}
}

func TestDecompositionFailureClass_PersistsModelAndFlaky(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cases := []struct {
		name          string
		finalize      func(string) error
		wantClass     string
		wantLifecycle string
	}{
		{
			name: "model",
			finalize: func(attemptID string) error {
				return h.svc.FailAttempt(ctx, attemptID, "planner cannot decompose", FailureClassModel)
			},
			wantClass:     FailureClassModel,
			wantLifecycle: LifecycleDraft,
		},
		{
			name: "flaky",
			finalize: func(attemptID string) error {
				return h.svc.ReapAttempt(ctx, attemptID)
			},
			wantClass:     FailureClassFlaky,
			wantLifecycle: LifecycleDraft,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := h.createRoot(KindComposite, AcceptanceContract{})
			att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, AttemptEnqueuer(func(context.Context, pgx.Tx, string, string) error { return nil }))
			if err != nil {
				t.Fatalf("BeginAutoDecomposition: %v", err)
			}
			if err := tc.finalize(att.ID); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			stored, err := h.q.GetAttempt(ctx, att.ID)
			if err != nil {
				t.Fatalf("GetAttempt: %v", err)
			}
			if stored.FailureClass != tc.wantClass {
				t.Fatalf("failure_class=%q want %q", stored.FailureClass, tc.wantClass)
			}
			if got := h.get(root.ID).Lifecycle; got != tc.wantLifecycle {
				t.Fatalf("lifecycle=%q want %q", got, tc.wantLifecycle)
			}
		})
	}
}

func TestDecompositionFlakyFailureRecoversToDraftAndCaps(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindComposite, AcceptanceContract{})

	for i := 1; i <= 5; i++ {
		att, err := h.svc.BeginDecomposition(ctx, root.ID)
		if err != nil {
			t.Fatalf("BeginDecomposition #%d: %v", i, err)
		}
		if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
			t.Fatalf("PromoteAttempt #%d: %v", i, err)
		}
		if err := h.svc.FailAttempt(ctx, att.ID, "runner error", FailureClassFlaky); err != nil {
			t.Fatalf("FailAttempt #%d: %v", i, err)
		}

		got := h.get(root.ID)
		if got.Lifecycle != LifecycleDraft || got.FlakyCount != int64(i) {
			t.Fatalf("after flaky %d goal=(%s flaky=%d) want draft flaky=%d", i, got.Lifecycle, got.FlakyCount, i)
		}
		assertDecomposable(t, h, root.ID, true)
		assertDecompositionBudgetRemaining(t, h, root.ID, defaultMaxAttempts)
	}

	att, err := h.svc.BeginDecomposition(ctx, root.ID)
	if err != nil {
		t.Fatalf("BeginDecomposition cap: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("PromoteAttempt cap: %v", err)
	}
	if err := h.svc.FailAttempt(ctx, att.ID, "runner error", FailureClassFlaky); err != nil {
		t.Fatalf("FailAttempt cap: %v", err)
	}
	got := h.get(root.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockEnvUnavailable || got.FlakyCount != 6 {
		t.Fatalf("after flaky cap goal=(%s,%s flaky=%d) want blocked/env_unavailable flaky=6", got.Lifecycle, got.BlockReason, got.FlakyCount)
	}
	assertDecomposable(t, h, root.ID, false)
	assertDecompositionBudgetRemaining(t, h, root.ID, defaultMaxAttempts)
}

func assertDecomposable(t *testing.T, h *harness, goalID string, want bool) {
	t.Helper()
	rows, err := h.q.ListDecomposableComposites(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListDecomposableComposites: %v", err)
	}
	for _, row := range rows {
		if row.ID == goalID {
			if !want {
				t.Fatalf("goal %s is decomposable, want not decomposable", goalID)
			}
			return
		}
	}
	if want {
		t.Fatalf("goal %s not listed as decomposable", goalID)
	}
}

func assertDecompositionBudgetRemaining(t *testing.T, h *harness, goalID string, want int) {
	t.Helper()
	got := h.get(goalID)
	var pol ConvergencePolicy
	if err := unmarshalJSON(got.ConvergencePolicy, &pol); err != nil {
		t.Fatalf("decode convergence policy: %v", err)
	}
	pol = pol.Normalized()
	spent, err := h.svc.spentAttemptBudget(context.Background(), h.q, goalID, PurposeDecomposition)
	if err != nil {
		t.Fatalf("CountBillableAttempts: %v", err)
	}
	if remaining := pol.MaxAttempts + int(got.BudgetBonus) - spent; remaining != want {
		t.Fatalf("decomposition remaining budget=%d want %d (max_attempts=%d bonus=%d spent=%d)", remaining, want, pol.MaxAttempts, got.BudgetBonus, spent)
	}
}

func TestAttemptInputCarriesGoalContext(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	contextJSON := json.RawMessage(`{"note":"keep this"}`)
	root, err := h.svc.CreateRoot(ctx, CreateInput{
		UserID: h.userID, AgentID: h.agentID, Title: "context title", Intent: "context intent",
		Kind: KindComposite, Required: true, Contract: AcceptanceContract{}, Context: contextJSON,
	})
	if err != nil {
		t.Fatalf("CreateRoot: %v", err)
	}
	att, err := h.svc.BeginAutoDecomposition(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("BeginAutoDecomposition: %v", err)
	}
	var input AttemptInput
	if err := unmarshalJSON(att.InputContext, &input); err != nil {
		t.Fatalf("decode input_context: %v", err)
	}
	var gotContext, wantContext map[string]string
	if err := json.Unmarshal(input.Context, &gotContext); err != nil {
		t.Fatalf("decode input context: %v", err)
	}
	if err := json.Unmarshal(contextJSON, &wantContext); err != nil {
		t.Fatalf("decode want context: %v", err)
	}
	if input.Title != "context title" || input.Intent != "context intent" || gotContext["note"] != wantContext["note"] {
		t.Fatalf("input envelope title=%q intent=%q context=%s", input.Title, input.Intent, input.Context)
	}
}
