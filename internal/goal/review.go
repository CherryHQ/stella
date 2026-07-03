package goal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

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
// rework with the reviewer rationale fed in as a gap (a composite, which cannot
// rework, parks back at needs_verdict for a human instead); a review that cannot
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
	_, execID, _, ok, err := s.pendingReviewWork(ctx, s.q, d)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, err
	}
	if !ok {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition
	}
	if spent, err := s.reviewBudgetSpent(ctx, s.q, d.ID, execID); err != nil {
		return sqlc.AgentGoalAttempt{}, err
	} else if spent >= defaultMaxReviewAttempts {
		return sqlc.AgentGoalAttempt{}, ErrInvalidTransition
	}

	if s.newSession == nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("goal: no worker session minter configured")
	}
	sessionID, err := s.newSession(ctx, d.UserID, d.AgentID, d.ProjectID.String)
	if err != nil {
		return sqlc.AgentGoalAttempt{}, fmt.Errorf("mint review session: %w", err)
	}

	out, err := s.beginAttempt(ctx, id, attemptSpec{
		purpose:       PurposeReview,
		sessionID:     sessionID,
		executorAgent: d.AgentID,
		lease:         nullTime(s.nowTime().Add(claimGraceTTL)),
		enqueue:       enqueue,
		prepare: func(ctx context.Context, q *sqlc.Queries, cur sqlc.AgentGoal, attemptNo int) (AttemptInput, error) {
			items, execID, execOut, ok, err := s.pendingReviewWork(ctx, q, cur)
			if err != nil {
				return AttemptInput{}, err
			}
			if !ok {
				return AttemptInput{}, ErrInvalidTransition
			}
			if spent, err := s.reviewBudgetSpent(ctx, q, cur.ID, execID); err != nil {
				return AttemptInput{}, err
			} else if spent >= defaultMaxReviewAttempts {
				return AttemptInput{}, ErrInvalidTransition
			}
			timeline, err := s.recentTimelineContext(ctx, q, cur.ID)
			if err != nil {
				return AttemptInput{}, err
			}
			input := buildInputContext(cur, nil, nil, timeline, "", attemptNo)
			input.ReviewItems = items
			judged := execOut
			input.ReviewOutput = &judged
			input.ReviewedAttemptID = execID
			return input, nil
		},
	})
	s.disposeOnRollback(ctx, err, d.UserID, d.AgentID, sessionID)
	return out, err
}

// pendingReviewWork reports whether a goal needs an agent verdict and, if so, the
// pending authority=agent items, the execution attempt whose output is judged
// ("" for a composite, whose judged evidence is its children's accepted outputs),
// and that output. ok is false (err nil) when the goal legitimately has no agent
// review to do — awaiting a human, no pending agent item, no output to judge, or a
// review already in flight; the caller skips it. A non-nil err is a real DB
// failure: it must NOT be flattened to "nothing to review" (a bad connection would
// masquerade as a healthy skip and the goal would silently never get reviewed),
// so it bubbles up to be logged and retried next tick. Pure of writes; safe to
// call with s.q or a tx querier.
func (s *GoalService) pendingReviewWork(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) ([]AcceptanceItem, string, AttemptOutput, bool, error) {
	if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockNeedsVerdict {
		return nil, "", AttemptOutput{}, false, nil
	}
	var contract AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &contract)
	if len(contract.AgentJudgmentItems()) == 0 {
		return nil, "", AttemptOutput{}, false, nil // no agent items: a human verdict path
	}
	// Skip if a review attempt is already in flight. The goal STAYS
	// blocked(needs_verdict) while the reviewer runs, so scanAndReview keeps
	// listing it every tick; without this guard each tick would mint a fresh
	// review session and only then collide on uniq_agent_goal_active_attempt,
	// leaking a session per tick and spamming the log. One in-flight review per
	// goal is enough -- its verdict folds the goal out of needs_verdict.
	if _, err := q.GetActiveAttempt(ctx, sqlc.GetActiveAttemptParams{GoalID: d.ID, Purpose: PurposeReview}); err == nil {
		return nil, "", AttemptOutput{}, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", AttemptOutput{}, false, fmt.Errorf("check in-flight review: %w", err)
	}
	execID, execOut := s.evaluatedAttempt(ctx, q, d)
	foldHash := execOut.Hash
	if execID == "" {
		// A composite carries no execution output: it reached needs_verdict through
		// RollupAccept, so its judged evidence is its children's frozen accepted
		// outputs. The fold scopes composite verdicts to the empty hash
		// (evaluatedAttempt resolves ""), matching human verdicts on composites —
		// evid.Hash below only fences the frozen evidence at SubmitReview, it is
		// never a verdict scope_hash. A leaf with no submitted output has nothing
		// for a reviewer to score against and stays with a human.
		evid, ok, err := s.compositeReviewEvidence(ctx, q, d)
		if err != nil || !ok {
			return nil, "", AttemptOutput{}, false, err
		}
		execOut = evid
		foldHash = ""
	}
	events, err := q.ListAcceptanceEventByGoal(ctx, d.ID)
	if err != nil {
		return nil, "", AttemptOutput{}, false, fmt.Errorf("list acceptance events: %w", err)
	}
	items := PendingAgentReviewItems(contract, foldHash, events)
	if len(items) == 0 {
		return nil, "", AttemptOutput{}, false, nil // every agent item already has a valid verdict
	}
	return items, execID, execOut, true, nil
}

