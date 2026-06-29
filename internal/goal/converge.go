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
func buildInputContext(d sqlc.AgentGoal, upstream []AcceptedOutput, priorGaps *Evaluation, resolvedVerdict string, attemptNo int) AttemptInput {
	var c AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &c)
	return AttemptInput{
		Intent:          d.Intent,
		UpstreamOutputs: upstream,
		PriorGaps:       priorGaps,
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
		if e.UpstreamLifecycle != LifecycleAccepted || !e.UpstreamOutput.Valid {
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

// priorGapsFor returns the most recent execution attempt's recorded gaps as the
// next attempt's input (the "rework = next attempt" loop, §5 step 7). nil on the
// first attempt or when no gaps were recorded. The prior attempt is 'submitted'
// (not active), so it is read off the attempt_no-DESC list.
func (s *GoalService) priorGapsFor(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) (*Evaluation, error) {
	if d.AttemptCount == 0 {
		return nil, nil
	}
	prev := PurposeExecution
	atts, err := q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{
		GoalID:  d.ID,
		Purpose: pgnull.Text(prev),
	})
	if err != nil {
		return nil, fmt.Errorf("list attempts: %w", err)
	}
	if len(atts) == 0 {
		return nil, nil
	}
	// Best-effort parse: a malformed gaps blob is simply "no prior evaluation",
	// not an error worth propagating — the next attempt starts clean.
	var ev Evaluation
	_ = unmarshalJSON(atts[0].Gaps, &ev)
	if len(ev.Gaps) == 0 {
		return nil, nil
	}
	return &ev, nil
}

// ── Mint next attempt (contract §5 step 1) ──────────────────────────────────

// mintNextAttempt is the convergence "Claim" helper: in ONE tx it transitions a
// ready leaf → active, inserts a queued attempt at attempt_no = attempt_count+1
// with its input_context frozen, points active_attempt_id at it and bumps
// attempt_count. uniq_agent_goal_active_attempt enforces ≤1 active attempt per
// (goal, purpose); a lost race surfaces as ErrInvalidTransition.
//
// resolvedVerdict carries a human answer that unblocked a needs_verdict (it
// rides in the frozen input). executorAgentID is the claim-time-resolved
// executor ("" leaves it NULL).
func (s *GoalService) mintNextAttempt(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, executorAgentID, resolvedVerdict string) (sqlc.AgentGoalAttempt, error) {
	upstream, err := upstreamAcceptedOutputs(ctx, q, d.ID)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}
	prior, err := s.priorGapsFor(ctx, q, d)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}

	attemptNo := int(d.AttemptCount) + 1
	input := buildInputContext(d, upstream, prior, resolvedVerdict, attemptNo)

	att, err := q.CreateAttempt(ctx, sqlc.CreateAttemptParams{
		ID:              newID(),
		GoalID:          d.ID,
		UserID:          d.UserID,
		AgentID:         pgnull.Text(d.AgentID),
		ExecutorAgentID: pgnull.Text(executorAgentID),
		SessionID:       d.SessionID,
		Purpose:         PurposeExecution,
		AttemptNo:       int64(attemptNo),
		Status:          AttemptQueued,
		InputContext:    marshalJSON(input),
		// Grace lease on the queued attempt: a River worker on any node must be
		// able to pick the job up and PromoteAttempt (extending the lease) before
		// the dispatcher reaper reclaims it. now() would be instantly stale — that
		// only worked under the old in-process active-map guard. The lease is now
		// the single, multi-node liveness signal: a claim/enqueue gap or a crash
		// before pickup recovers via the reaper once this grace window expires.
		LeaseExpiresAt: nullTime(s.nowTime().Add(claimGraceTTL)),
	})
	if err != nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("create attempt: %w", err)
	}

	rows, err := q.ClaimGoal(ctx, sqlc.ClaimGoalParams{
		ActiveAttemptID: pgnull.Text(att.ID),
		ID:              d.ID,
	})
	if err != nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("claim goal: %w", err)
	}
	if rows == 0 {
		// ready→active guard failed (already claimed / not ready) — the unique
		// active-attempt index would also reject; roll the tx back.
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition
	}
	return att, nil
}

