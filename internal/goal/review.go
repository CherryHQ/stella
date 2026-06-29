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
	if _, _, _, ok := s.pendingReviewWork(ctx, s.q, d); !ok {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition
	}
	spent, err := s.q.GetMaxAttemptNo(ctx, sqlc.GetMaxAttemptNoParams{GoalID: d.ID, Purpose: PurposeReview})
	if err != nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("max review attempt no: %w", err)
	}
	if int(spent) >= defaultMaxReviewAttempts {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition // review budget spent; degrade to human
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
		items, _, execOut, ok := s.pendingReviewWork(ctx, q, cur)
		if !ok {
			return ErrInvalidTransition
		}
		spent, err := q.GetMaxAttemptNo(ctx, sqlc.GetMaxAttemptNoParams{GoalID: cur.ID, Purpose: PurposeReview})
		if err != nil {
			return fmt.Errorf("max review attempt no: %w", err)
		}
		if int(spent) >= defaultMaxReviewAttempts {
			return ErrInvalidTransition
		}
		attemptNo := int(spent) + 1
		input := buildInputContext(cur, nil, nil, "", attemptNo)
		input.ReviewItems = items
		judged := execOut
		input.ReviewOutput = &judged

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
	execID, execOut := s.evaluatedAttempt(ctx, q, d)
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
		d, err := getGoal(ctx, q, att.GoalID)
		if err != nil {
			return err
		}
		// The verdict judges the current evaluated execution output. active_attempt_id
		// is cleared while blocked, so this resolves to the most recent submitted
		// execution attempt — its hash anchors every verdict's scope (§4.2).
		execID, execOut := s.evaluatedAttempt(ctx, q, d)

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
		for _, v := range verdicts {
			params := AgentVerdictEvent(d.ID, v.ItemID, execID, attemptID, execOut.Hash, v.Pass, v.Rationale)
			if _, err := s.appendAcceptanceEvent(ctx, q, params); err != nil {
				return fmt.Errorf("append agent verdict %q: %w", v.ItemID, err)
			}
		}
		return s.foldAndTransition(ctx, q, d.ID, execID, execOut)
	})
}