// compositeReviewEvidence synthesizes the output a reviewer judges for a
// composite: the frozen accepted output of every accepted child (a composite
// produces no work of its own — its deliverable IS its children's). Hash
// fingerprints that evidence (child ids + output hashes, plan order) so
// SubmitReview can drop a verdict whose evidence moved between mint and submit;
// it is NOT the verdict scope_hash — the composite fold scopes verdicts to ""
// exactly like human ones (§4.2, TestCompositeRollup_AuthoredContractGate).
// ok=false for a leaf or a composite with no accepted children (nothing
// judgeable; leave it for a human).
func (s *GoalService) compositeReviewEvidence(ctx context.Context, q *sqlc.Queries, d sqlc.AgentGoal) (AttemptOutput, bool, error) {
	if d.Kind != KindComposite {
		return AttemptOutput{}, false, nil
	}
	kids, err := childAcceptedOutputs(ctx, q, d.ID)
	if err != nil {
		return AttemptOutput{}, false, fmt.Errorf("collect child outputs for review: %w", err)
	}
	if len(kids) == 0 {
		return AttemptOutput{}, false, nil
	}
	h := sha256.New()
	for _, k := range kids {
		writeNUL(h, k.GoalID, k.Hash)
	}
	return AttemptOutput{
		Summary: fmt.Sprintf("This is a composite goal: its work was carried out by %d accepted subtask(s). Judge the criteria against their frozen outputs in the structured result.", len(kids)),
		Result:  map[string]any{"children": kids},
		Hash:    hex.EncodeToString(h.Sum(nil)),
	}, true, nil
}

// frozenReviewHash returns the hash of the execution output a review attempt was
// minted to judge, or "" when none was frozen.
func frozenReviewHash(in AttemptInput) string {
	if in.ReviewOutput == nil {
		return ""
	}
	return in.ReviewOutput.Hash
}

// reviewItemsStillCurrent reports whether every frozen review item is still a
// required authority=agent judgment item in the goal's CURRENT contract. The fold
// resolves verdicts against the live contract by item id alone, so a goal edit
// that retyped an item (agent->human), made it non-required, or dropped it would
// otherwise let a stale agent verdict satisfy a gate it no longer owns. Checked
// under the goal lock at SubmitReview so the verdict binds to the contract the
// reviewer was actually asked about.
func reviewItemsStillCurrent(d sqlc.AgentGoal, frozen []AcceptanceItem) bool {
	var c AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &c)
	cur := make(map[string]struct{})
	for _, it := range c.AgentJudgmentItems() {
		cur[it.ID] = struct{}{}
	}
	for _, it := range frozen {
		if _, ok := cur[it.ID]; !ok {
			return false
		}
	}
	return true
}