// Claim is the dispatcher's leaf ready→active (contract §2.1, §5 step 1). It
// resolves the dispatch hint's executor, mints a queued execution attempt and
// claims the goal in one tx. The per-root/per-user concurrency caps are
// enforced by the dispatcher BEFORE Claim (§5 step 2); Claim itself only guards
// the single-writer invariants.
//
// enqueue (River Phase 2c) inserts the attempt's durable execution job in the
// SAME tx, so the claim and its job commit atomically — a crash can no longer
// leave a claimed attempt with no job to run it. A nil enqueue skips this (tests
// minting+claiming without River); its failure rolls the claim back, leaving the
// goal ready for the next tick.
func (s *GoalService) Claim(ctx context.Context, id, workerID string, enqueue AttemptEnqueuer) (sqlc.AgentGoalAttempt, error) {
	var out sqlc.AgentGoalAttempt
	err := s.withTxRaw(ctx, func(q *sqlc.Queries, tx pgx.Tx) error {
		d, err := getGoal(ctx, q, id)
		if err != nil {
			return err
		}
		if d.Kind != KindLeaf || d.Lifecycle != LifecycleReady || d.ActiveAttemptID.Valid {
			return ErrInvalidTransition
		}
		att, err := s.mintNextAttempt(ctx, q, d, dispatchExecutor(d), "")
		if err != nil {
			return err
		}
		if enqueue != nil {
			if err := enqueue(ctx, tx, d.ID, att.ID); err != nil {
				return fmt.Errorf("enqueue attempt job: %w", err)
			}
		}
		out = att
		return nil
	})
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
		rows, err := q.SubmitAttempt(ctx, sqlc.SubmitAttemptParams{
			Evidence: marshalJSON(ev),
			Output:   marshalJSON(out),
			ID:       attemptID,
		})
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
//   - execution attempt → branchOnFailure: budget left reopens to ready (rework =
//     next attempt, the failure reason rides as a gap); budget out blocks/abandons/
//     rejects per policy. A failed attempt consumes one budget unit (same as a fold
//     failure), so a persistently failing agent parks at blocked, never loops.
//   - decomposition attempt → release the composite to draft so a new
//     BeginDecomposition can re-mint (mirror ReapAttempt's decomposition branch).
//
// A 0-row FinalizeAttempt means the attempt is no longer queued/running (already
// reaped/raced); the tx rolls back and the caller treats ErrInvalidTransition as a
// no-op (the goal already recovered).
func (s *GoalService) FailAttempt(ctx context.Context, attemptID, reason string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		att, err := q.GetAttempt(ctx, attemptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := q.FinalizeAttempt(ctx, sqlc.FinalizeAttemptParams{
			ToStatus: AttemptFailed,
			Error:    reason,
			ID:       attemptID,
		})
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
		if att.Purpose == PurposeDecomposition {
			// A reported decomposition failure always ran the planner, so it charges
			// one plan-budget unit.
			return s.recoverDecomposition(ctx, q, d, true)
		}
		if att.Purpose == PurposeReview {
			// A failed review attempt (it ran but produced no usable verdict) leaves
			// the goal blocked(needs_verdict): the dispatcher re-mints within the
			// per-episode review budget, then degrades to a human. It is finalized
			// failed above with started_at set, so it charges one budget unit
			// (CountRanReviewAttemptsForOutput) and a broken reviewer cannot loop.
			return nil
		}
		return s.branchOnFailure(ctx, q, d, attemptID, Evaluation{Gaps: []Gap{{Reason: reason}}})
	})
}

