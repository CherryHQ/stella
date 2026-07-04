package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// converge.go is the convergence orchestration seam (contract §5): it turns a
// ready leaf into a queued attempt with a FROZEN input context, and it maps a
// submitted attempt's acceptance fold to exactly one lifecycle transition. All
// durable writes stay in the service (each in one withTx); this file is the glue
// the dispatcher/worker call.

// ── Input context (frozen at mint, contract §3.3, §5 step 3) ────────────────

// buildInputContext assembles the AttemptInput frozen into an attempt's
// input_context at mint (contract §5 step 3). It is PURE: intent + only the
// ACCEPTED upstream outputs + the prior fold's gaps + the contract + a resolved
// verdict, stamped with attempt_no. Freezing here is what keeps an in-flight
// edit to intent/contract from mutating a running attempt's context.
func buildInputContext(d sqlc.AgentGoal, upstream []AcceptedOutput, priorGaps *Evaluation, timeline []TimelineContextEvent, resolvedVerdict string, attemptNo int) AttemptInput {
	var c AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &c)
	return AttemptInput{
		Title:           d.Title,
		Intent:          d.Intent,
		Context:         d.Context,
		UpstreamOutputs: upstream,
		PriorGaps:       priorGaps,
		TimelineContext: timeline,
		Contract:        c,
		ResolvedVerdict: resolvedVerdict,
		AttemptNo:       attemptNo,
	}
}

// upstreamAcceptedOutputs reads a goal's edges and returns the frozen
// AcceptedOutput of every upstream that is already accepted (only accepted
// output flows downstream, §3.3). A non-accepted upstream contributes nothing —
// the readiness gate, not this collector, decides whether a missing upstream
// blocks the claim.
func upstreamAcceptedOutputs(ctx context.Context, q *sqlc.Queries, goalID string) ([]AcceptedOutput, error) {
	edges, err := q.ListEdgeWithUpstreamState(ctx, goalID)
	if err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}
	var outs []AcceptedOutput
	for _, e := range edges {
		if e.UpstreamLifecycle != LifecycleDone || !e.UpstreamOutput.Valid {
			continue
		}
		var ao AcceptedOutput
		if err := unmarshalNullJSON(e.UpstreamOutput, &ao); err != nil {
			continue
		}
		outs = append(outs, ao)
	}
	return outs, nil
}

// priorGapsFor returns recent failed acceptance items from the goal timeline as
// the next attempt's gap context. The attempt history remains the audit source,
// but job input now reads the L3 timeline instead of stitching attempt rows.
func (s *GoalService) priorGapsFor(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) (*Evaluation, error) {
	if d.AttemptCount == 0 {
		return nil, nil
	}
	return s.priorGapsFromTimeline(ctx, q, d.ID)
}

// ── Mint next attempt (contract §5 step 1) ──────────────────────────────────