// reviewBudgetSpent counts agent-review attempts already spent on the CURRENT
// needs_verdict episode: reviews of execID (the execution attempt whose output is
// being judged) that actually ran. A rework that produces a newer execution
// output starts a fresh episode with full budget, so a healthy reviewer that
// returns a fail (legitimate rework) never starves later episodes — only a
// reviewer that keeps failing to produce a verdict against the SAME output is
// bounded, then degraded to a human (contract §10.13).
func (s *GoalService) reviewBudgetSpent(ctx context.Context, q *sqlc.Queries, goalID, execID string) (int, error) {
	// A composite episode has no reviewed execution attempt (execID ""), and its
	// evidence — the children's frozen accepted outputs — never moves (accepted is
	// terminal). The episode is therefore the goal's whole review history: a zero
	// Since counts every review that ran.
	var since time.Time
	if execID != "" {
		exec, err := q.GetAttempt(ctx, execID)
		if err != nil {
			return 0, fmt.Errorf("review budget: load reviewed attempt: %w", err)
		}
		since = exec.CreatedAt
	}
	n, err := q.CountRanReviewAttemptsForOutput(ctx, sqlc.CountRanReviewAttemptsForOutputParams{
		GoalID: goalID,
		Since:  since,
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

		// Lock the goal so the staleness checks below see a stable snapshot: a
		// concurrent human verdict, rework-claim, or goal edit cannot move the goal
		// (or its evaluated output / contract) between the reads here and the fold.
		// Mirrors BeginReview / ApprovePlan, which also lock + re-read under the lock.
		if err := q.LockGoalForWrite(ctx, att.GoalID); err != nil {
			return fmt.Errorf("lock goal for review submit: %w", err)
		}
		d, err := getGoal(ctx, q, att.GoalID)
		if err != nil {
			return err
		}
		// Finalize the review attempt so the reaper never recovers it after we fold.
		rows, err := s.submitAttempt(ctx, q, att, ev, emptyJSON)
		if err != nil {
			return fmt.Errorf("submit review attempt: %w", err)
		}
		if rows == 0 {
			return ErrInvalidTransition // not the running attempt (reaped / raced)
		}

		// Drop the verdict (attempt already finalized above) unless the goal still
		// awaits exactly the episode the reviewer judged:
		//   - still blocked(needs_verdict): a concurrent verdict/rework may have woken
		//     it; folding now could move a goal that is no longer awaiting this verdict.
		//   - evaluated output unchanged: a rework that produced a newer execution
		//     attempt while this review ran makes the verdict stale (bind to the EXACT
		//     output frozen at mint, not whatever evaluatedAttempt resolves now).
		//   - frozen items are still required authority=agent items: a goal edit that
		//     retyped an item (agent->human), dropped it, or made it non-required must
		//     not let a stale agent verdict satisfy a different (or human) gate.
		// On any mismatch the goal stays blocked(needs_verdict) and the dispatcher
		// mints a fresh review against the current output (a new episode, full budget).
		if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockNeedsVerdict {
			return nil
		}
		curID, curOut := s.evaluatedAttempt(ctx, q, d)
		if in.ReviewedAttemptID == "" {
			// A composite review judged the children's frozen outputs, not an
			// execution attempt. Re-derive that evidence and drop the verdict if it
			// moved (or the goal is not actually a composite — a malformed freeze).
			// Composite verdicts fold against the empty hash, same as human ones.
			evid, ok, err := s.compositeReviewEvidence(ctx, q, d)
			if err != nil {
				return err
			}
			if !ok || curID != "" || evid.Hash != frozenReviewHash(in) {
				return nil
			}
			curOut = AttemptOutput{}
		} else if curID != in.ReviewedAttemptID || curOut.Hash != frozenReviewHash(in) {
			return nil
		}
		if !reviewItemsStillCurrent(d, in.ReviewItems) {
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