// recoverDecomposition releases a failed or reaped decomposition attempt's
// composite. It returns the composite to draft so the dispatcher re-decomposes it
// next tick, until the plan budget is spent (convergence MaxAttempts) — then it
// parks active->blocked(budget_exhausted) so a persistently failing planner waits
// for a human instead of looping forever.
//
// ran reports whether the planner attempt actually executed. The plan budget is
// metered by GetMaxAttemptNo (the max decomposition attempt_no), which keeps
// climbing across re-claims. A queued reap (ran=false) never executed — the
// River worker never picked it up (queue backpressure under wide fanout) — yet
// it still consumed an attempt_no, so we refund by raising MaxAttempts one step,
// mirroring refundAndReopen on the leaf path. Without that refund a string of
// queued reaps would burn the whole plan budget on attempts that never ran and
// block the composite without a single real planning failure.
func (s *GoalService) recoverDecomposition(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, ran bool) error {
	if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	var pol ConvergencePolicy
	_ = unmarshalJSON(d.ConvergencePolicy, &pol)
	pol = pol.Normalized()
	if ran {
		spent, err := q.GetMaxAttemptNo(ctx, sqlc.GetMaxAttemptNoParams{
			GoalID:  d.ID,
			Purpose: PurposeDecomposition,
		})
		if err != nil {
			return fmt.Errorf("max decomposition attempt no: %w", err)
		}
		if int(spent) >= pol.MaxAttempts {
			return s.blockBudget(ctx, q, d)
		}
	} else {
		// Refund the attempt_no a queued reap consumed without executing.
		pol.MaxAttempts++
		if err := q.UpdateGoalIntent(ctx, sqlc.UpdateGoalIntentParams{
			Title:              d.Title,
			Intent:             d.Intent,
			AcceptanceContract: d.AcceptanceContract,
			ConvergencePolicy:  marshalJSON(pol),
			ReviewPolicy:       d.ReviewPolicy,
			Priority:           d.Priority,
			ID:                 d.ID,
		}); err != nil {
			return fmt.Errorf("refund decomposition budget on queued reap: %w", err)
		}
	}
	rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
		ToLifecycle:   LifecycleDraft,
		BlockReason:   "",
		ID:            d.ID,
		FromLifecycle: LifecycleActive,
	})
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
// applies for a purpose=decomposition attempt. For the none path child sessions
// are pre-minted OUTSIDE the tx (their own minting tx would self-deadlock against
// the held one); everything else runs in ONE tx so a crash never leaves a
// half-planned composite.
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

	// Pre-mint a session per child OUTSIDE the tx, but only for the none path that
	// materializes now. The human path defers materialize (and session minting) to
	// ApprovePlan, so it mints nothing here.
	childSessions := make(map[string]string, len(content.Children))
	if !humanReview {
		if s.newSession == nil {
			return fmt.Errorf("goal: no worker session minter configured")
		}
		for _, ch := range content.Children {
			sid, err := s.newSession(ctx, parent.UserID, parent.AgentID, parent.ProjectID.String)
			if err != nil {
				// A mid-batch mint failure orphans the children minted before it (no tx
				// has run yet); archive them so the partial batch does not leak.
				s.disposeOrphanSessions(ctx, parent.UserID, parent.AgentID, mapValues(childSessions)...)
				return fmt.Errorf("mint child session %q: %w", ch.Key, err)
			}
			childSessions[ch.Key] = sid
		}
	}

	err = s.withTx(ctx, func(q *sqlc.Queries) error {
		if err := q.LockGoalForWrite(ctx, parent.ID); err != nil {
			return fmt.Errorf("lock goal for decomposition submit: %w", err)
		}
		// Record the proposed plan on the composite.
		if err := q.SetGoalPlan(ctx, sqlc.SetGoalPlanParams{Plan: marshalJSON(content), ID: parent.ID}); err != nil {
			return fmt.Errorf("set goal plan: %w", err)
		}
		// Finalize the decomposition attempt (running->submitted) so the reaper
		// never recovers it after we move the goal on.
		subRows, err := q.SubmitAttempt(ctx, sqlc.SubmitAttemptParams{
			Evidence: marshalJSON(ev),
			Output:   emptyJSON,
			ID:       attemptID,
		})
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
			rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
				ToLifecycle:   LifecycleBlocked,
				BlockReason:   BlockNeedsPlanApproval,
				ID:            parent.ID,
				FromLifecycle: LifecycleActive,
			})
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
		if err := s.Materialize(ctx, q, parent, content, childSessions); err != nil {
			return err
		}
		return s.releaseChildren(ctx, q, parent.ID)
	})
	// On a definite rollback, archive the child sessions pre-minted above (none on
	// the human-review path) so they are not orphaned.
	s.disposeOnRollback(ctx, err, parent.UserID, parent.AgentID, mapValues(childSessions)...)
	return err
}

