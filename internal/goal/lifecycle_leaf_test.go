package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// lifecycle_leaf_test.go exercises the leaf goal lifecycle through the
// worker path (Claim → run → Submit → fold) plus the verdict, budget, and
// manual-transition seams. Every assertion is written to the contract invariants
// embedded as §N comments in converge.go / service.go / acceptance.go, not to
// "whatever the code happens to do".

// ── scripted-executor helpers ───────────────────────────────────────────────

// lcl_passOutput scripts a successful submit with a DETERMINISTIC output hash, so
// a test can anchor a verdict's scope_hash to it (§4.2 — scope_hash must equal
// the evaluated output's Hash).
func lcl_passOutput(hash string) func(ExecutorRequest) (ExecutorResult, error) {
	return func(_ ExecutorRequest) (ExecutorResult, error) {
		return ExecutorResult{
			Submitted: true,
			Evidence:  AttemptEvidence{Summary: "done"},
			Output:    AttemptOutput{Summary: "done", Hash: hash},
		}, nil
	}
}

// lcl_fail scripts a reported executor failure (the worker's finalize-failed
// branch, §5 step 7).
func lcl_fail(reason string) func(ExecutorRequest) (ExecutorResult, error) {
	return lcl_failClass(reason, FailureClassModel, "")
}

func lcl_failClass(reason, failureClass, blockedBy string) func(ExecutorRequest) (ExecutorResult, error) {
	return func(_ ExecutorRequest) (ExecutorResult, error) {
		return ExecutorResult{Failed: true, FailReason: reason, FailureClass: failureClass, BlockedBy: blockedBy}, nil
	}
}

// ── deterministic-check rig (budget / rework paths) ─────────────────────────
//
// The worker-reported Failed path (lcl_fail) only finalizes the attempt and
// clears the active pointer — it leaves the goal 'active' and defers the
// budget branch to a dispatcher tick the harness does not run. To exercise the
// CONVERGENCE branch (reopen-for-rework vs blocked(budget_exhausted)) inside one
// runLeaf, acceptance must fold to FAILED, which needs a deterministic check
// event. The harness service has no CheckRunner, so these tests stand up a
// sibling service+worker over the SAME db/queries with a scripted CheckRunner.

// lcl_checkRunner returns a fold result driven by `pass`, flipped per-test like
// the scripted executor's fn.
type lcl_checkRunner struct{ pass bool }

func (r *lcl_checkRunner) Run(_ context.Context, item AcceptanceItem, _ CheckEnv, _ sandbox.Session) (CheckResult, error) {
	return CheckResult{ItemID: item.ID, Pass: r.pass}, nil
}

// lcl_rig bundles a check-running service+worker so a fold can reach FAILED.
type lcl_rig struct {
	svc    *GoalService
	worker *Worker
	checks *lcl_checkRunner
}

// lcl_newRig builds a service+worker sharing the harness db/queries with a
// controllable executor + check runner, plus the harness's FK-correct session
// minter so CreateRoot satisfies the session_id FK.
func lcl_newRig(h *harness) *lcl_rig {
	checks := &lcl_checkRunner{}
	svc := New(h.db, h.q,
		WithSessionMinter(h.sessionMinter()),
		WithExecutor(h.exec),
		WithCheckRunner(checks),
	)
	w := NewWorker(svc, h.q)
	w.SetHeartbeat(0)
	return &lcl_rig{svc: svc, worker: w, checks: checks}
}

// lcl_runLeaf drives one full worker attempt on the rig (Claim → run → fold).
func (r *lcl_rig) runLeaf(t *testing.T, id string) string {
	t.Helper()
	ctx := context.Background()
	att, err := r.svc.Claim(ctx, id, "w-1", nil)
	if err != nil {
		t.Fatalf("rig claim %s: %v", id, err)
	}
	if err := r.worker.Run(ctx, id, att.ID, Actor{Type: ActorWorker}); err != nil {
		t.Fatalf("rig run %s/%s: %v", id, att.ID, err)
	}
	return att.ID
}

