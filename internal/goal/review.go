package goal

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// review.go is the agent auto-review producer (contract §10.13). A goal parks at
// blocked(needs_verdict) whenever a required judgment item has no valid verdict;
// for authority=agent items the dispatcher (scanAndReview) drives a reviewer
// agent here instead of waiting for a human. The consumer half — folding an
// authority=agent acceptance_event — already exists (judgment.go, acceptance.go);
// this file mints the review attempt and turns its output into those events.
//
// The goal STAYS blocked(needs_verdict) while the review runs: the review is a
// background producer, not an executing episode, and keeping active_attempt_id
// cleared lets evaluatedAttempt keep scoping verdicts to the execution output's
// hash. A passing review wakes the goal to accept; a failing one wakes it to
// rework with the reviewer rationale fed in as a gap; a review that cannot
// produce a verdict (or exhausts the small review budget) leaves the goal
// blocked(needs_verdict) for a human (the chosen degradation).

// BeginReview mints a headless purpose=review attempt for a goal parked at
// blocked(needs_verdict) with a pending authority=agent item, and enqueues its
// durable River job in ONE tx (mirrors BeginAutoDecomposition). It does NOT move
// the goal's lifecycle — the goal stays blocked until the verdict folds in. A
// fresh hidden session is minted per review so the reviewer never reads the
// executor's working context. The dispatcher (scanAndReview) is the only caller;
// ErrInvalidTransition means "nothing to review" (no agent item pending, budget
// spent, or a race) and is ignored by the scan.
func (s *GoalService) BeginReview(ctx context.Context, id string, enqueue AttemptEnqueuer) (sqlc.AgentGoalAttempt, error) {
	d, err := getGoal(ctx, s.q, id)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}
	// Cheap pre-checks before minting a session: skip goals awaiting a human, with
	// no pending agent item, or out of review budget. Re-checked under the lock.
	_, execID, _, ok := s.pendingReviewWork(ctx, s.q, d)
	if !ok {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition
	}
	if spent, err := s.reviewBudgetSpent(ctx, s.q, d.ID, execID); err != nil {
		return sqlc.AgentGoalAttempt{}, err
	} else if spent >= defaultMaxReviewAttempts {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition // episode budget spent; degrade to human
	}

	// Mint the review session OUTSIDE the tx (it opens its own tx and would
	// self-deadlock against the held one). Reuse the worker-session minter: the
	// review runs as a fresh hidden task session, same as an execution attempt.
	if s.newSession == nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("goal: no worker session minter configured")
	}
	sessionID, err := s.newSession(ctx, d.UserID, d.AgentID, d.ProjectID.String)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("mint review session: %w", err)
	}

	var out sqlc.AgentGoalAttempt
	err = s.withTxRaw(ctx, func(q *sqlc.Queries, tx pgx.Tx) error {
		if err := q.LockGoalForWrite(ctx, d.ID); err != nil {
			return fmt.Errorf("lock goal for review attempt: %w", err)
		}
		// Re-read + re-derive under the lock: a racing verdict (human or a prior
		// review) or a new execution attempt may have moved the goal since the scan.
		cur, err := getGoal(ctx, q, d.ID)
		if err != nil {
			return err
		}
		items, execID, execOut, ok := s.pendingReviewWork(ctx, q, cur)
		if !ok {
			return ErrInvalidTransition
		}
		if spent, err := s.reviewBudgetSpent(ctx, q, cur.ID, execID); err != nil {
			return err
		} else if spent >= defaultMaxReviewAttempts {
			return ErrInvalidTransition
		}
		// attempt_no stays the global per-purpose sequence (uniq_agent_goal_attempt_no
		// is on goal+purpose+attempt_no); only the budget is per-episode.
		maxNo, err := q.GetMaxAttemptNo(ctx, sqlc.GetMaxAttemptNoParams{GoalID: cur.ID, Purpose: PurposeReview})
		if err != nil {
			return fmt.Errorf("max review attempt no: %w", err)
		}
		attemptNo := int(maxNo) + 1
		input := buildInputContext(cur, nil, nil, "", attemptNo)
		input.ReviewItems = items
		judged := execOut
		input.ReviewOutput = &judged
		input.ReviewedAttemptID = execID

		att, err := q.CreateAttempt(ctx, sqlc.CreateAttemptParams{
			ID:              newID(),
			GoalID:          cur.ID,
			UserID:          cur.UserID,
			AgentID:         pgnull.Text(cur.AgentID),
			ExecutorAgentID: pgnull.Text(cur.AgentID),
			SessionID:       sessionID,
			Purpose:         PurposeReview,
			AttemptNo:       int64(attemptNo),
			Status:          AttemptQueued,
			InputContext:    marshalJSON(input),
			// A claim-grace lease: the River worker heartbeats it forward, so an
			// expired lease is a genuine orphan the reaper recovers (mirrors Claim).
			// uniq_agent_goal_active_attempt (goal_id, purpose) keeps this to one
			// in-flight review attempt per goal — a racing second mint rolls back.
			LeaseExpiresAt: nullTime(s.nowTime().Add(claimGraceTTL)),
		})
		if err != nil {
			return fmt.Errorf("create review attempt: %w", err)
		}
		if enqueue != nil {
			if err := enqueue(ctx, tx, cur.ID, att.ID); err != nil {
				return fmt.Errorf("enqueue review attempt: %w", err)
			}
		}
		out = att
		return nil
	})
	return out, err
}