// Claim is the dispatcher's leaf pending->active (contract §2.1, §5 step 1). It
// resolves the dispatch hint's executor, mints a queued execution attempt and
// claims the goal in one tx. The per-root/per-user concurrency caps are
// enforced by the dispatcher BEFORE Claim (§5 step 2); Claim itself only guards
// the single-writer invariants.
//
// enqueue (River Phase 2c) inserts the attempt's durable execution job in the
// SAME tx, so the claim and its job commit atomically — a crash can no longer
// leave a claimed attempt with no job to run it. A nil enqueue skips this (tests
// minting+claiming without River); its failure rolls the claim back, leaving the
// goal pending for the next tick.
func (s *GoalService) Claim(ctx context.Context, id, workerID string, enqueue AttemptEnqueuer) (sqlc.AgentGoalAttempt, error) {
	// Execution attempts are one-shot task sessions. Queued attempts are executed
	// by the current executor version; running attempts owned by an old process are
	// recovered by the existing lease reaper, exactly like a process crash.
	d, err := getGoal(ctx, s.q, id)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}
	if d.Kind != KindLeaf || d.Lifecycle != LifecyclePending || d.ActiveAttemptID.Valid {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition
	}
	executorAgentID := dispatchExecutor(d)
	mintAgentID := executorAgentID
	if mintAgentID == "" {
		mintAgentID = d.AgentID
	}
	if s.newSession == nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("goal: no worker session minter configured")
	}
	sessionID, err := s.newSession(ctx, d.UserID, mintAgentID, d.ProjectID.String)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("mint attempt session: %w", err)
	}

	out, err := s.beginAttempt(ctx, id, attemptSpec{
		purpose:       PurposeExecution,
		sessionID:     sessionID,
		executorAgent: executorAgentID,
		lease:         nullTime(s.nowTime().Add(claimGraceTTL)),
		enqueue:       enqueue,
		prepare: func(ctx context.Context, q *sqlc.Queries, cur sqlc.AgentGoal, attemptNo int) (AttemptInput, error) {
			if cur.Kind != KindLeaf || cur.Lifecycle != LifecyclePending || cur.ActiveAttemptID.Valid || dispatchExecutor(cur) != executorAgentID {
				return AttemptInput{}, ErrInvalidTransition
			}
			upstream, err := upstreamAcceptedOutputs(ctx, q, cur.ID)
			if err != nil {
				return AttemptInput{}, err
			}
			prior, err := s.priorGapsFor(ctx, q, cur)
			if err != nil {
				return AttemptInput{}, err
			}
			timeline, err := s.recentTimelineContext(ctx, q, cur.ID)
			if err != nil {
				return AttemptInput{}, err
			}
			return buildInputContext(cur, upstream, prior, timeline, "", attemptNo), nil
		},
		transition: func(ctx context.Context, q *sqlc.Queries, cur sqlc.AgentGoal, att sqlc.AgentGoalAttempt) error {
			rows, err := q.ClaimGoal(ctx, sqlc.ClaimGoalParams{ActiveAttemptID: pgnull.Text(att.ID), ID: cur.ID})
			if err != nil {
				return fmt.Errorf("claim goal: %w", err)
			}
			if rows == 0 {
				return ErrInvalidTransition
			}
			return nil
		},
	})
	s.disposeOnRollback(ctx, err, d.UserID, mintAgentID, sessionID)
	return out, err
}

// dispatchExecutor extracts the executor override from a goal's
// dispatch_hint ({"executor_agent_id": ...}); "" when unset (the worker resolves
// a default executor).
func dispatchExecutor(d sqlc.AgentGoal) string {
	var hint struct {
		ExecutorAgentID string `json:"executor_agent_id"`
	}
	_ = unmarshalJSON(d.DispatchHint, &hint)
	return hint.ExecutorAgentID
}

// ── Submit → fold → branch (contract §5 steps 5-7) ──────────────────────────

// Submit advances an attempt running→submitted, writes its evidence+output, and
// folds acceptance (contract §2.2, §5 steps 5-7). It is the worker's single
// durable call after running the executor (and, for deterministic items, the
// CheckRunner that appended its check events). An empty summary on a non-root
// goal is a retryable protocol miss (ErrInvalidEvidence). The fold runs
// in the SAME tx so submit→evaluate→transition is atomic.
func (s *GoalService) Submit(ctx context.Context, attemptID string, ev AttemptEvidence, out AttemptOutput) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		att, err := q.GetAttempt(ctx, attemptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		d, err := getGoal(ctx, q, att.GoalID)
		if err != nil {
			return err
		}
		// Non-root handoff must carry a summary (the old ErrInvalidHandoff rule,
		// now a contract rule). A retryable protocol miss leaves budget intact.
		if d.ParentID.Valid && ev.Summary == "" {
			return ErrInvalidEvidence
		}
		if out.Hash == "" {
			out.Hash = HashWithArtifacts(out, ev.Artifacts)
		}
		rows, err := s.submitAttempt(ctx, q, att, ev, marshalJSON(out))
		if err != nil {
			return fmt.Errorf("submit attempt: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // not the running attempt (reaped / raced)
		}
		return s.foldAndTransition(ctx, q, d.ID, attemptID, out)
	})
}

