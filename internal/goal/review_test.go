package goal

import (
	"context"
	"errors"
	"testing"
)

// agentJudgmentContract is a single required agent-verdict item — the gate the
// dispatcher resolves with an agent reviewer (contract §10.13) rather than a
// human, the mirror of humanJudgmentContract.
func agentJudgmentContract() AcceptanceContract {
	return AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items: []AcceptanceItem{
			{ID: "review", Kind: ItemJudgment, Required: true, Authority: AuthorityAgent, Rubric: "is it good?"},
		},
	}
}

// reviewExec scripts the shared executor: execution attempts submit a fixed-hash
// output; review attempts return a verdict (pass) for every item they were asked
// to judge. A test overrides the review branch to script a fail/no-verdict.
func reviewExec(outputHash string, review func(ExecutorRequest) (ExecutorResult, error)) func(ExecutorRequest) (ExecutorResult, error) {
	return func(req ExecutorRequest) (ExecutorResult, error) {
		if req.Attempt.Purpose == PurposeReview {
			return review(req)
		}
		return ExecutorResult{
			Submitted: true,
			Evidence:  AttemptEvidence{Summary: "done"},
			Output:    AttemptOutput{Summary: "done", Hash: outputHash},
		}, nil
	}
}

// reviewVerdict scripts a reviewer that returns the same pass/rationale for every
// pending item the attempt froze into its input.
func reviewVerdict(pass bool, rationale string) func(ExecutorRequest) (ExecutorResult, error) {
	return func(req ExecutorRequest) (ExecutorResult, error) {
		var vs []ReviewVerdict
		for _, it := range req.Input.ReviewItems {
			vs = append(vs, ReviewVerdict{ItemID: it.ID, Pass: pass, Rationale: rationale})
		}
		return ExecutorResult{Submitted: true, Evidence: AttemptEvidence{Summary: "reviewed"}, Verdicts: vs}, nil
	}
}

// runReview drives one agent-review episode: BeginReview mints + the worker runs
// the review attempt to its single SubmitReview/fail transition. Returns the
// review attempt id.
func (h *harness) runReview(goalID string) string {
	h.t.Helper()
	ctx := context.Background()
	att, err := h.svc.BeginReview(ctx, goalID, nil)
	if err != nil {
		h.t.Fatalf("begin review %s: %v", goalID, err)
	}
	if err := h.worker.Run(ctx, goalID, att.ID, Actor{Type: ActorReviewer}); err != nil {
		h.t.Fatalf("worker run review %s/%s: %v", goalID, att.ID, err)
	}
	return att.ID
}

// TestReview_AgentVerdictAccepts walks the full agent auto-review gate: a worker
// attempt blocks the leaf on needs_verdict, the dispatcher mints a review attempt,
// and a passing reviewer verdict (scope_hash = the evaluated output) accepts the
// goal — no human in the loop.
func TestReview_AgentVerdictAccepts(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)

	blocked := h.get(d.ID)
	if blocked.Lifecycle != LifecycleBlocked || blocked.BlockReason != BlockNeedsVerdict {
		t.Fatalf("after run lifecycle=%q reason=%q want blocked/needs_verdict", blocked.Lifecycle, blocked.BlockReason)
	}

	h.runReview(d.ID)

	accepted := h.get(d.ID)
	if accepted.Lifecycle != LifecycleAccepted {
		t.Fatalf("after agent verdict lifecycle=%q want accepted", accepted.Lifecycle)
	}
	var ao AcceptedOutput
	if !accepted.AcceptedOutput.Valid {
		t.Fatalf("accepted_output not frozen after agent verdict")
	}
	if err := unmarshalNullJSON(accepted.AcceptedOutput, &ao); err != nil {
		t.Fatalf("decode accepted_output: %v", err)
	}
	if ao.Hash != "H1" {
		t.Fatalf("accepted_output.hash=%q want H1", ao.Hash)
	}
}