// ApprovePlan applies a human approval of a composite's proposed plan: it
// materializes the children and releases them, moving the composite out of
// blocked(needs_plan_approval) back to active for rollup (contract §2.3). Child
// sessions are pre-minted OUTSIDE the tx (mirrors the none path in
// SubmitDecomposition). Only a composite blocked on plan approval is accepted.
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
	if s.newSession == nil {
		return fmt.Errorf("goal: no worker session minter configured")
	}
	childSessions := make(map[string]string, len(content.Children))
	for _, ch := range content.Children {
		sid, err := s.newSession(ctx, parent.UserID, parent.AgentID, parent.ProjectID.String)
		if err != nil {
			// A mid-batch mint failure orphans the children minted before it (no tx
			// has run yet); archive them so the partial batch does not leak.
			s.disposeOrphanSessions(ctx, parent.UserID, parent.AgentID, mapValues(childSessions)...)
			return fmt.Errorf("mint child session %q: %w", ch.Key, err)
		}
		childSessions[ch.Key] = sid
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
		if err := s.Materialize(ctx, q, cur, content, childSessions); err != nil {
			return err
		}
		rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
			ToLifecycle:   LifecycleActive,
			BlockReason:   "",
			ID:            cur.ID,
			FromLifecycle: LifecycleBlocked,
		})
		if err != nil {
			return fmt.Errorf("activate after plan approval: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition
		}
		return s.releaseChildren(ctx, q, cur.ID)
	})
	// On a definite rollback (concurrent reject/approve / collision), archive the
	// child sessions pre-minted above so they are not orphaned.
	s.disposeOnRollback(ctx, err, parent.UserID, parent.AgentID, mapValues(childSessions)...)
	return err
}

// mapValues returns a map's values as a slice in unspecified order. Used to feed
// a pre-minted child-session map to disposeOrphanSessions on a rollback.
func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
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
		rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
			ToLifecycle:   LifecycleDraft,
			BlockReason:   "",
			ID:            cur.ID,
			FromLifecycle: LifecycleBlocked,
		})
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
// draft so the tree runs: a leaf child -> ready (the dispatcher claims it), a
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
		if _, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
			ToLifecycle:   LifecycleReady,
			BlockReason:   "",
			ID:            c.ID,
			FromLifecycle: LifecycleDraft,
		}); err != nil {
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
//   - failed + budget out  → blocked(budget_exhausted) | abandoned | rejected_final.
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
		return s.acceptLeaf(ctx, q, d, attemptID, out)
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
// active so a terminal fold branch (acceptLeaf/branchOnFailure) — which guards
// from 'active', the same from-state Submit folds against — can fire once a
// verdict resolves the block. A no-op when not so blocked. It mutates the passed
// goal's lifecycle so a subsequent in-Go guard reads the woken state.
func (s *GoalService) wakeIfBlocked(ctx context.Context, q *sqlc.Queries, d *sqlc.AgentGoal) error {
	if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockNeedsVerdict {
		return nil
	}
	rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
		ToLifecycle:   LifecycleActive,
		BlockReason:   "",
		ID:            d.ID,
		FromLifecycle: LifecycleBlocked,
	})
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

