package deliverable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
func buildInputContext(d sqlc.AgentDlvDeliverable, upstream []AcceptedOutput, priorGaps *Evaluation, resolvedVerdict string, attemptNo int) AttemptInput {
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

// upstreamAcceptedOutputs reads a deliverable's edges and returns the frozen
// AcceptedOutput of every upstream that is already accepted (only accepted
// output flows downstream, §3.3). A non-accepted upstream contributes nothing —
// the readiness gate, not this collector, decides whether a missing upstream
// blocks the claim.
func upstreamAcceptedOutputs(ctx context.Context, q *sqlc.Queries, deliverableID string) ([]AcceptedOutput, error) {
	edges, err := q.ListEdgeWithUpstreamState(ctx, deliverableID)
	if err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}
	var outs []AcceptedOutput
	for _, e := range edges {
		if e.UpstreamLifecycle != LifecycleAccepted || !e.UpstreamOutput.Valid {
			continue
		}
		var ao AcceptedOutput
		if err := unmarshalJSON(e.UpstreamOutput.String, &ao); err != nil {
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
func (s *DeliverableService) priorGapsFor(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable) (*Evaluation, error) {
	if d.AttemptCount == 0 {
		return nil, nil
	}
	prev := PurposeExecution
	atts, err := q.ListAttemptByDeliverable(ctx, sqlc.ListAttemptByDeliverableParams{
		DeliverableID: d.ID,
		Purpose:       prev,
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
// attempt_count. uniq_agent_dlv_active_attempt enforces ≤1 active attempt per
// (deliverable, purpose); a lost race surfaces as ErrInvalidTransition.
//
// resolvedVerdict carries a human answer that unblocked a needs_verdict (it
// rides in the frozen input). executorAgentID is the claim-time-resolved
// executor ("" leaves it NULL).
func (s *DeliverableService) mintNextAttempt(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable, executorAgentID, resolvedVerdict string) (sqlc.AgentDlvAttempt, error) {
	upstream, err := upstreamAcceptedOutputs(ctx, q, d.ID)
	if err != nil {
		return sqlc.AgentDlvAttempt{}, err
	}
	prior, err := s.priorGapsFor(ctx, q, d)
	if err != nil {
		return sqlc.AgentDlvAttempt{}, err
	}

	attemptNo := int(d.AttemptCount) + 1
	input := buildInputContext(d, upstream, prior, resolvedVerdict, attemptNo)

	att, err := q.CreateAttempt(ctx, sqlc.CreateAttemptParams{
		ID:              newID(),
		DeliverableID:   d.ID,
		UserID:          d.UserID,
		AgentID:         nullStr(d.AgentID),
		ExecutorAgentID: nullStr(executorAgentID),
		SessionID:       d.SessionID,
		Purpose:         PurposeExecution,
		AttemptNo:       int64(attemptNo),
		Status:          AttemptQueued,
		InputContext:    marshalJSON(input),
		LeaseExpiresAt:  nullStr(s.now()),
	})
	if err != nil {
		return sqlc.AgentDlvAttempt{}, fmt.Errorf("create attempt: %w", err)
	}

	rows, err := q.ClaimDeliverable(ctx, sqlc.ClaimDeliverableParams{
		ActiveAttemptID: nullStr(att.ID),
		ID:              d.ID,
	})
	if err != nil {
		return sqlc.AgentDlvAttempt{}, fmt.Errorf("claim deliverable: %w", err)
	}
	if rows == 0 {
		// ready→active guard failed (already claimed / not ready) — the unique
		// active-attempt index would also reject; roll the tx back.
		return sqlc.AgentDlvAttempt{}, ErrInvalidTransition
	}
	return att, nil
}

// Claim is the dispatcher's leaf ready→active (contract §2.1, §5 step 1). It
// resolves the dispatch hint's executor, mints a queued execution attempt and
// claims the deliverable in one tx. The per-root/per-user concurrency caps are
// enforced by the dispatcher BEFORE Claim (§5 step 2); Claim itself only guards
// the single-writer invariants.
func (s *DeliverableService) Claim(ctx context.Context, id, workerID string) (sqlc.AgentDlvAttempt, error) {
	var out sqlc.AgentDlvAttempt
	err := s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, id)
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
		out = att
		return nil
	})
	return out, err
}

// dispatchExecutor extracts the executor override from a deliverable's
// dispatch_hint ({"executor_agent_id": ...}); "" when unset (the worker resolves
// a default executor).
func dispatchExecutor(d sqlc.AgentDlvDeliverable) string {
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
// deliverable is a retryable protocol miss (ErrInvalidEvidence). The fold runs
// in the SAME tx so submit→evaluate→transition is atomic.
func (s *DeliverableService) Submit(ctx context.Context, attemptID string, ev AttemptEvidence, out AttemptOutput) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		att, err := q.GetAttempt(ctx, attemptID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		d, err := getDeliverable(ctx, q, att.DeliverableID)
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
			Evidence:   marshalJSON(ev),
			Output:     marshalJSON(out),
			RevisionID: sql.NullString{},
			ID:         attemptID,
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

// applyAcceptance is the re-fold entry point used after a verdict arrives or a
// check event is appended outside Submit (contract §4.3). It opens its own tx,
// derives the projection over the full ledger and applies exactly one transition.
// The hot path (Submit) calls foldAndTransition directly inside its tx.
//
// It resolves the evaluated attempt's output even when the active pointer was
// cleared by a needs_verdict block, so a verdict's scope_hash still anchors; the
// wake out of blocked(needs_verdict) happens inside foldAndTransition, gated on the
// fold reaching a real transition (never on the pending branch — see there).
func (s *DeliverableService) applyAcceptance(ctx context.Context, deliverableID string) error {
	return s.withTx(ctx, func(q *sqlc.Queries) error {
		d, err := getDeliverable(ctx, q, deliverableID)
		if err != nil {
			return err
		}
		attemptID, out := s.evaluatedAttempt(ctx, q, d)
		return s.foldAndTransition(ctx, q, deliverableID, attemptID, out)
	})
}

// evaluatedAttempt returns the attempt whose output the current fold scopes to and
// its decoded output: the active attempt when one is pointed, else the most recent
// SUBMITTED execution attempt — the output a human reviewed after blockForVerdict
// cleared the active pointer. ("", AttemptOutput{}) when none, so a "" hash forces
// any scoped verdict to re-request (no output ⇒ no valid pass, §4.2).
func (s *DeliverableService) evaluatedAttempt(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable) (string, AttemptOutput) {
	if id := d.ActiveAttemptID.String; id != "" {
		att, err := q.GetAttempt(ctx, id)
		if err != nil {
			return "", AttemptOutput{}
		}
		return id, decodeOutput(att.Output)
	}
	atts, err := q.ListAttemptByDeliverable(ctx, sqlc.ListAttemptByDeliverableParams{
		DeliverableID: d.ID,
		Purpose:       PurposeExecution,
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
func decodeOutput(s string) AttemptOutput {
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
//   - passed       → acceptDeliverable (freeze output, clear attempt, bump parent).
//   - failed + budget left → write gaps on the attempt; the dispatcher mints the
//     next attempt (rework = next attempt, not a node).
//   - failed + budget out  → blocked(budget_exhausted) | abandoned | rejected_final.
//   - NeedsVerdict → blocked(needs_verdict) (a pending human is not an executing
//     episode, so the active attempt is cleared).
//
// A verdict resolving a blocked(needs_verdict) deliverable re-enters here from
// applyAcceptance. The passed/failed branches wake it blocked→active first (their
// transitions guard from 'active'); the NeedsVerdict branch is idempotent when
// already blocked; the pending branch must NOT wake — waking into a still-pending
// fold would strand it 'active' with no attempt and no recovery route, so a
// blocked deliverable whose fold is still pending stays safely blocked.
func (s *DeliverableService) foldAndTransition(ctx context.Context, q *sqlc.Queries, deliverableID, attemptID string, out AttemptOutput) error {
	d, err := getDeliverable(ctx, q, deliverableID)
	if err != nil {
		return err
	}
	var contract AcceptanceContract
	_ = unmarshalJSON(d.AcceptanceContract, &contract)

	events, err := q.ListAcceptanceEventByDeliverable(ctx, deliverableID)
	if err != nil {
		return fmt.Errorf("list acceptance events: %w", err)
	}
	proj := DeriveAcceptance(contract, out.Hash, events)

	// Persist the projection under the stale fence. seq = #events folded; the
	// fence rejects a fold computed against an out-of-date seq.
	if _, err := q.SetDeliverableAcceptanceState(ctx, sqlc.SetDeliverableAcceptanceStateParams{
		AcceptanceState: proj.State,
		AcceptanceSeq:   int64(len(events)),
		ID:              deliverableID,
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
		// blocked deliverable stays blocked — do NOT wake into a strand.
		return nil
	}
}

// wakeIfBlocked promotes a deliverable parked in blocked(needs_verdict) back to
// active so a terminal fold branch (acceptLeaf/branchOnFailure) — which guards
// from 'active', the same from-state Submit folds against — can fire once a
// verdict resolves the block. A no-op when not so blocked. It mutates the passed
// deliverable's lifecycle so a subsequent in-Go guard reads the woken state.
func (s *DeliverableService) wakeIfBlocked(ctx context.Context, q *sqlc.Queries, d *sqlc.AgentDlvDeliverable) error {
	if d.Lifecycle != LifecycleBlocked || d.BlockReason != BlockNeedsVerdict {
		return nil
	}
	rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
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

// acceptLeaf freezes the accepted output, flips the deliverable → accepted, and
// bumps the parent's required_accepted in the SAME tx (contract §2.1
// active→accepted, §6 rollup). Downstream readiness is re-derived lazily by the
// dispatcher off the upstream-accepted index, so no push is needed here.
func (s *DeliverableService) acceptLeaf(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable, attemptID string, out AttemptOutput) error {
	// Already at the desired terminal state: a re-fold of an accepted leaf (e.g. a
	// verdict re-submitted after acceptance, or a double-clicked Approve whose
	// first request already accepted) derives passed again. That is the steady
	// state, not a conflict — return nil rather than a 0-row AcceptDeliverable
	// (which guards from 'active') so the verdict path is idempotent and the
	// parent counter is bumped exactly once. Mirrors blockForVerdict's guard.
	if d.Lifecycle == LifecycleAccepted {
		return nil
	}
	accepted := AcceptedOutput{
		DeliverableID: d.ID,
		Summary:       out.Summary,
		Result:        out.Result,
		Hash:          out.Hash,
		AcceptedAt:    s.now(),
		SourceAttempt: attemptID,
	}
	rows, err := q.AcceptDeliverable(ctx, sqlc.AcceptDeliverableParams{
		AcceptedOutput: nullStr(marshalJSON(accepted)),
		ID:             d.ID,
	})
	if err != nil {
		return fmt.Errorf("accept deliverable: %w", err)
	}
	if rows == 0 {
		return ErrInvalidTransition // not active (raced)
	}
	return s.bumpParentCounter(ctx, q, d, counterAccepted)
}

// blockForVerdict parks a deliverable awaiting a required human verdict:
// active→blocked(needs_verdict), clearing the active attempt (a pending human is
// not an executing episode, contract §2.1).
func (s *DeliverableService) blockForVerdict(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable) error {
	// Already parked awaiting this verdict (a multi-item contract where a verdict
	// resolved one item but another judgment item is still pending): the desired
	// end-state IS blocked(needs_verdict), so this is an idempotent no-op rather
	// than a 0-row BlockDeliverable (which guards from ready/active, not blocked).
	if d.Lifecycle == LifecycleBlocked && d.BlockReason == BlockNeedsVerdict {
		return nil
	}
	rows, err := q.BlockDeliverable(ctx, sqlc.BlockDeliverableParams{
		BlockReason: BlockNeedsVerdict,
		ID:          d.ID,
	})
	if err != nil {
		return fmt.Errorf("block deliverable: %w", err)
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
func (s *DeliverableService) branchOnFailure(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable, attemptID string, gaps Evaluation) error {
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
		// Budget left: clear the active attempt so the deliverable returns to ready
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

// reopenForRework returns a failed-with-budget deliverable to ready for the next
// attempt: clear the active attempt and move active→ready. The current attempt
// stays 'submitted' with its gaps recorded; the new attempt carries them.
func (s *DeliverableService) reopenForRework(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable) error {
	if err := q.ClearDeliverableActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
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

// blockBudget parks an exhausted deliverable: active→blocked(budget_exhausted),
// awaiting a human Reattempt (raise budget) or Abandon (contract §2.1).
func (s *DeliverableService) blockBudget(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable) error {
	rows, err := q.BlockDeliverable(ctx, sqlc.BlockDeliverableParams{
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
func (s *DeliverableService) transition(ctx context.Context, q *sqlc.Queries, d sqlc.AgentDlvDeliverable, to, blockReason string, counter counterKind) error {
	if err := q.ClearDeliverableActiveAttempt(ctx, d.ID); err != nil {
		return fmt.Errorf("clear active attempt: %w", err)
	}
	rows, err := q.TransitionDeliverableLifecycle(ctx, sqlc.TransitionDeliverableLifecycleParams{
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

// judgmentOnlyContract reports whether a deliverable's contract has no required
// deterministic item — a pure-judgment contract has no rework path on a failed
// verdict, so budget exhaustion is rejected_final rather than recoverable.
func judgmentOnlyContract(d sqlc.AgentDlvDeliverable) bool {
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