// pendingReviewWork reports whether a goal needs an agent verdict and, if so, the
// pending authority=agent items, the execution attempt whose output is judged,
// and that output. ok is false unless the goal is blocked(needs_verdict) with at
// least one pending agent item — a human-only or already-resolved goal is left
// for its existing path. Pure of writes; safe to call with s.q or a tx querier.
func (s *GoalService) pendingReviewWork(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) ([]AcceptanceItem, string, AttemptOutput, bool) {
	if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockNeedsVerdict {
		return nil, "", AttemptOutput{}, false
	}
	var contract AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &contract)
	if len(contract.AgentJudgmentItems()) == 0 {
		return nil, "", AttemptOutput{}, false // no agent items: a human verdict path
	}
	// Skip if a review attempt is already in flight. The goal STAYS
	// blocked(needs_verdict) while the reviewer runs, so scanAndReview keeps
	// listing it every tick; without this guard each tick would mint a fresh
	// review session and only then collide on uniq_agent_goal_active_attempt,
	// leaking a session per tick and spamming the log. One in-flight review per
	// goal is enough -- its verdict folds the goal out of needs_verdict.
	if _, err := q.GetActiveAttempt(ctx, sqlc.GetActiveAttemptParams{GoalID: d.ID, Purpose: PurposeReview}); err == nil {
		return nil, "", AttemptOutput{}, false
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", AttemptOutput{}, false
	}
	execID, execOut := s.evaluatedAttempt(ctx, q, d)
	if execID == "" {
		// No submitted execution output to judge (e.g. a composite carrying an
		// agent judgment item). There is nothing for a reviewer to score against, so
		// leave it for a human verdict.
		return nil, "", AttemptOutput{}, false
	}
	events, err := q.ListAcceptanceEventByGoal(ctx, d.ID)
	if err != nil {
		return nil, "", AttemptOutput{}, false
	}
	items := PendingAgentReviewItems(contract, execOut.Hash, events)
	if len(items) == 0 {
		return nil, "", AttemptOutput{}, false // every agent item already has a valid verdict
	}
	return items, execID, execOut, true
}

// frozenReviewHash returns the hash of the execution output a review attempt was
// minted to judge, or "" when none was frozen.
func frozenReviewHash(in AttemptInput) string {
	if in.ReviewOutput == nil {
		return ""
	}
	return in.ReviewOutput.Hash
}

