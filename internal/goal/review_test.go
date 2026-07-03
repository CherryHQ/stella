package goal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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
	if err := h.worker.Run(ctx, goalID, att.ID, Actor{Type: ActorAgent}); err != nil {
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
	if accepted.Lifecycle != LifecycleDone {
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
	if got.Lifecycle != LifecyclePending {
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
		return ExecutorResult{Failed: true, FailReason: "reviewer crashed", FailureClass: FailureClassFlaky}, nil
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
	if got := h.get(d.ID); got.Lifecycle != LifecycleDone {
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

// submitSyntheticExec injects a freshly-submitted execution attempt carrying the
// given output hash, so evaluatedAttempt (most-recent submitted execution) now
// resolves to it — simulating a rework that landed a new output while a prior
// review was mid-flight.
func (h *harness) submitSyntheticExec(goalID, hash string) {
	h.t.Helper()
	ctx := context.Background()
	d := h.get(goalID)
	maxNo, err := h.q.GetMaxAttemptNo(ctx, sqlc.GetMaxAttemptNoParams{GoalID: goalID, Purpose: PurposeExecution})
	if err != nil {
		h.t.Fatalf("max exec no: %v", err)
	}
	sid, err := h.sessionMinter()(ctx, d.UserID, d.AgentID, d.ProjectID.String)
	if err != nil {
		h.t.Fatalf("mint synthetic exec session: %v", err)
	}
	att, err := h.q.CreateAttempt(ctx, sqlc.CreateAttemptParams{
		ID:              newID(),
		GoalID:          goalID,
		UserID:          d.UserID,
		AgentID:         pgnull.Text(d.AgentID),
		ExecutorAgentID: pgnull.Text(d.AgentID),
		SessionID:       sid,
		Purpose:         PurposeExecution,
		AttemptNo:       maxNo + 1,
		Status:          AttemptQueued,
		InputContext:    marshalJSON(AttemptInput{}),
	})
	if err != nil {
		h.t.Fatalf("create synthetic exec: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		h.t.Fatalf("promote synthetic exec: %v", err)
	}
	if _, err := h.q.SubmitAttempt(ctx, sqlc.SubmitAttemptParams{
		Evidence: emptyJSON,
		Output:   marshalJSON(AttemptOutput{Summary: hash, Hash: hash}),
		ID:       att.ID,
	}); err != nil {
		h.t.Fatalf("submit synthetic exec: %v", err)
	}
}

// TestReview_StaleVerdictDoesNotAccept proves a verdict scoped to a now-superseded
// execution output never accepts the goal: if the evaluated output moves between a
// review's mint and its submit (a rework landed a new attempt while the reviewer
// ran), SubmitReview finalizes the review but appends no verdict and does not
// fold. The goal stays blocked(needs_verdict) for a fresh review against the new
// output — it must not pass the new output on a verdict that judged the old one.
func TestReview_StaleVerdictDoesNotAccept(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID) // exec H1 -> blocked(needs_verdict)

	ctx := context.Background()
	att, err := h.svc.BeginReview(ctx, d.ID, nil) // frozen on exec H1
	if err != nil {
		t.Fatalf("begin review: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("promote review: %v", err)
	}
	// A newer execution output lands before the reviewer submits.
	h.submitSyntheticExec(d.ID, "H2")

	// The reviewer (still judging H1) returns a pass; it must be discarded.
	if err := h.svc.SubmitReview(ctx, att.ID, AttemptEvidence{Summary: "ok"},
		[]ReviewVerdict{{ItemID: "review", Pass: true, Rationale: "lgtm"}}); err != nil {
		t.Fatalf("submit stale review: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle == LifecycleDone {
		t.Fatalf("stale verdict accepted the goal against a superseded output; want still blocked")
	}
}

// TestReview_BudgetResetsPerEpisode proves the review budget is per needs_verdict
// episode, not per goal lifetime: a healthy reviewer that fails two outputs (each
// reworked) must still review the third. With a cumulative budget the dispatcher
// would refuse the third review and wrongly degrade a working reviewer to a human.
func TestReview_BudgetResetsPerEpisode(t *testing.T) {
	h := newHarness(t)
	var execN int
	h.exec.fn = func(req ExecutorRequest) (ExecutorResult, error) {
		if req.Attempt.Purpose == PurposeReview {
			hash := ""
			if req.Input.ReviewOutput != nil {
				hash = req.Input.ReviewOutput.Hash
			}
			pass := hash == "H3" // accept only the third episode's output
			var vs []ReviewVerdict
			for _, it := range req.Input.ReviewItems {
				vs = append(vs, ReviewVerdict{ItemID: it.ID, Pass: pass, Rationale: "r"})
			}
			return ExecutorResult{Submitted: true, Evidence: AttemptEvidence{Summary: "reviewed"}, Verdicts: vs}, nil
		}
		execN++
		return ExecutorResult{Submitted: true, Evidence: AttemptEvidence{Summary: "done"}, Output: AttemptOutput{Summary: "done", Hash: fmt.Sprintf("H%d", execN)}}, nil
	}
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)

	for ep := 1; ep <= 2; ep++ {
		h.runLeaf(d.ID) // new output Hep
		h.runReview(d.ID)
		if got := h.get(d.ID); got.Lifecycle != LifecyclePending {
			t.Fatalf("episode %d lifecycle=%q want ready (fail verdict reworks)", ep, got.Lifecycle)
		}
	}
	// Episode 3: a cumulative budget (the bug) would have been spent by now and
	// BeginReview inside runReview would fail; a per-episode budget reviews freely.
	h.runLeaf(d.ID) // H3
	h.runReview(d.ID)
	if got := h.get(d.ID); got.Lifecycle != LifecycleDone {
		t.Fatalf("episode 3 lifecycle=%q want accepted (budget must reset per episode)", got.Lifecycle)
	}
}

// TestReview_QueuedReapDoesNotChargeBudget proves a review attempt reaped before
// it ever ran (queued backpressure) does not consume the episode budget — only
// attempts that actually ran do (started_at set), mirroring the queued
// decomposition refund. Otherwise River queue lag would degrade a goal to a human
// without a single reviewer turn.
func TestReview_QueuedReapDoesNotChargeBudget(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)

	ctx := context.Background()
	for i := range defaultMaxReviewAttempts {
		att, err := h.svc.BeginReview(ctx, d.ID, nil) // queued
		if err != nil {
			t.Fatalf("begin review %d: %v", i, err)
		}
		if err := h.svc.ReapAttempt(ctx, att.ID); err != nil { // reaped while queued
			t.Fatalf("reap %d: %v", i, err)
		}
	}
	// Budget intact: a real review still mints, runs, and accepts.
	h.runReview(d.ID)
	if got := h.get(d.ID); got.Lifecycle != LifecycleDone {
		t.Fatalf("after %d queued reaps lifecycle=%q want accepted (queued reap must not charge)", defaultMaxReviewAttempts, got.Lifecycle)
	}
}

// TestReview_SubmitRejectsUncoveredVerdicts proves coverage is re-validated at the
// durable boundary, not only in the tool: SubmitReview refuses a verdict set that
// names an unknown item / misses a required one, so no partial or bogus ledger is
// written even if a non-tool executor bypasses the in-turn check.
func TestReview_SubmitRejectsUncoveredVerdicts(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)

	ctx := context.Background()
	att, err := h.svc.BeginReview(ctx, d.ID, nil)
	if err != nil {
		t.Fatalf("begin review: %v", err)
	}
	if err := h.svc.SubmitReview(ctx, att.ID, AttemptEvidence{},
		[]ReviewVerdict{{ItemID: "bogus", Pass: true}}); err == nil {
		t.Fatalf("SubmitReview accepted an unknown-item verdict; want rejection")
	}
	if got := h.get(d.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
		t.Fatalf("after rejected verdict lifecycle=%q reason=%q want still blocked/needs_verdict", got.Lifecycle, got.BlockReason)
	}
}

// TestReview_ContractRetypeDropsVerdict proves a goal edit that retypes a frozen
// review item invalidates an in-flight agent verdict: the fold resolves verdicts
// against the LIVE contract by item id alone, so without the under-lock recheck an
// agent verdict could satisfy a gate the user just changed to authority=human.
// SubmitReview finalizes the attempt but appends nothing and does not fold.
func TestReview_ContractRetypeDropsVerdict(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID) // exec H1 -> blocked(needs_verdict)

	ctx := context.Background()
	att, err := h.svc.BeginReview(ctx, d.ID, nil) // frozen: item "review" authority=agent
	if err != nil {
		t.Fatalf("begin review: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("promote review: %v", err)
	}
	// The user retypes the same item id to authority=human while the reviewer runs.
	cur := h.get(d.ID)
	retyped := AcceptanceContract{Policy: PolicyDetThenJudgment, Items: []AcceptanceItem{
		{ID: "review", Kind: ItemJudgment, Required: true, Authority: AuthorityHuman, Prompt: "is it good?"},
	}}
	if err := h.q.UpdateGoalIntent(ctx, sqlc.UpdateGoalIntentParams{
		Title:              cur.Title,
		Intent:             cur.Intent,
		AcceptanceContract: marshalJSON(retyped),
		ConvergencePolicy:  cur.ConvergencePolicy,
		ReviewPolicy:       cur.ReviewPolicy,
		Priority:           cur.Priority,
		ID:                 d.ID,
	}); err != nil {
		t.Fatalf("retype contract: %v", err)
	}
	// The agent verdict must not satisfy the now-human gate.
	if err := h.svc.SubmitReview(ctx, att.ID, AttemptEvidence{Summary: "ok"},
		[]ReviewVerdict{{ItemID: "review", Pass: true, Rationale: "lgtm"}}); err != nil {
		t.Fatalf("submit review after retype: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle == LifecycleDone {
		t.Fatalf("stale agent verdict satisfied a retyped human gate; want still blocked")
	}
}

// TestReview_DropsVerdictWhenAlreadyResolved proves the under-lock lifecycle
// recheck drops a late reviewer verdict for a goal that already left
// blocked(needs_verdict): a human verdict accepts the goal before the reviewer
// submits, so a (fail) verdict folded in now would wrongly rework an accepted
// goal. SubmitReview is a clean no-op (no error), and the goal stays accepted.
func TestReview_DropsVerdictWhenAlreadyResolved(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)

	ctx := context.Background()
	att, err := h.svc.BeginReview(ctx, d.ID, nil)
	if err != nil {
		t.Fatalf("begin review: %v", err)
	}
	if _, err := h.q.PromoteAttempt(ctx, sqlc.PromoteAttemptParams{ID: att.ID}); err != nil {
		t.Fatalf("promote review: %v", err)
	}
	// A human verdict resolves (accepts) the goal before the reviewer submits.
	if err := h.svc.SubmitVerdict(ctx, VerdictInput{
		GoalID: d.ID, ItemID: "review", Result: ResultPass, ScopeHash: "H1", ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("human verdict: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle != LifecycleDone {
		t.Fatalf("precondition: human verdict should have accepted, got %q", got.Lifecycle)
	}
	// The late reviewer submit (a fail) must be dropped, not fold into a rework.
	if err := h.svc.SubmitReview(ctx, att.ID, AttemptEvidence{Summary: "ok"},
		[]ReviewVerdict{{ItemID: "review", Pass: false, Rationale: "reject"}}); err != nil {
		t.Fatalf("submit review after resolved: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle != LifecycleDone {
		t.Fatalf("late verdict moved a resolved goal lifecycle=%q want accepted", got.Lifecycle)
	}
}

// setupCompositeNeedsAgentVerdict builds a composite with an agent-judgment
// contract, decomposes it into one required leaf, runs that leaf to accepted, and
// applies the rollup so the composite parks at blocked(needs_verdict) — the state
// the dispatcher's composite auto-review picks up.
func setupCompositeNeedsAgentVerdict(t *testing.T, h *harness) sqlc.AgentGoal {
	t.Helper()
	root := h.createRoot(KindComposite, agentJudgmentContract())
	cmp_decompose(t, h, root.ID, DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true)},
	})
	cmp_acceptChild(t, h, childID(root.ID, "a"))
	if err := h.svc.RollupAccept(context.Background(), root.ID); err != nil {
		t.Fatalf("RollupAccept: %v", err)
	}
	got := h.get(root.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
		t.Fatalf("composite after rollup lifecycle=%q reason=%q want blocked/needs_verdict", got.Lifecycle, got.BlockReason)
	}
	return got
}

// TestReview_CompositeAgentVerdictAccepts proves the composite auto-review path:
// a composite whose agent-judgment gate parked it at needs_verdict is reviewed
// against its children's frozen accepted outputs (no execution attempt exists),
// and a passing verdict accepts it with those outputs carried on its own
// accepted_output — no human in the loop.
func TestReview_CompositeAgentVerdictAccepts(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "phase complete"))
	root := setupCompositeNeedsAgentVerdict(t, h)

	ctx := context.Background()
	att, err := h.svc.BeginReview(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("begin composite review: %v", err)
	}
	// The frozen review input judges the children evidence, not an execution attempt.
	var in AttemptInput
	if err := unmarshalJSON(att.InputContext, &in); err != nil {
		t.Fatalf("decode review input: %v", err)
	}
	if in.ReviewedAttemptID != "" {
		t.Fatalf("composite review frozen reviewed_attempt_id=%q want empty", in.ReviewedAttemptID)
	}
	if in.ReviewOutput == nil || in.ReviewOutput.Hash == "" || in.ReviewOutput.Result["children"] == nil {
		t.Fatalf("composite review froze no children evidence: %+v", in.ReviewOutput)
	}
	if err := h.worker.Run(ctx, root.ID, att.ID, Actor{Type: ActorAgent}); err != nil {
		t.Fatalf("worker run composite review: %v", err)
	}

	accepted := h.get(root.ID)
	if accepted.Lifecycle != LifecycleDone {
		t.Fatalf("after composite agent verdict lifecycle=%q want accepted", accepted.Lifecycle)
	}
	var ao AcceptedOutput
	if err := unmarshalNullJSON(accepted.AcceptedOutput, &ao); err != nil {
		t.Fatalf("decode accepted_output: %v", err)
	}
	if len(ao.Children) != 1 || ao.Children[0].Hash != "H1" {
		t.Fatalf("composite accepted_output children=%+v want the child's frozen H1 output", ao.Children)
	}
}

// TestReview_CompositeFailVerdictParksForHuman proves a failing agent verdict on
// a composite does NOT reopen it to ready (a composite is not claimable — that
// would zombie it): it parks back at blocked(needs_verdict) for a human, whose
// pass-verdict override then accepts it.
func TestReview_CompositeFailVerdictParksForHuman(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(false, "phase output inadequate"))
	root := setupCompositeNeedsAgentVerdict(t, h)
	h.runReview(root.ID)

	got := h.get(root.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
		t.Fatalf("after composite fail verdict lifecycle=%q reason=%q want blocked/needs_verdict (never ready)", got.Lifecycle, got.BlockReason)
	}
	// The failing item is resolved (valid fail), so no further agent review mints.
	if _, err := h.svc.BeginReview(context.Background(), root.ID, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("BeginReview after fail verdict err=%v want ErrInvalidTransition (human adjudicates)", err)
	}
	// The human overrides the agent's fail: a later pass verdict wins the fold.
	if err := h.svc.SubmitVerdict(context.Background(), VerdictInput{
		GoalID: root.ID, ItemID: "review", Result: ResultPass, ScopeHash: "", ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("human override verdict: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("after human override lifecycle=%q want accepted", got)
	}
}

// TestReview_CompositeHumanFailDoesNotZombie pins the same no-zombie guarantee on
// the human path: failing a composite's judgment item used to route through
// reopenForRework into 'ready', where nothing claims a composite. It must park at
// blocked(needs_verdict) instead.
func TestReview_CompositeHumanFailDoesNotZombie(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = lcl_passOutput("H1")
	root := h.createRoot(KindComposite, humanJudgmentContract())
	cmp_decompose(t, h, root.ID, DecompositionContent{
		Children: []ProposedChild{cmp_child("a", true)},
	})
	cmp_acceptChild(t, h, childID(root.ID, "a"))
	if err := h.svc.RollupAccept(context.Background(), root.ID); err != nil {
		t.Fatalf("RollupAccept: %v", err)
	}

	if err := h.svc.SubmitVerdict(context.Background(), VerdictInput{
		GoalID: root.ID, ItemID: "review", Result: ResultFail, Rationale: "not good enough",
		ScopeHash: "", ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("human fail verdict: %v", err)
	}
	got := h.get(root.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
		t.Fatalf("after human fail lifecycle=%q reason=%q want blocked/needs_verdict (not a ready zombie)", got.Lifecycle, got.BlockReason)
	}
}

// TestReview_CompositeBudgetDegradesToHuman proves the composite review budget
// (whole-history episode: the children evidence never moves) bounds a reviewer
// that cannot produce a verdict, then degrades to a human.
func TestReview_CompositeBudgetDegradesToHuman(t *testing.T) {
	h := newHarness(t)
	reviewerCrashes := func(_ ExecutorRequest) (ExecutorResult, error) {
		return ExecutorResult{Failed: true, FailReason: "reviewer crashed", FailureClass: FailureClassFlaky}, nil
	}
	h.exec.fn = reviewExec("H1", reviewerCrashes)
	root := setupCompositeNeedsAgentVerdict(t, h)

	for i := range defaultMaxReviewAttempts {
		h.runReview(root.ID)
		if got := h.get(root.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
			t.Fatalf("after failed composite review %d lifecycle=%q reason=%q want still blocked/needs_verdict", i, got.Lifecycle, got.BlockReason)
		}
	}
	if _, err := h.svc.BeginReview(context.Background(), root.ID, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("BeginReview after composite budget spent err=%v want ErrInvalidTransition", err)
	}
	// The human remains the final adjudicator.
	if err := h.svc.SubmitVerdict(context.Background(), VerdictInput{
		GoalID: root.ID, ItemID: "review", Result: ResultPass, ScopeHash: "", ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("human verdict after budget: %v", err)
	}
	if got := h.get(root.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("after human verdict lifecycle=%q want accepted", got)
	}
}

// fakeEnqueuer is a no-op goalEnqueuer for dispatcher scan tests: it records
// nothing and reports a successful (non-duplicate) insert so the claim/review tx
// commits without a real River client.
type fakeEnqueuer struct{}

func (fakeEnqueuer) InsertTx(_ context.Context, _ pgx.Tx, _ river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

// TestReview_RespectsConcurrencyCap proves scanAndReview honors the per-user
// concurrency cap: a user saturated by an inflight execution attempt gets no
// review minted for a blocked(needs_verdict) goal until a slot frees. Review
// attempts are LLM jobs, so they share the cap rather than bursting one per
// blocked goal.
func TestReview_RespectsConcurrencyCap(t *testing.T) {
	h := newHarness(t)
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID) // blocked(needs_verdict)

	ctx := context.Background()
	// Saturate the per-user cap with one inflight (queued) execution attempt on
	// another goal of the same user — never run, so it stays inflight.
	other := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(other.ID)
	if _, err := h.svc.Claim(ctx, other.ID, "w-1", nil); err != nil {
		t.Fatalf("claim other: %v", err)
	}

	fake := fakeEnqueuer{}
	overCap := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Enqueuer: fake, MaxConcurrentPerUser: 1, BatchLimit: 50,
	})
	overCap.scanAndReview(ctx, time.Time{})
	if _, err := h.q.GetActiveAttempt(ctx, sqlc.GetActiveAttemptParams{GoalID: d.ID, Purpose: PurposeReview}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("review minted while over per-user cap (err=%v); want none", err)
	}

	underCap := NewDispatcher(DispatcherConfig{
		Service: h.svc, Queries: h.q, Enqueuer: fake, MaxConcurrentPerUser: 10, BatchLimit: 50,
	})
	underCap.scanAndReview(ctx, time.Time{})
	if _, err := h.q.GetActiveAttempt(ctx, sqlc.GetActiveAttemptParams{GoalID: d.ID, Purpose: PurposeReview}); err != nil {
		t.Fatalf("review not minted under cap: %v", err)
	}
}

// TestReview_DisposesSessionOnRollback proves the session minted before
// BeginReview's tx is archived when that tx rolls back, so a lost race / collision
// does not orphan it. A failing enqueue forces the rollback deterministically
// (mint happens outside the tx; enqueue inside it).
func TestReview_DisposesSessionOnRollback(t *testing.T) {
	h := newHarness(t)
	// Record every minted session (the last one is the review session) and every
	// disposed session, replacing the minter/disposer on the service directly.
	base := h.sessionMinter()
	var minted []string
	h.svc.newSession = func(ctx context.Context, u, a, p string) (string, error) {
		id, err := base(ctx, u, a, p)
		if err == nil {
			minted = append(minted, id)
		}
		return id, err
	}
	var disposed []string
	h.svc.disposeSession = func(_ context.Context, _, _, sid string) error {
		disposed = append(disposed, sid)
		return nil
	}

	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID) // blocked(needs_verdict)

	ctx := context.Background()
	boom := errors.New("enqueue boom")
	if _, err := h.svc.BeginReview(ctx, d.ID, func(_ context.Context, _ pgx.Tx, _, _ string) error {
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("BeginReview err=%v want enqueue boom", err)
	}
	if len(disposed) != 1 {
		t.Fatalf("disposed %d sessions, want 1 (the rolled-back review session)", len(disposed))
	}
	if want := minted[len(minted)-1]; disposed[0] != want {
		t.Fatalf("disposed %q want the review session %q", disposed[0], want)
	}
}

// TestDisposeOnRollback pins the rollback gate: a session is archived only on a
// DEFINITE rollback. A committed flow (nil err) and an AMBIGUOUS commit failure
// (errTxCommit — the row may be live) are both left alone, so a commit blip can
// never archive a live session.
func TestDisposeOnRollback(t *testing.T) {
	var disposed []string
	svc := &GoalService{disposeSession: func(_ context.Context, _, _, sid string) error {
		disposed = append(disposed, sid)
		return nil
	}}
	ctx := context.Background()
	svc.disposeOnRollback(ctx, nil, "u", "a", "s1")                                 // committed: keep
	svc.disposeOnRollback(ctx, fmt.Errorf("%w: boom", errTxCommit), "u", "a", "s2") // ambiguous: keep
	svc.disposeOnRollback(ctx, errors.New("body boom"), "u", "a", "s3")             // rolled back: dispose
	if len(disposed) != 1 || disposed[0] != "s3" {
		t.Fatalf("disposed=%v want [s3] (only the definite rollback)", disposed)
	}
}

// TestReview_SuccessfulReviewKeepsSession proves the happy path never disposes: a
// full create -> run -> agent-review -> accept commits every tx, so no session is
// archived. Guards against a rollback-gate regression that would archive live
// sessions on success.
func TestReview_SuccessfulReviewKeepsSession(t *testing.T) {
	h := newHarness(t)
	var disposed []string
	h.svc.disposeSession = func(_ context.Context, _, _, sid string) error {
		disposed = append(disposed, sid)
		return nil
	}
	h.exec.fn = reviewExec("H1", reviewVerdict(true, "lgtm"))
	d := h.createRoot(KindLeaf, agentJudgmentContract())
	h.activate(d.ID)
	h.runLeaf(d.ID)
	h.runReview(d.ID)
	if got := h.get(d.ID); got.Lifecycle != LifecycleDone {
		t.Fatalf("want accepted, got %q", got.Lifecycle)
	}
	if len(disposed) != 0 {
		t.Fatalf("successful flow disposed %v; want none", disposed)
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