// acceptLeaf freezes the accepted output, flips the goal → accepted, and
// bumps the parent's required_accepted in the SAME tx (contract §2.1
// active→accepted, §6 rollup). Downstream readiness is re-derived lazily by the
// dispatcher off the upstream-accepted index, so no push is needed here.
func (s *GoalService) acceptLeaf(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, attemptID string, out AttemptOutput) error {
	// Already at the desired terminal state: a re-fold of an accepted leaf (e.g. a
	// verdict re-submitted after acceptance, or a double-clicked Approve whose
	// first request already accepted) derives passed again. That is the steady
	// state, not a conflict — return nil rather than a 0-row AcceptGoal
	// (which guards from 'active') so the verdict path is idempotent and the
	// parent counter is bumped exactly once. Mirrors blockForVerdict's guard.
	if d.Lifecycle == LifecycleAccepted {
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
	rows, err := q.AcceptGoal(ctx, sqlc.AcceptGoalParams{
		AcceptedOutput: marshalNullJSON(accepted),
		ID:             d.ID,
	})
	if err != nil {
		return fmt.Errorf("accept goal: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition // not active (raced)
	}
	return s.bumpParentCounter(ctx, q, d, counterAccepted)
}

// blockForVerdict parks a goal awaiting a required human verdict:
// active→blocked(needs_verdict), clearing the active attempt (a pending human is
// not an executing episode, contract §2.1).
func (s *GoalService) blockForVerdict(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	// Already parked awaiting this verdict (a multi-item contract where a verdict
	// resolved one item but another judgment item is still pending): the desired
	// end-state IS blocked(needs_verdict), so this is an idempotent no-op rather
	// than a 0-row BlockGoal (which guards from ready/active, not blocked).
	if d.Lifecycle == LifecycleBlocked && d.BlockReason == BlockNeedsVerdict {
		return nil
	}
	rows, err := q.BlockGoal(ctx, sqlc.BlockGoalParams{
		BlockReason: BlockNeedsVerdict,
		ID:          d.ID,
	})
	if err != nil {
		return fmt.Errorf("block goal: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

// branchOnFailure routes an acceptance failure (contract §5 step 7). With budget
// left it records the gaps on the rejected attempt and clears the active pointer
// so the dispatcher mints attempt_no+1 (rework = next attempt). Budget out routes
// by escalation/contract shape to blocked(budget_exhausted), abandoned, or
// rejected_final.
func (s *GoalService) branchOnFailure(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, attemptID string, gaps Evaluation) error {
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

	if d.AttemptCount < int64(pol.MaxAttempts) {
		// Budget left: clear the active attempt so the goal returns to ready
		// for the next claim. The dispatcher re-claims and mints attempt_no+1 with
		// the gaps now in input_context.
		return s.reopenForRework(ctx, q, d)
	}

	// Budget exhausted: terminal/blocked per policy and contract shape.
	switch {
	case judgmentOnlyContract(d):
		return s.transition(ctx, q, d, LifecycleRejectedFinal, "", counterFailed)
	case pol.Escalation == EscalationAbandon:
		return s.transition(ctx, q, d, LifecycleAbandoned, "", counterFailed)
	default:
		return s.blockBudget(ctx, q, d)
	}
}

// reopenForRework returns a failed-with-budget goal to ready for the next
// attempt: clear the active attempt and move active→ready. The current attempt
// stays 'submitted' with its gaps recorded; the new attempt carries them.
func (s *GoalService) reopenForRework(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
		ToLifecycle:   LifecycleReady,
		BlockReason:   "",
		ID:            d.ID,
		FromLifecycle: LifecycleActive,
	})
	if err != nil {
		return fmt.Errorf("reopen for rework: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

// refundAndReopen returns a goal to ready WITHOUT charging convergence budget,
// for an attempt reaped while still 'queued' (it never executed). ClaimGoal
// already bumped attempt_count at claim, so we restore the pre-claim budget by
// raising the MaxAttempts ceiling one step — we cannot decrement attempt_count
// because uniq_agent_goal_attempt_no requires attempt_no to keep climbing across
// re-claims. Mirrors Reattempt's budget raise. reopenForRework then clears the
// active attempt and moves active→ready.
func (s *GoalService) refundAndReopen(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	var pol ConvergencePolicy
	_ = unmarshalJSON(d.ConvergencePolicy, &pol)
	pol = pol.Normalized()
	pol.MaxAttempts++
	if err := q.UpdateGoalIntent(ctx, sqlc.UpdateGoalIntentParams{
		Title:              d.Title,
		Intent:             d.Intent,
		AcceptanceContract: d.AcceptanceContract,
		ConvergencePolicy:  marshalJSON(pol),
		ReviewPolicy:       d.ReviewPolicy,
		Priority:           d.Priority,
		ID:                 d.ID,
	}); err != nil {
		return fmt.Errorf("refund budget on queued reap: %w", err)
	}
	return s.reopenForRework(ctx, q, d)
}

// blockBudget parks an exhausted goal: active→blocked(budget_exhausted),
// awaiting a human Reattempt (raise budget) or Abandon (contract §2.1).
func (s *GoalService) blockBudget(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) error {
	rows, err := q.BlockGoal(ctx, sqlc.BlockGoalParams{
		BlockReason: BlockBudgetExhausted,
		ID:          d.ID,
	})
	if err != nil {
		return fmt.Errorf("block budget: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return nil
}

// transition applies a terminal active→{to} move and bumps one parent counter in
// the same tx (contract §6). Used for the budget-out terminal-bad paths
// (rejected_final/abandoned).
func (s *GoalService) transition(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal, to, blockReason string, counter counterKind) error {
	if err := q.ClearGoalActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	rows, err := q.TransitionGoalLifecycle(ctx, sqlc.TransitionGoalLifecycleParams{
		ToLifecycle:   to,
		BlockReason:   blockReason,
		ID:            d.ID,
		FromLifecycle: LifecycleActive,
	})
	if err != nil {
		return fmt.Errorf("transition to %s: %w", to, err)
	}
	if rows == 0 {
		return ErrInvalidTransition
	}
	return s.bumpParentCounter(ctx, q, d, counter)
}

// judgmentOnlyContract reports whether a goal's contract has no required
// deterministic item — a pure-judgment contract has no rework path on a failed
// verdict, so budget exhaustion is rejected_final rather than recoverable.
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