// lcl_detLeaf mints a DRAFT leaf root with one required deterministic item (so a
// fold can reach passed/failed via the rig's CheckRunner) and an explicit
// convergence budget. Goes through CreateRoot for the session_id FK.
func lcl_detLeaf(h *harness, maxAttempts int) sqlc.AgentGoal {
	h.t.Helper()
	contract := AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items: []AcceptanceItem{
			{ID: "build", Kind: ItemDeterministic, Required: true, Command: "true"},
		},
	}
	d, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:      h.userID,
		AgentID:     h.agentID,
		Title:       "root",
		Intent:      "test goal",
		Kind:        KindLeaf,
		Required:    true,
		Contract:    contract,
		Convergence: ConvergencePolicy{MaxAttempts: maxAttempts},
	})
	if err != nil {
		h.t.Fatalf("lcl_detLeaf: %v", err)
	}
	return d
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestLcl_ActivateNonDraftIsInvalid asserts the plan gate is a draft→ready move
// only: re-activating a non-draft leaf returns ErrInvalidTransition (§2.1, the
// Activate guard d.Lifecycle != draft).
func TestLcl_ActivateNonDraftIsInvalid(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID) // draft → ready

	_, err := h.svc.Activate(context.Background(), d.ID)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("re-activate ready leaf err=%v want ErrInvalidTransition", err)
	}
	if got := h.get(d.ID).Lifecycle; got != LifecyclePending {
		t.Fatalf("after failed re-activate lifecycle=%q want ready (unchanged)", got)
	}
}

// TestLcl_TrivialLeafAccepts asserts the trivial (auto-accept) leaf path: ready →
// accepted on one worker attempt, with accepted_output FROZEN (§4.3 passed →
// acceptLeaf freezes the output, §3.5 immutable AcceptedOutput.Hash).
func TestLcl_TrivialLeafAccepts(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)
	h.exec.fn = lcl_passOutput("TRIV1")
	h.runLeaf(d.ID)

	got := h.get(d.ID)
	if got.Lifecycle != LifecycleDone {
		t.Fatalf("lifecycle=%q want accepted", got.Lifecycle)
	}
	// accepted_output is frozen on acceptance — a leaf has no decomposition plan
	// (that is the composite freeze: plan + planned_at); the leaf freeze is the output.
	if !got.AcceptedOutput.Valid || got.AcceptedOutput.String == "" {
		t.Fatalf("accepted_output not frozen: %+v", got.AcceptedOutput)
	}
	var ao AcceptedOutput
	if err := unmarshalNullJSON(got.AcceptedOutput, &ao); err != nil {
		t.Fatalf("decode accepted_output: %v", err)
	}
	if ao.Hash != "TRIV1" {
		t.Fatalf("accepted_output.hash=%q want TRIV1 (frozen submitted hash)", ao.Hash)
	}
	// The active attempt is cleared once the leaf accepts (no live episode).
	if got.ActiveAttemptID.Valid && got.ActiveAttemptID.String != "" {
		t.Fatalf("active_attempt_id=%q want cleared on accept", got.ActiveAttemptID.String)
	}
	if got.AcceptanceState != AcceptancePassed {
		t.Fatalf("acceptance_state=%q want passed", got.AcceptanceState)
	}
}