// FailAttempt finalizes a worker-reported failed attempt AND applies the single
// convergence transition, so the goal never strands 'active' with no live
// attempt (issue #543). It mirrors ReapAttempt but is the executor-failure entry
// (agent reported fail / produced no action / empty handoff / panic), so the
// attempt is recorded 'failed' rather than 'interrupted'. One tx.
//
//   - execution attempt -> branchOnFailure: budget left reopens to pending (rework =
//     next attempt, the failure reason rides as a gap); budget out blocks/abandons/
//     rejects per policy. A failed attempt consumes one budget unit (same as a fold
//     failure), so a persistently failing agent parks at blocked, never loops.
//   - decomposition attempt → release the composite to draft so a new
//     BeginDecomposition can re-mint (mirror ReapAttempt's decomposition branch).
//
// A 0-row FinalizeAttempt means the attempt is no longer queued/running (already
// reaped/raced); the tx rolls back and the caller treats ErrInvalidTransition as a
// no-op (the goal already recovered).
func (s *GoalService) FailAttempt(ctx context.Context, attemptID, reason, failureClass string, blockedByArg ...string) error {
	blockedBy := ""
	if len(blockedByArg) > 0 {
		blockedBy = blockedByArg[0]
	}
	if !ValidFailureClass(failureClass) || failureClass == "" || (blockedBy != "" && blockedBy != BlockEnvUnavailable && blockedBy != BlockContractConflict) {
		return ErrInvalidTransition
	}
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		att, err := q.GetAttempt(ctx, attemptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := s.finalizeAttempt(ctx, q, att, AttemptFailed, reason, failureClass, blockedBy)
		if err != nil {
			return fmt.Errorf("finalize failed attempt: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		d, err := getGoal(ctx, q, att.GoalID)
		if err != nil {
			return err
		}
		// The goal may have reached a terminal state (e.g. cancelled) while this
		// attempt was in flight. The attempt is finalized above; routing recovery
		// would resurrect the terminal lifecycle — and the env/contract branches
		// would 0-row on blockGoal and roll the finalize back, leaving the attempt
		// to be reaped in a loop. Mirrors ReapAttempt's terminal guard.
		if IsTerminalLifecycle(d.Lifecycle) {
			return nil
		}
		if att.Purpose == PurposeDecomposition {
			return s.routeFailedAttempt(ctx, q, d, attemptID, reason, failureClass, true)
		}
		if att.Purpose == PurposeReview {
			// A failed review attempt (it ran but produced no usable verdict) leaves
			// the goal blocked(needs_verdict): the dispatcher re-mints within the
			// per-episode review budget, then degrades to a human. It is finalized
			// failed above with started_at set, so it charges one budget unit
			// (CountRanReviewAttemptsForOutput) and a broken reviewer cannot loop.
			return nil
		}
		return s.routeFailedAttempt(ctx, q, d, attemptID, reason, failureClass, false)
	})
}

// recoverDecomposition releases a failed or reaped decomposition attempt's
// composite. It returns the composite to draft while billable decomposition
// attempts remain; once the effective budget is spent it parks the composite at
// blocked(budget_exhausted). Queued reaps and flaky/environment/contract failures
// are excluded by CountBillableAttempts, replacing the old refund-by-policy-mutation path.
func (s *GoalService) recoverDecomposition(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, ran bool) error {
	if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	left, err := s.budgetLeft(ctx, q, d, PurposeDecomposition)
	if err != nil {
		return err
	}
	if !left {
		return s.blockBudget(ctx, q, d)
	}
	rows, err := s.transitionGoalLifecycle(ctx, q, d, LifecycleDraft, "")
	if err != nil {
		return fmt.Errorf("recover decomposition: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

// SubmitDecomposition applies a successful autonomous decomposition attempt: it
// records the produced plan on the composite goal, then branches on review policy
// (contract §2.3, §6). For review_policy=none it materializes the children and
// releases them so the tree runs; for review_policy=human it parks the composite
// blocked(needs_plan_approval) so a human can ApprovePlan/RejectPlan. It is the
// decomposition analogue of Submit and the single durable transition the worker
// applies for a purpose=decomposition attempt. Everything runs in ONE tx so a
// crash never leaves a half-planned composite.
func (s *GoalService) SubmitDecomposition(ctx context.Context, attemptID string, ev AttemptEvidence, content DecompositionContent) error {
	att, err := s.q.GetAttempt(ctx, attemptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	parent, err := getGoal(ctx, s.q, att.GoalID)
	if err != nil {
		return err
	}
	if att.Purpose != PurposeDecomposition || parent.Kind != KindComposite {
		return ErrInvalidTransition
	}
	if err := s.validateContent(ctx, parent, content); err != nil {
		return err
	}
	humanReview := parent.ReviewPolicy == ReviewHuman

	err = s.withTx(ctx, func(q *sqlc.Queries) error {
		if err := q.LockGoalForWrite(ctx, parent.ID); err != nil {
			return fmt.Errorf("lock goal for decomposition submit: %w", err)
		}
		// Re-read under the lock: a frozen workflow replay may have materialized
		// this composite since the pre-tx read. planned_at set means a plan is
		// already installed and children exist, so a late decomposition submit
		// must fail closed instead of overwriting the plan and creating a second
		// children set (child ids are content-keyed, not position-keyed).
		cur, err := getGoal(ctx, q, parent.ID)
		if err != nil {
			return err
		}
		if cur.PlannedAt.Valid {
			return ErrInvalidTransition
		}
		// Record the proposed plan on the composite.
		if err := q.SetGoalPlan(ctx, sqlc.SetGoalPlanParams{Plan: marshalJSON(content), ID: parent.ID}); err != nil {
			return fmt.Errorf("set goal plan: %w", err)
		}
		if _, err := s.appendGoalEvent(ctx, q, parent.ID, attemptID, GoalEventPlanSubmitted, planPayload(content)); err != nil {
			return err
		}
		// Finalize the decomposition attempt (running->submitted) so the reaper
		// never recovers it after we move the goal on.
		subRows, err := s.submitAttempt(ctx, q, att, ev, emptyJSON)
		if err != nil {
			return fmt.Errorf("submit decomposition attempt: %w", err)
		}
		if subRows == 0 {
			return ErrInvalidTransition // not the running attempt (reaped / raced)
		}
		if err := q.ClearGoalActiveAttempt(ctx, parent.ID); err != nil {
			return fmt.Errorf("clear active attempt: %w", err)
		}

		if humanReview {
			// Park the composite for human approval (active->blocked). The plan sits
			// in goal.plan until ApprovePlan materializes it or RejectPlan re-plans.
			// Mirror the budget-block path: do NOT eagerly bump the parent counter;
			// the reconcile backstop counts lifecycle='blocked' children.
			rows, err := s.transitionGoalLifecycle(ctx, q, parent, LifecycleBlocked, BlockNeedsPlanApproval)
			if err != nil {
				return fmt.Errorf("block for plan approval: %w", err)
			}
			if rows == 0 {
				return ErrInvalidTransition
			}
			return nil
		}

		// review_policy=none: materialize now and release children. The composite
		// stays active for rollup.
		if err := s.Materialize(ctx, q, parent, content, nil); err != nil {
			return err
		}
		return s.releaseChildren(ctx, q, parent.ID)
	})
	return err
}

// ApprovePlan applies a human approval of a composite's proposed plan: it
// materializes the children and releases them, moving the composite out of
// blocked(needs_plan_approval) back to active for rollup (contract §2.3). Only a
// composite blocked on plan approval is accepted.
func (s *GoalService) ApprovePlan(ctx context.Context, goalID string, by Actor) error {
	parent, err := getGoal(ctx, s.q, goalID)
	if err != nil {
		return err
	}
	if parent.Kind != KindComposite || parent.Lifecycle != LifecycleBlocked ||
		parent.BlockReason != BlockNeedsPlanApproval {
		return ErrInvalidTransition
	}
	var content DecompositionContent
	if err := unmarshalJSON(parent.Plan, &content); err != nil {
		return fmt.Errorf("%w: goal plan: %w", ErrInvalidDecomposition, err)
	}
	if err := s.validateContent(ctx, parent, content); err != nil {
		return err
	}
	err = s.withTx(ctx, func(q *sqlc.Queries) error {
		if err := q.LockGoalForWrite(ctx, parent.ID); err != nil {
			return fmt.Errorf("lock goal for plan approval: %w", err)
		}
		// Re-read under the lock: a concurrent reject/approve may have moved it.
		cur, err := getGoal(ctx, q, parent.ID)
		if err != nil {
			return err
		}
		if cur.Lifecycle != LifecycleBlocked || cur.BlockReason != BlockNeedsPlanApproval {
			return ErrInvalidTransition
		}
		if err := s.Materialize(ctx, q, cur, content, nil); err != nil {
			return err
		}
		rows, err := s.transitionGoalLifecycle(ctx, q, cur, LifecycleActive, "")
		if err != nil {
			return fmt.Errorf("activate after plan approval: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return s.releaseChildren(ctx, q, cur.ID)
	})
	return err
}

// RejectPlan applies a human rejection of a composite's proposed plan: it clears
// the plan and returns the composite to draft so the dispatcher re-decomposes it
// (contract §2.3). Only a composite blocked on plan approval AND not yet
// materialized (planned_at IS NULL) is accepted — replan after materialize is not
// supported (childID would collide; see materializer.go).
func (s *GoalService) RejectPlan(ctx context.Context, goalID, reason string, by Actor) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		if err := q.LockGoalForWrite(ctx, goalID); err != nil {
			return fmt.Errorf("lock goal for plan reject: %w", err)
		}
		cur, err := getGoal(ctx, q, goalID)
		if err != nil {
			return err
		}
		if cur.Kind != KindComposite || cur.Lifecycle != LifecycleBlocked ||
			cur.BlockReason != BlockNeedsPlanApproval || cur.PlannedAt.Valid {
			return ErrInvalidTransition
		}
		if err := q.SetGoalPlan(ctx, sqlc.SetGoalPlanParams{Plan: emptyJSON, ID: cur.ID}); err != nil {
			return fmt.Errorf("clear goal plan: %w", err)
		}
		rows, err := s.transitionGoalLifecycle(ctx, q, cur, LifecycleDraft, "")
		if err != nil {
			return fmt.Errorf("return goal to draft after plan reject: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return nil
	})
}

// releaseChildren moves a composite's freshly materialized draft children out of
// draft so the tree runs: a leaf child -> pending (the dispatcher claims it), a
// composite child STAYS draft so scanAndDecompose recurses and plans it in turn.
// The composite itself is left as-is. Shared by Activate and SubmitDecomposition.
func (s *GoalService) releaseChildren(ctx context.Context, q *sqlc.Queries, parentID string) error {
	children, err := q.ListGoalChildren(ctx, pgnull.Text(parentID))
	if err != nil {
		return fmt.Errorf("list children for release: %w", err)
	}
	for _, c := range children {
		if c.Lifecycle != LifecycleDraft || c.Kind == KindComposite {
			continue // composite children await their own decomposition (still draft)
		}
		if _, err := s.transitionGoalLifecycle(ctx, q, c, LifecyclePending, ""); err != nil {
			return fmt.Errorf("release child: %w", err)
		}
	}
	return nil
}

// applyAcceptance is the re-fold entry point used after a verdict arrives or a
// check event is appended outside Submit (contract §4.3). It opens its own tx,
// derives the projection over the full ledger and applies exactly one transition.
// The hot path (Submit) calls foldAndTransition directly inside its tx.
//
// It resolves the evaluated attempt's output even when the active pointer was
// cleared by a needs_verdict block, so a verdict's scope_hash still anchors; the
// wake out of blocked(needs_verdict) happens inside foldAndTransition, gated on the
// fold reaching a real transition (never on the pending branch — see there).
func (s *GoalService) applyAcceptance(ctx context.Context, goalID string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getGoal(ctx, q, goalID)
		if err != nil {
			return err
		}
		attemptID, out := s.evaluatedAttempt(ctx, q, d)
		return s.foldAndTransition(ctx, q, goalID, attemptID, out)
	})
}

// evaluatedAttempt returns the attempt whose output the current fold scopes to and
// its decoded output: the active attempt when one is pointed, else the most recent
// SUBMITTED execution attempt — the output a human reviewed after blockForVerdict
// cleared the active pointer. ("", AttemptOutput{}) when none, so a "" hash forces
// any scoped verdict to re-request (no output ⇒ no valid pass, §4.2).
func (s *GoalService) evaluatedAttempt(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) (string, AttemptOutput) {
	if id := d.ActiveAttemptID.String; id != "" {
		att, err := q.GetAttempt(ctx, id)
		if err != nil {
			return "", AttemptOutput{}
		}
		return id, decodeOutput(att.Output)
	}
	atts, err := q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{
		GoalID:  d.ID,
		Purpose: pgnull.Text(PurposeExecution),
	})
	if err != nil {
		return "", AttemptOutput{}
	}
	for _, a := range atts { // attempt_no DESC: the most recent submitted output
		if a.Status == AttemptSubmitted {
			return a.ID, decodeOutput(a.Output)
		}
	}
	return "", AttemptOutput{}
}

// decodeOutput parses an attempt's stored output JSON, degrading to the zero
// value (a "" hash) on a malformed column.
func decodeOutput(s json.RawMessage) AttemptOutput {
	var out AttemptOutput
	_ = unmarshalJSON(s, &out)
	return out
}

// foldAndTransition is the convergence branch (contract §5 step 7): derive the
// projection over the full ledger, write the new acceptance_state under the
// stale-projection fence, then apply ONE lifecycle move. It must run inside an
// open tx (Submit's or applyAcceptance's). Nothing else maps projection →
// lifecycle.
//
//   - passed       → acceptGoal (freeze output, clear attempt, bump parent).
//   - failed + budget left → write gaps on the attempt; the dispatcher mints the
//     next attempt (rework = next attempt, not a node).
//   - failed + budget out  -> blocked(budget_exhausted) or done(failed).
//   - NeedsVerdict → blocked(needs_verdict) (a pending human is not an executing
//     episode, so the active attempt is cleared).
//
// A verdict resolving a blocked(needs_verdict) goal re-enters here from
// applyAcceptance. The passed/failed branches wake it blocked→active first (their
// transitions guard from 'active'); the NeedsVerdict branch is idempotent when
// already blocked; the pending branch must NOT wake — waking into a still-pending
// fold would strand it 'active' with no attempt and no recovery route, so a
// blocked goal whose fold is still pending stays safely blocked.
func (s *GoalService) foldAndTransition(ctx context.Context, q *sqlc.Queries, goalID, attemptID string, out AttemptOutput) error {
	d, err := getGoal(ctx, q, goalID)
	if err != nil {
		return err
	}
	var contract AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &contract)

	events, err := q.ListAcceptanceEventByGoal(ctx, goalID)
	if err != nil {
		return fmt.Errorf("list acceptance events: %w", err)
	}
	proj := DeriveAcceptance(contract, out.Hash, events)

	// Persist the projection under the stale fence. seq = #events folded; the
	// fence rejects a fold computed against an out-of-date seq.
	if _, err := q.SetGoalAcceptanceState(ctx, sqlc.SetGoalAcceptanceStateParams{
		AcceptanceState: proj.State,
		AcceptanceSeq:   int64(len(events)),
		ID:              goalID,
	}); err != nil {
		return fmt.Errorf("set acceptance state: %w", err)
	}

	switch {
	case proj.State == AcceptancePassed:
		if err := s.wakeIfBlocked(ctx, q, &d); err != nil {
			return err
		}
		return s.acceptFolded(ctx, q, d, attemptID, out)
	case proj.NeedsVerdict:
		return s.blockForVerdict(ctx, q, d)
	case proj.State == AcceptanceFailed:
		if err := s.wakeIfBlocked(ctx, q, &d); err != nil {
			return err
		}
		return s.branchOnFailure(ctx, q, d, attemptID, proj.Gaps)
	default:
		// Pending: more events to come (an in-flight attempt, or a verdict that
		// resolved one item while a non-judgment item is still event-less). A
		// blocked goal stays blocked — do NOT wake into a strand.
		return nil
	}
}

// wakeIfBlocked promotes a goal parked in blocked(needs_verdict) back to
// active so a terminal fold branch (acceptFolded/branchOnFailure) — which guards
// from 'active', the same from-state Submit folds against — can fire once a
// verdict resolves the block. A no-op when not so blocked. It mutates the passed
// goal's lifecycle so a subsequent in-Go guard reads the woken state.
func (s *GoalService) wakeIfBlocked(ctx context.Context, q *sqlc.Queries, d *sqlc.AgentGoal) error {
	if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockNeedsVerdict {
		return nil
	}
	rows, err := s.transitionGoalLifecycle(ctx, q, *d, LifecycleActive, "")
	if err != nil {
		return fmt.Errorf("wake needs_verdict: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	d.Lifecycle = LifecycleActive
	d.BlockReason = ""
	return nil
}

// acceptFolded freezes the accepted output and flips the goal to done(accepted)
// (contract §2.1, §6 rollup). It is the fold's accept branch for both a leaf
// (output = the accepted attempt's) and an authored-contract composite (output
// synthesized from the children below). Downstream readiness is re-derived
// lazily by the dispatcher off the upstream-accepted index, so no push is
// needed here.
func (s *GoalService) acceptFolded(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, attemptID string, out AttemptOutput) error {
	// Already at the desired terminal state: a re-fold of an accepted leaf (e.g. a
	// verdict re-submitted after acceptance, or a double-clicked Approve whose
	// first request already accepted) derives passed again. That is the steady
	// state, not a conflict — return nil rather than a 0-row AcceptGoal
	// (which guards from 'active') so the verdict path is idempotent and the
	// parent counter is bumped exactly once. Mirrors blockForVerdict's guard.
	if d.Lifecycle == LifecycleDone {
		return nil
	}
	accepted := AcceptedOutput{
		GoalID:        d.ID,
		Summary:       out.Summary,
		Result:        out.Result,
		Hash:          out.Hash,
		AcceptedAt:    s.now(),
		SourceAttempt: attemptID,
	}
	if d.Kind == KindComposite {
		// An authored-contract composite folds with no execution output (out is
		// empty), but its deliverable is its children's: carry their frozen outputs
		// exactly as the trivial-contract rollup does, so downstream readers see the
		// phase results without walking the tree.
		kids, err := childAcceptedOutputs(ctx, q, d.ID)
		if err != nil {
			return fmt.Errorf("collect child outputs: %w", err)
		}
		accepted.Children = kids
		if accepted.Summary == "" {
			accepted.Summary = d.Title
		}
	}
	rows, err := s.acceptGoal(ctx, q, d, accepted)
	if err != nil {
		return fmt.Errorf("accept goal: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition // not active (raced)
	}
	return nil
}

// blockForVerdict parks a goal awaiting a required human verdict:
// active→blocked(needs_verdict), clearing the active attempt (a pending human is
// not an executing episode, contract §2.1).
func (s *GoalService) blockForVerdict(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	// Already parked awaiting this verdict (a multi-item contract where a verdict
	// resolved one item but another judgment item is still pending): the desired
	// end-state IS blocked(needs_verdict), so this is an idempotent no-op rather
	// than a 0-row BlockGoal (which guards from pending/active, not blocked).
	if d.Lifecycle == LifecycleBlocked && d.BlockReason == BlockNeedsVerdict {
		return nil
	}
	rows, err := s.blockGoal(ctx, q, d, BlockNeedsVerdict)
	if err != nil {
		return fmt.Errorf("block goal: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

func (s *GoalService) routeFailedAttempt(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, attemptID, reason, failureClass string, decomposition bool) error {
	switch failureClass {
	case FailureClassModel:
		if decomposition {
			return s.recoverDecomposition(ctx, q, d, true)
		}
		return s.branchOnFailure(ctx, q, d, attemptID, Evaluation{Gaps: []Gap{{Reason: reason}}})
	case FailureClassEnvironment:
		return s.blockFailureCause(ctx, q, d, BlockEnvUnavailable)
	case FailureClassContract:
		return s.blockFailureCause(ctx, q, d, BlockContractConflict)
	case FailureClassFlaky:
		count, err := q.IncrementGoalFlakyCount(ctx, d.ID)
		if err != nil {
			return fmt.Errorf("increment flaky count: %w", err)
		}
		if count > 5 {
			return s.blockFailureCause(ctx, q, d, BlockEnvUnavailable)
		}
		if decomposition {
			return s.recoverDecomposition(ctx, q, d, true)
		}
		return s.reopenForRework(ctx, q, d)
	default:
		return ErrInvalidTransition
	}
}

// branchOnFailure routes an acceptance failure (contract §5 step 7). With budget
// left it records the gaps on the rejected attempt and clears the active pointer
// so the dispatcher mints attempt_no+1 (rework = next attempt). Budget out routes
// by escalation/contract shape to blocked(budget_exhausted) or done(failed).
func (s *GoalService) branchOnFailure(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, attemptID string, gaps Evaluation) error {
	// A composite cannot rework: it has no execution attempts (its work is its
	// children, all accepted by the time its own gate folds), so reopening to
	// pending would strand it -- the dispatcher only claims pending LEAVES. A failing
	// verdict on its judgment gate parks it back at needs_verdict for a human to
	// adjudicate: override the item, edit the contract, or cancel.
	if d.Kind == KindComposite {
		return s.blockForVerdict(ctx, q, d)
	}
	var pol ConvergencePolicy
	_ = unmarshalJSON(d.ConvergencePolicy, &pol)
	pol = pol.Normalized()

	if attemptID != "" {
		if err := q.SetAttemptGaps(ctx, sqlc.SetAttemptGapsParams{
			Gaps: marshalJSON(gaps),
			ID:   attemptID,
		}); err != nil {
			return fmt.Errorf("set attempt gaps: %w", err)
		}
	}

	left, err := s.budgetLeft(ctx, q, d, PurposeExecution)
	if err != nil {
		return err
	}
	if left {
		// Budget left: clear the active attempt so the goal returns to pending
		// for the next claim. The dispatcher re-claims and mints attempt_no+1 with
		// the gaps now in input_context.
		return s.reopenForRework(ctx, q, d)
	}

	// Budget exhausted: terminal/blocked per policy and contract shape.
	switch {
	case judgmentOnlyContract(d):
		return s.transition(ctx, q, d, LifecycleDone, "")
	case pol.Escalation == EscalationAbandon:
		return s.transition(ctx, q, d, LifecycleDone, "")
	default:
		return s.blockBudget(ctx, q, d)
	}
}

// reopenForRework returns a failed-with-budget goal to pending for the next
// attempt: clear the active attempt and move active->pending. The current attempt
// stays 'submitted' with its gaps recorded; the new attempt carries them.
func (s *GoalService) reopenForRework(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	rows, err := s.transitionGoalLifecycle(ctx, q, d, LifecyclePending, "")
	if err != nil {
		return fmt.Errorf("reopen for rework: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

func (s *GoalService) blockFailureCause(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, cause string) error {
	rows, err := s.blockGoal(ctx, q, d, cause)
	if err != nil {
		return fmt.Errorf("block %s: %w", cause, err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

// blockBudget parks an exhausted goal: active→blocked(budget_exhausted),
// awaiting a human Reattempt (raise budget) or Abandon (contract §2.1).
func (s *GoalService) blockBudget(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	rows, err := s.blockGoal(ctx, q, d, BlockBudgetExhausted)
	if err != nil {
		return fmt.Errorf("block budget: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

// transition applies a terminal active->{to} move. Used for budget-out
// done(failed) paths.
func (s *GoalService) transition(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, to, blockReason string) error {
	if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	rows, err := s.transitionGoalLifecycle(ctx, q, d, to, blockReason)
	if err != nil {
		return fmt.Errorf("transition to %s: %w", to, err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

// judgmentOnlyContract reports whether a goal's contract has no required
// deterministic item -- a pure-judgment contract has no rework path on a failed
// verdict, so budget exhaustion becomes done(failed) rather than recoverable.
func judgmentOnlyContract(d sqlc.AgentGoal) bool {
	var c AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &c)
	if c.IsTrivial() {
		return false
	}
	for _, it := range c.Items {
		if it.Required && it.Kind == ItemDeterministic {
			return false
		}
	}
	return true
}