// reviewBudgetSpent counts agent-review attempts already spent on the CURRENT
// needs_verdict episode: reviews of execID (the execution attempt whose output is
// being judged) that actually ran. A rework that produces a newer execution
// output starts a fresh episode with full budget, so a healthy reviewer that
// returns a fail (legitimate rework) never starves later episodes — only a
// reviewer that keeps failing to produce a verdict against the SAME output is
// bounded, then degraded to a human (contract §10.13).
func (s *GoalService) reviewBudgetSpent(ctx context.Context, q *sqlc.Queries, goalID, execID string) (int, error) {
	exec, err := q.GetAttempt(ctx, execID)
	if err != nil {
		return 0, fmt.Errorf("review budget: load reviewed attempt: %w", err)
	}
	n, err := q.CountRanReviewAttemptsForOutput(ctx, sqlc.CountRanReviewAttemptsForOutputParams{
		GoalID: goalID,
		Since:  exec.CreatedAt,
	})
	if err != nil {
		return 0, fmt.Errorf("review budget: count attempts: %w", err)
	}
	return int(n), nil
}

// SubmitReview applies a reviewer agent's verdicts: it finalizes the review
// attempt (running->submitted), appends one authority=agent acceptance_event per
// verdict bound to the judged execution output's hash, and re-folds the ledger —
// all in ONE tx so the verdict-as-evidence and the resulting transition are
// atomic. The fold wakes the goal blocked(needs_verdict)->active and then accepts
// (all items pass) or reworks (a fail, whose rationale rides as a gap); a
// remaining human item keeps it blocked. It is the review analogue of Submit and
// the single durable transition the worker applies for a purpose=review attempt.
func (s *GoalService) SubmitReview(ctx context.Context, attemptID string, ev AttemptEvidence, verdicts []ReviewVerdict) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		att, err := q.GetAttempt(ctx, attemptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if att.Purpose != PurposeReview {
			return ErrInvalidTransition
		}
		// Re-derive the frozen episode the reviewer judged (mint-time input), and
		// re-validate coverage at this durable boundary — not only in-turn. The
		// verdicts must answer exactly the frozen required items so a non-tool
		// executor or a stale prompt can never append an unknown, duplicate, or
		// partial verdict set the fold would misread.
		var in AttemptInput
		_ = unmarshalJSON(att.InputContext, &in)
		if err := ValidateReviewVerdicts(verdicts, in.ReviewItems); err != nil {
			return fmt.Errorf("review verdicts reject: %w", err)
		}

		d, err := getGoal(ctx, q, att.GoalID)
		if err != nil {
			return err
		}
		// Finalize the review attempt so the reaper never recovers it after we fold.
		rows, err := q.SubmitAttempt(ctx, sqlc.SubmitAttemptParams{
			Evidence: marshalJSON(ev),
			Output:   emptyJSON,
			ID:       attemptID,
		})
		if err != nil {
			return fmt.Errorf("submit review attempt: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // not the running attempt (reaped / raced)
		}

		// Bind to the EXACT execution output the reviewer judged (frozen at mint),
		// not whatever evaluatedAttempt resolves now. If the evaluated output moved
		// since mint (a rework produced a newer execution attempt while this review
		// ran), the verdict is stale: the attempt is finalized above, but we do NOT
		// append it or fold. The goal stays blocked(needs_verdict) and the dispatcher
		// mints a fresh review against the new output (a new episode, full budget).
		curID, curOut := s.evaluatedAttempt(ctx, q, d)
		if in.ReviewedAttemptID == "" || curID != in.ReviewedAttemptID || curOut.Hash != frozenReviewHash(in) {
			return nil
		}
		for _, v := range verdicts {
			params := AgentVerdictEvent(d.ID, v.ItemID, in.ReviewedAttemptID, attemptID, curOut.Hash, v.Pass, v.Rationale)
			if _, err := s.appendAcceptanceEvent(ctx, q, params); err != nil {
				return fmt.Errorf("append agent verdict %q: %w", v.ItemID, err)
			}
		}
		return s.foldAndTransition(ctx, q, d.ID, in.ReviewedAttemptID, curOut)
	})
}