// TestLcl_HumanVerdictLeaf walks the full human-judgment gate (§4.2, §2.1
// blocked(needs_verdict)→accepted): a worker attempt blocks the leaf awaiting a
// verdict (active attempt cleared), a WRONG scope_hash verdict is ignored as
// stale, and a verdict whose scope_hash matches the evaluated output accepts.
func TestLcl_HumanVerdictLeaf(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, humanJudgmentContract())
	h.activate(d.ID)
	// Deterministic output hash so the verdict scope_hash can anchor to it (§4.2).
	h.exec.fn = lcl_passOutput("H1")
	h.runLeaf(d.ID)

	blocked := h.get(d.ID)
	if blocked.Lifecycle != LifecycleBlocked || blocked.BlockReason != BlockNeedsVerdict {
		t.Fatalf("after run lifecycle=%q reason=%q want blocked/needs_verdict", blocked.Lifecycle, blocked.BlockReason)
	}
	// A pending human is not an executing episode → active attempt cleared.
	if blocked.ActiveAttemptID.Valid && blocked.ActiveAttemptID.String != "" {
		t.Fatalf("active_attempt_id=%q want cleared while awaiting verdict", blocked.ActiveAttemptID.String)
	}

	// A verdict whose scope_hash equals the evaluated attempt's output hash accepts.
	if err := h.svc.SubmitVerdict(context.Background(), VerdictInput{
		GoalID:         d.ID,
		ItemID:         "review",
		Result:         ResultPass,
		ScopeHash:      "H1",
		ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("submit matching verdict: %v", err)
	}
	accepted := h.get(d.ID)
	if accepted.Lifecycle != LifecycleDone {
		t.Fatalf("after matching verdict lifecycle=%q want accepted", accepted.Lifecycle)
	}
	var ao AcceptedOutput
	if !accepted.AcceptedOutput.Valid {
		t.Fatalf("accepted_output not frozen after verdict accept")
	}
	if err := unmarshalNullJSON(accepted.AcceptedOutput, &ao); err != nil {
		t.Fatalf("decode accepted_output: %v", err)
	}
	if ao.Hash != "H1" {
		t.Fatalf("accepted_output.hash=%q want H1", ao.Hash)
	}

	// Stale-verdict guard (§4.2 verdictValid) on a FRESH goal: a PASS verdict
	// whose scope_hash does not equal the evaluated attempt's output hash is stale —
	// the fold drops it and the leaf stays blocked. (A separate goal because a
	// verdict is one-per-(attempt,item); in production a stale verdict only arises
	// against a superseded attempt, never a second verdict on the same artifact.)
	d2 := h.createRoot(KindLeaf, humanJudgmentContract())
	h.activate(d2.ID)
	h.exec.fn = lcl_passOutput("H2")
	h.runLeaf(d2.ID)
	if err := h.svc.SubmitVerdict(context.Background(), VerdictInput{
		GoalID:         d2.ID,
		ItemID:         "review",
		Result:         ResultPass,
		ScopeHash:      "STALE",
		ReviewerUserID: h.userID,
	}); err != nil {
		t.Fatalf("submit stale verdict: %v", err)
	}
	if got := h.get(d2.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockNeedsVerdict {
		t.Fatalf("after stale verdict lifecycle=%q reason=%q want still blocked/needs_verdict", got.Lifecycle, got.BlockReason)
	}
}

// TestLcl_VerdictReSubmitIsIdempotent guards the double-submit path: the
// acceptance ledger is append-only with a natural-key UNIQUE index
// (goal, attempt, item, cache_key), so a re-submitted identical verdict
// must be a no-op (ON CONFLICT DO NOTHING swallowed as pgx.ErrNoRows) and the
// re-fold of the now-accepted leaf must NOT raise ErrInvalidTransition — a
// double-clicked Approve should not 500. The leaf stays accepted with the same
// frozen output.
func TestLcl_VerdictReSubmitIsIdempotent(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, humanJudgmentContract())
	h.activate(d.ID)
	h.exec.fn = lcl_passOutput("H1")
	h.runLeaf(d.ID)

	verdict := VerdictInput{
		GoalID:         d.ID,
		ItemID:         "review",
		Result:         ResultPass,
		ScopeHash:      "H1",
		ReviewerUserID: h.userID,
	}
	if err := h.svc.SubmitVerdict(context.Background(), verdict); err != nil {
		t.Fatalf("first verdict: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle != LifecycleDone {
		t.Fatalf("after first verdict lifecycle=%q want accepted", got.Lifecycle)
	}

	// Re-submit the exact same verdict: idempotent no-op, never an error.
	if err := h.svc.SubmitVerdict(context.Background(), verdict); err != nil {
		t.Fatalf("duplicate verdict must be idempotent, got err=%v", err)
	}
	got := h.get(d.ID)
	if got.Lifecycle != LifecycleDone {
		t.Fatalf("after duplicate verdict lifecycle=%q want still accepted", got.Lifecycle)
	}
	// The ledger deduped the duplicate — only the single verdict event remains.
	events, err := h.q.ListAcceptanceEventByGoal(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var verdicts int
	for _, e := range events {
		if e.ItemKind == ItemJudgment {
			verdicts++
		}
	}
	if verdicts != 1 {
		t.Fatalf("judgment events=%d want 1 (duplicate must dedup)", verdicts)
	}
}

// TestLcl_FailWithBudgetRework asserts the rework-as-next-attempt loop (§5 step 7
// failed + budget left): a fold failure does NOT go terminal — it returns the
// leaf to ready for the next claim with gaps recorded and attempt_count bumped —
// and a later success accepts. Driven through a deterministic check so the fold
// reaches FAILED (vs the worker-reported failure path, which defers convergence
// to a dispatcher tick the harness does not run).
func TestLcl_FailWithBudgetRework(t *testing.T) {
	h := newHarness(t)
	rig := lcl_newRig(h)
	d := lcl_detLeaf(h, 3) // budget 3: one failure leaves room
	h.activate(d.ID)

	rig.checks.pass = false
	h.exec.fn = lcl_passOutput("OUT1")
	attID := rig.runLeaf(t, d.ID)

	afterFail := h.get(d.ID)
	if IsTerminalLifecycle(afterFail.Lifecycle) {
		t.Fatalf("after fold failure lifecycle=%q is terminal; budget remained, want recoverable", afterFail.Lifecycle)
	}
	// Budget left ⇒ reopen-for-rework: active pointer cleared, back to ready.
	if afterFail.Lifecycle != LifecyclePending {
		t.Fatalf("after fold failure lifecycle=%q want ready (rework = next attempt)", afterFail.Lifecycle)
	}
	if afterFail.ActiveAttemptID.Valid && afterFail.ActiveAttemptID.String != "" {
		t.Fatalf("active_attempt_id=%q want cleared after fold failure", afterFail.ActiveAttemptID.String)
	}
	if afterFail.AttemptCount != 1 {
		t.Fatalf("attempt_count=%d want 1 after one attempt", afterFail.AttemptCount)
	}
	if afterFail.AcceptanceState != AcceptanceFailed {
		t.Fatalf("acceptance_state=%q want failed", afterFail.AcceptanceState)
	}
	// The rejected attempt records its gaps so attempt_no+1 carries them (§5 step 7).
	failedAtt, err := h.q.GetAttempt(context.Background(), attID)
	if err != nil {
		t.Fatalf("get failed attempt: %v", err)
	}
	var gaps Evaluation
	if err := unmarshalJSON(failedAtt.Gaps, &gaps); err != nil {
		t.Fatalf("decode gaps: %v", err)
	}
	if len(gaps.Gaps) == 0 {
		t.Fatalf("rejected attempt recorded no gaps; want the unmet item")
	}

	// A later success accepts (attempt_no 2 carries on the same budget).
	rig.checks.pass = true
	h.exec.fn = lcl_passOutput("OUT2")
	rig.runLeaf(t, d.ID)
	got := h.get(d.ID)
	if got.Lifecycle != LifecycleDone {
		t.Fatalf("after retry success lifecycle=%q want accepted", got.Lifecycle)
	}
	if got.AttemptCount != 2 {
		t.Fatalf("attempt_count=%d want 2", got.AttemptCount)
	}
}

// TestLcl_BudgetExhaustedReattemptAbandon asserts the budget-out path (§5 step 7
// failed + budget out, default escalation=block, non-judgment-only contract): a
// single fold failure with MaxAttempts=1 parks the leaf blocked(budget_exhausted);
// Reattempt raises the budget and unblocks to ready; re-exhausting then Abandon
// terminates it.
func TestLcl_BudgetExhaustedReattemptAbandon(t *testing.T) {
	h := newHarness(t)
	rig := lcl_newRig(h)
	d := lcl_detLeaf(h, 1) // budget 1: one failure exhausts
	h.activate(d.ID)

	rig.checks.pass = false
	h.exec.fn = lcl_passOutput("X1")
	rig.runLeaf(t, d.ID)

	exhausted := h.get(d.ID)
	if exhausted.Lifecycle != LifecycleBlocked || exhausted.BlockReason != BlockBudgetExhausted {
		t.Fatalf("after exhausting budget lifecycle=%q reason=%q want blocked/budget_exhausted", exhausted.Lifecycle, exhausted.BlockReason)
	}

	// Reattempt raises max_attempts past attempt_count and unblocks to ready (§2.1).
	if err := h.svc.Reattempt(context.Background(), d.ID, UserActor(h.userID)); err != nil {
		t.Fatalf("reattempt: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle != LifecyclePending {
		t.Fatalf("after reattempt lifecycle=%q want ready", got.Lifecycle)
	}

	// Fail again to re-exhaust the raised budget, then Abandon → abandoned terminal.
	rig.checks.pass = false
	h.exec.fn = lcl_passOutput("X2")
	rig.runLeaf(t, d.ID)
	if got := h.get(d.ID); got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockBudgetExhausted {
		t.Fatalf("after second exhaustion lifecycle=%q reason=%q want blocked/budget_exhausted", got.Lifecycle, got.BlockReason)
	}
	if err := h.svc.Abandon(context.Background(), d.ID, "give up", UserActor(h.userID)); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	got := h.get(d.ID)
	if got.Lifecycle != LifecycleDone {
		t.Fatalf("after abandon lifecycle=%q want abandoned", got.Lifecycle)
	}
	if !IsTerminalLifecycle(got.Lifecycle) {
		t.Fatalf("abandoned must be terminal")
	}
}

// TestLcl_WorkerReportedFailureReopensToReady asserts a worker-reported Failed
// result routes through convergence (§5 step 7, issue #543): the attempt is
// finalized failed AND, with budget remaining, the goal returns to READY
// for the next claim — it does NOT strand 'active' with no live attempt. The old
// behavior (bare-clear the active pointer, leave it 'active') would hang forever
// because the dispatcher only re-claims 'ready' leaves.
func TestLcl_WorkerReportedFailureReopensToReady(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{}) // default budget 3
	h.activate(d.ID)

	h.exec.fn = lcl_fail("model failure")
	attID := h.runLeaf(d.ID)

	got := h.get(d.ID)
	if got.Lifecycle != LifecyclePending {
		t.Fatalf("after worker-reported failure lifecycle=%q want ready (reopened for rework, not stranded active)", got.Lifecycle)
	}
	if got.ActiveAttemptID.Valid && got.ActiveAttemptID.String != "" {
		t.Fatalf("active_attempt_id=%q want cleared after failed attempt", got.ActiveAttemptID.String)
	}
	if got.AttemptCount != 1 {
		t.Fatalf("attempt_count=%d want 1 after one claimed attempt", got.AttemptCount)
	}
	att, err := h.q.GetAttempt(context.Background(), attID)
	if err != nil {
		t.Fatalf("get failed attempt: %v", err)
	}
	if att.Status != AttemptFailed {
		t.Fatalf("attempt status=%q want failed", att.Status)
	}
}

// TestLcl_WorkerReportedFailureBudgetOutBlocks asserts the budget-out side of the
// same path (issue #543): a worker-reported failure on a MaxAttempts=1 leaf has no
// rework budget, so convergence parks it blocked(budget_exhausted) — visible to a
// human — rather than stranding it 'active'.
func TestLcl_ResponsibilityFailureRoutes(t *testing.T) {
	cases := []struct {
		name       string
		class      string
		blockedBy  string
		wantLife   string
		wantReason string
	}{
		{name: "model", class: FailureClassModel, wantLife: LifecyclePending},
		{name: "environment", class: FailureClassEnvironment, blockedBy: BlockEnvUnavailable, wantLife: LifecycleBlocked, wantReason: BlockEnvUnavailable},
		{name: "contract", class: FailureClassContract, blockedBy: BlockContractConflict, wantLife: LifecycleBlocked, wantReason: BlockContractConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			d := h.createRoot(KindLeaf, AcceptanceContract{})
			h.activate(d.ID)
			h.exec.fn = lcl_failClass(tc.name+" failure", tc.class, tc.blockedBy)
			h.runLeaf(d.ID)

			got := h.get(d.ID)
			if got.Lifecycle != tc.wantLife || got.BlockReason != tc.wantReason {
				t.Fatalf("goal=(%s,%s) want (%s,%s)", got.Lifecycle, got.BlockReason, tc.wantLife, tc.wantReason)
			}
			remaining := remainingBudget(t, h, got)
			if tc.class == FailureClassModel {
				if remaining != defaultMaxAttempts-1 {
					t.Fatalf("model remaining=%d want %d", remaining, defaultMaxAttempts-1)
				}
			} else if remaining != defaultMaxAttempts {
				t.Fatalf("%s remaining=%d want unchanged %d", tc.name, remaining, defaultMaxAttempts)
			}
		})
	}
}

func TestLcl_FlakyFailureRetriesOutsideBusinessBudgetThenBlocksEnvironment(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)
	h.exec.fn = lcl_failClass("network timeout", FailureClassFlaky, "")

	for i := 1; i <= 5; i++ {
		h.runLeaf(d.ID)
		got := h.get(d.ID)
		if got.Lifecycle != LifecyclePending || got.FlakyCount != int64(i) {
			t.Fatalf("after flaky %d goal=(%s flaky=%d) want ready flaky=%d", i, got.Lifecycle, got.FlakyCount, i)
		}
		if remaining := remainingBudget(t, h, got); remaining != defaultMaxAttempts {
			t.Fatalf("after flaky %d remaining=%d want unchanged %d", i, remaining, defaultMaxAttempts)
		}
	}

	h.runLeaf(d.ID)
	got := h.get(d.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockEnvUnavailable || got.FlakyCount != 6 {
		t.Fatalf("after flaky limit goal=(%s,%s flaky=%d) want blocked/env_unavailable flaky=6", got.Lifecycle, got.BlockReason, got.FlakyCount)
	}
	if remaining := remainingBudget(t, h, got); remaining != defaultMaxAttempts {
		t.Fatalf("after flaky limit remaining=%d want unchanged %d", remaining, defaultMaxAttempts)
	}
}

func remainingBudget(t *testing.T, h *harness, d sqlc.AgentGoal) int {
	t.Helper()
	spent, err := h.svc.spentAttemptBudget(context.Background(), h.q, d.ID, PurposeExecution)
	if err != nil {
		t.Fatalf("count billable attempts: %v", err)
	}
	return effectiveAttemptBudget(d) - spent
}

func TestLcl_WorkerReportedFailureBudgetOutBlocks(t *testing.T) {
	h := newHarness(t)
	d, err := h.svc.CreateRoot(context.Background(), CreateInput{
		UserID:      h.userID,
		AgentID:     h.agentID,
		Title:       "root",
		Intent:      "budget 1",
		Kind:        KindLeaf,
		Required:    true,
		Contract:    AcceptanceContract{},
		Convergence: ConvergencePolicy{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("createRoot: %v", err)
	}
	h.activate(d.ID)

	h.exec.fn = lcl_fail("boom")
	h.runLeaf(d.ID)

	got := h.get(d.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockBudgetExhausted {
		t.Fatalf("after budget-out failure lifecycle=%q reason=%q want blocked/budget_exhausted", got.Lifecycle, got.BlockReason)
	}
}

// TestLcl_MissingCheckRunnerBlocksEnvironment asserts the deterministic-strand
// guard (issue #543): a leaf with a REQUIRED deterministic item run by a service
// with NO CheckRunner cannot evaluate the gate, so the worker classifies the
// attempt as environment-owned and blocks without charging business budget. (The
// base harness service wires no CheckRunner; lcl_newRig is the one that does.)
func TestLcl_MissingCheckRunnerBlocksEnvironment(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{
		Policy: PolicyDetThenJudgment,
		Items: []AcceptanceItem{
			{ID: "build", Kind: ItemDeterministic, Required: true, Command: "true"},
		},
	})
	h.activate(d.ID)

	// Default scripted executor submits an output; with no CheckRunner the required
	// deterministic item can never produce an event.
	attID := h.runLeaf(d.ID)

	got := h.get(d.ID)
	if got.Lifecycle != LifecycleBlocked || got.BlockReason != BlockEnvUnavailable {
		t.Fatalf("missing-check-runner goal=(%s,%s) want blocked/env_unavailable", got.Lifecycle, got.BlockReason)
	}
	if remaining := remainingBudget(t, h, got); remaining != defaultMaxAttempts {
		t.Fatalf("missing-check-runner remaining=%d want unchanged %d", remaining, defaultMaxAttempts)
	}
	att, err := h.q.GetAttempt(context.Background(), attID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if att.Status != AttemptFailed || att.FailureClass != FailureClassEnvironment {
		t.Fatalf("attempt=(%s,%s) want failed/environment", att.Status, att.FailureClass)
	}
}

// TestLcl_CancelActiveLeaf asserts a manual cancel on an in-flight (active) leaf
// terminates it: cancelled is terminal (§6 cancel cascade / §2.1). The leaf is
// claimed (active) but not run, so a live attempt exists when Cancel fires.
func TestLcl_CancelActiveLeaf(t *testing.T) {
	h := newHarness(t)
	d := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(d.ID)

	att, err := h.svc.Claim(context.Background(), d.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got := h.get(d.ID); got.Lifecycle != LifecycleActive {
		t.Fatalf("after claim lifecycle=%q want active", got.Lifecycle)
	}

	if err := h.svc.Cancel(context.Background(), d.ID, "no longer needed", UserActor(h.userID)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got := h.get(d.ID)
	if got.Lifecycle != LifecycleDone {
		t.Fatalf("after cancel lifecycle=%q want cancelled", got.Lifecycle)
	}
	if !IsTerminalLifecycle(got.Lifecycle) {
		t.Fatalf("cancelled must be terminal")
	}
	// Cancel finalizes the in-flight attempt (§6 cancel in-flight attempts).
	finalized, err := h.q.GetAttempt(context.Background(), att.ID)
	if err != nil {
		t.Fatalf("get cancelled attempt: %v", err)
	}
	if finalized.Status != AttemptCancelled {
		t.Fatalf("in-flight attempt status=%q want cancelled", finalized.Status)
	}
}

// TestClaimEnqueueAtomic asserts the River Phase 2c invariant: claim and durable
// enqueue commit together. A failing enqueue must roll the whole claim back — no
// attempt row, goal still ready — so a claimed attempt is never stranded without a
// job. A succeeding enqueue commits the claim and the attempt.
func TestClaimEnqueueAtomic(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	t.Run("enqueue failure rolls the claim back", func(t *testing.T) {
		d := h.createRoot(KindLeaf, AcceptanceContract{})
		h.activate(d.ID)

		boom := errors.New("enqueue boom")
		var gotTx bool
		fail := AttemptEnqueuer(func(_ context.Context, tx pgx.Tx, _, _ string) error {
			gotTx = tx != nil // the enqueue runs inside the claim tx
			return boom
		})

		_, err := h.svc.Claim(ctx, d.ID, "w-1", fail)
		if !errors.Is(err, boom) {
			t.Fatalf("Claim err = %v, want wrapped %v", err, boom)
		}
		if !gotTx {
			t.Fatal("enqueue hook was not handed the claim tx")
		}
		// The mintNextAttempt write ran before the enqueue error, so this proves the
		// rollback: no attempt persisted and the goal is still ready, not active.
		if atts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: d.ID}); err != nil {
			t.Fatalf("list attempts: %v", err)
		} else if len(atts) != 0 {
			t.Fatalf("attempts after rolled-back claim = %d, want 0", len(atts))
		}
		if got := h.get(d.ID); got.Lifecycle != LifecyclePending || got.ActiveAttemptID.Valid {
			t.Fatalf("goal after rolled-back claim = (%s, active=%v), want (ready, false)",
				got.Lifecycle, got.ActiveAttemptID.Valid)
		}
	})

	t.Run("enqueue success commits the claim", func(t *testing.T) {
		d := h.createRoot(KindLeaf, AcceptanceContract{})
		h.activate(d.ID)

		ok := AttemptEnqueuer(func(_ context.Context, _ pgx.Tx, _, _ string) error { return nil })
		att, err := h.svc.Claim(ctx, d.ID, "w-1", ok)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if atts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: d.ID}); err != nil {
			t.Fatalf("list attempts: %v", err)
		} else if len(atts) != 1 || atts[0].ID != att.ID {
			t.Fatalf("attempts after committed claim = %d, want 1 (%s)", len(atts), att.ID)
		}
		if got := h.get(d.ID); got.Lifecycle != LifecycleActive {
			t.Fatalf("goal after committed claim = %s, want active", got.Lifecycle)
		}
	})
}

// TestLcl_QueuedReapDoesNotChargeBudget pins CR-001: an attempt reaped while still
// 'queued' (its River job sat behind the queue's MaxWorkers and the claim-grace
// lease expired before any PromoteAttempt — queue backpressure) never executed, so
// it must NOT consume convergence budget. Before the fix, ClaimGoal's attempt_count
// bump charged budget at claim time, so a wide-fanout root would park at
// blocked(budget_exhausted) having executed nothing.
func TestLcl_QueuedReapDoesNotChargeBudget(t *testing.T) {
	h := newHarness(t)
	rig := lcl_newRig(h)
	d := lcl_detLeaf(h, 1) // budget 1: a single mischarge would exhaust
	h.activate(d.ID)

	ctx := context.Background()
	// Sustained backpressure: each claim mints a 'queued' attempt that never gets a
	// worker, so the reaper finalizes it before any PromoteAttempt. Repeat past the
	// budget; every cycle must return the goal to ready, never blocked.
	for i := range 3 {
		att, err := h.svc.Claim(ctx, d.ID, "w-1", nil)
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if att.Status != AttemptQueued {
			t.Fatalf("claim %d status=%q want queued", i, att.Status)
		}
		if err := h.svc.ReapAttempt(ctx, att.ID); err != nil {
			t.Fatalf("reap %d: %v", i, err)
		}
		if got := h.get(d.ID); got.Lifecycle != LifecyclePending {
			t.Fatalf("after queued reap %d lifecycle=%q reason=%q want ready (never-run attempt must not charge budget)", i, got.Lifecycle, got.BlockReason)
		}
	}

	// Budget survived: a real attempt still runs to acceptance on the original budget.
	rig.checks.pass = true
	h.exec.fn = lcl_passOutput("REAP-OK")
	rig.runLeaf(t, d.ID)
	if got := h.get(d.ID).Lifecycle; got != LifecycleDone {
		t.Fatalf("after real run lifecycle=%q want accepted (queued reaps did not consume budget)", got)
	}
}