// TestReview_AgentVerdictFailReworks proves a failing agent verdict reworks the
// goal (budget left): the leaf wakes blocked->active->ready for the next attempt
// with the reviewer rationale recorded as a gap, not parked for a human.
func TestReview_AgentVerdictFailReworks(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(false, "missing tests"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	execAttempt := h.runLeaf(d.ID)
	h.runReview(d.ID)

	got := h.get(d.ID)
	if got.Lifecycle != LifecycleReady {
		t.Fatalf("after fail verdict lifecycle=%q want ready (rework)", got.Lifecycle)
	}
	// The reviewer rationale rides as a gap on the rejected execution attempt.
	att, err := h.q.GetAttempt(context.Background(), execAttempt)
	if err != nil {
		t.Fatalf("get exec attempt: %v", err)
	}
	var ev Evaluation
	if err := unmarshalJSON(att.Gaps, &ev); err != nil {
		t.Fatalf("decode gaps: %v", err)
	}
	if len(ev.Gaps) == 0 {
		t.Fatalf("no gaps recorded on rejected attempt after fail verdict")
	}
}

// TestReview_BudgetDegradesToHuman proves a reviewer that cannot produce a
// verdict is retried within the small review budget, then degrades to a human:
// once the budget is spent BeginReview is a no-op (ErrInvalidTransition) and the
// goal stays blocked(needs_verdict) until a human verdict accepts it.
func TestReview_BudgetDegradesToHuman(t *testing.T) {
	h := newHarness(t)
	reviewerCrashes := func(_ ExecutorRequest) (ExecutorResult, error) {
		return ExecutorResult{Failed: true, FailReason: "reviewer crashed", Retryable: true}, nil
	}
	h.exec.fn = reviewExec("H1", reviewerCrashes)
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)

	// Burn the review budget: each attempt fails and leaves the goal blocked.
	for i := range defaultMaxReviewAttempts {
		h.runReview(d.ID)
		if got := h.get(d.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
			t.Fatalf("after failed review %d lifecycle=%q reason=%q want still blocked/needs_verdict", i, got.Lifecycle, got.BlockReason)
		}
	}

	// Budget spent: the dispatcher no longer mints a reviewer.
	if _, err := h.svc.BeginReview(context.Background(), d.ID, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("BeginReview after budget spent err=%v want ErrInvalidTransition", err)
	}

	// A human verdict still resolves it (human authority is the final override).
	if err := h.svc.SubmitVerdict(context.Background(), VerdictInput{
		GoalID:         d.ID,
		ItemID:         "review",
		Result:         ResultPass,
		ScopeHash:      "H1",
		ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("human verdict after budget: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle != LifecycleAccepted {
		t.Fatalf("after human verdict lifecycle=%q want accepted", got.Lifecycle)
	}
}

// TestReview_HumanOnlyNotReviewed proves a human-authority contract is left for a
// human: BeginReview finds no agent item and is a no-op (ErrInvalidTransition).
func TestReview_HumanOnlyNotReviewed(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = lcl_passOutput("H1")
	d := h.createRoot(KindLeaf, humanJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)

	if got := h.get(d.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
		t.Fatalf("after run lifecycle=%q reason=%q want blocked/needs_verdict", got.Lifecycle, got.BlockReason)
	}
	if _, err := h.svc.BeginReview(context.Background(), d.ID, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("BeginReview on human-only contract err=%v want ErrInvalidTransition", err)
	}
}

// TestReview_SkipsWhenInFlight proves the dispatcher does not re-mint while a
// review attempt is already queued/running. The goal stays blocked(needs_verdict)
// across the review, so scanAndReview lists it every tick; without the in-flight
// guard each tick would mint a throwaway session and collide on
// uniq_agent_goal_active_attempt. A second BeginReview must be a no-op.
func TestReview_SkipsWhenInFlight(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)

	// First BeginReview mints a queued review attempt (no worker run yet).
	if _, err := h.svc.BeginReview(context.Background(), d.ID, nil); err != nil {
		t.Fatalf("first BeginReview: %v", err)
	}
	// Second BeginReview sees the in-flight attempt and is a no-op.
	if _, err := h.svc.BeginReview(context.Background(), d.ID, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second BeginReview err=%v want ErrInvalidTransition", err)
	}
}

// TestValidateReviewVerdicts pins the in-turn coverage guard: a review must
// answer every required item exactly once and name no unknown item.
func TestValidateReviewVerdicts(t *testing.T) {
	items := []AcceptanceItem{{ID: "a"}, {ID: "b"}}
	cases := []struct {
		name     string
		verdicts []ReviewVerdict
		wantErr  bool
	}{
		{"complete", []ReviewVerdict{{ItemID: "a"}, {ItemID: "b"}}, false},
		{"missing", []ReviewVerdict{{ItemID: "a"}}, true},
		{"unknown", []ReviewVerdict{{ItemID: "a"}, {ItemID: "b"}, {ItemID: "c"}}, true},
		{"duplicate", []ReviewVerdict{{ItemID: "a"}, {ItemID: "a"}, {ItemID: "b"}}, true},
		{"empty id", []ReviewVerdict{{ItemID: ""}, {ItemID: "b"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReviewVerdicts(tc.verdicts, items)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateReviewVerdicts err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
