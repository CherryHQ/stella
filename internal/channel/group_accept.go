package channel

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// The accept transaction is the server-side backstop of optimistic
// collaboration. Every member decides locally whether to speak, so this is the
// last place that can still stop a reply -- and the only place that sees the
// group under a lock, after the agent finished thinking. It re-checks what
// changed meanwhile, then either commits the reply together with the agent's
// session memory in one transaction, or retires the row with the gate that
// stopped it.

// groupAcceptStatus is how the accept transaction retired a dispatch row.
// It doubles as the wire value of the turn event, so these strings are UI.
type groupAcceptStatus string

const (
	groupTurnAccepted groupAcceptStatus = "accepted"
	// held rows stay claimable: the agent gets to re-run against the newer
	// context. silent rows are terminal -- the reply was simply not wanted.
	groupTurnHeld   groupAcceptStatus = "held"
	groupTurnSilent groupAcceptStatus = "silent"
)

// groupAcceptOutcome names which backstop stopped a reply. The caller reports
// Reason verbatim, so each gate must carry its own -- reporting a neighbouring
// gate's reason is indistinguishable from the backstop misfiring.
type groupAcceptOutcome struct {
	Status groupAcceptStatus
	Reason string
	// Accepted holds the appended message; valid only when Status is accepted.
	Accepted eventlog.AppendResult
}

// heldUpTo is set only by the freshness gate: it promises the agent that its
// next run starts at the peer message that caused this hold.
type groupBackstop struct {
	status   groupAcceptStatus
	reason   string
	heldUpTo pgtype.Int8
}

var groupTurnAccepts = groupBackstop{status: groupTurnAccepted}

// acceptGroupResponse commits a finished group turn under the group state lock,
// or retires the dispatch row with the backstop that stopped it. A non-accepted
// outcome is a normal result, not an error: the transaction still committed.
func (d *GroupDispatcher) acceptGroupResponse(ctx context.Context, row sqlc.CtxGroupDispatch, state sqlc.CtxGroupState, response groupResponse, turn memory.DeferredGroupTurn) (groupAcceptOutcome, error) {
	if d.db == nil {
		return groupAcceptOutcome{}, errors.New("dispatcher db not configured")
	}
	if d.committer == nil {
		return groupAcceptOutcome{}, errors.New("group dispatcher requires memory.TxGroupCommitter")
	}
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("accept group response: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)
	locked, err := q.GetGroupStateByIDForUpdate(ctx, row.GroupID)
	if err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("lock group state for accept: %w", err)
	}
	verdict, err := d.groupBackstopVerdict(ctx, q, row, locked, response)
	if err != nil {
		return groupAcceptOutcome{}, err
	}
	if verdict.status != groupTurnAccepted {
		return stopGroupTurn(ctx, tx, q, row, verdict)
	}

	deliveryState := "pending"
	if state.Platform == "web" {
		deliveryState = "delivered"
	}
	result, err := eventlog.AppendToGroupWithQueries(ctx, q, row.GroupID, eventlog.GroupMessage{
		ActorType:      eventlog.ActorAgent,
		ActorID:        row.AgentID,
		Content:        response.text,
		Reasoning:      response.reasoning,
		AgentSessionID: response.sessionID,
		DeliveryState:  deliveryState,
	})
	if err != nil {
		return groupAcceptOutcome{}, err
	}
	updated, err := q.SetGroupDispatchResultMessage(ctx, sqlc.SetGroupDispatchResultMessageParams{
		ID:              row.ID,
		AttemptCount:    row.AttemptCount,
		ResultMessageID: result.Message.ID,
	})
	if err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("set dispatch result message: %w", err)
	}
	if updated == 0 {
		return groupAcceptOutcome{}, errors.New("set dispatch result message: lost dispatch ownership")
	}
	// The session memory of this turn and the decision to publish it commit
	// together; a split would let an agent remember a reply nobody received.
	if err := d.committer.CommitGroupTurn(ctx, q, turn); err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("commit deferred group turn: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("accept group response: commit: %w", err)
	}
	if d.events != nil {
		d.events.Announce(result)
	}
	return groupAcceptOutcome{Status: groupTurnAccepted, Accepted: result}, nil
}

// groupBackstopVerdict runs the gates in cost order under the held state lock.
// It only reads: nothing is written until the caller acts on the verdict.
func (d *GroupDispatcher) groupBackstopVerdict(ctx context.Context, q *sqlc.Queries, row sqlc.CtxGroupDispatch, locked sqlc.CtxGroupState, response groupResponse) (groupBackstop, error) {
	// Freshness. A peer spoke while this agent was thinking, so the reply was
	// written against stale context -- unless the agent has already been held
	// its budget of times in this causal chain, at which point holding again
	// would silence it for good.
	held, err := q.CountHeldGroupDispatchesInChain(ctx, sqlc.CountHeldGroupDispatchesInChainParams{GroupID: row.GroupID, AgentID: row.AgentID, TriggerSeq: row.TriggerSeq})
	if err != nil {
		return groupBackstop{}, fmt.Errorf("count held dispatches: %w", err)
	}
	if held < int64(locked.HoldLimit) {
		peers, err := q.CountPeerMessagesAfterSeq(ctx, sqlc.CountPeerMessagesAfterSeqParams{GroupID: row.GroupID, AfterSeq: row.TriggerSeq, AgentID: row.AgentID})
		if err != nil {
			return groupBackstop{}, fmt.Errorf("count peer messages after snapshot: %w", err)
		}
		if peers > 0 {
			upTo, err := q.MaxPeerMessageSeqAfterSeq(ctx, sqlc.MaxPeerMessageSeqAfterSeqParams{GroupID: row.GroupID, AfterSeq: row.TriggerSeq, AgentID: row.AgentID})
			if err != nil {
				return groupBackstop{}, fmt.Errorf("max peer message seq: %w", err)
			}
			return groupBackstop{status: groupTurnHeld, reason: "freshness", heldUpTo: pgtype.Int8{Int64: upTo, Valid: true}}, nil
		}
	}
	// Verbatim echo. Two agents reaching the same sentence is the visible face
	// of optimistic collaboration; the second one adds nothing.
	if _, err := q.GetLatestPeerGroupMessageWithContent(ctx, sqlc.GetLatestPeerGroupMessageWithContentParams{GroupID: row.GroupID, AgentID: row.AgentID, Content: response.text}); err == nil {
		return groupBackstop{status: groupTurnHeld, reason: "duplicate"}, nil
	}
	// Hard cap. The number of agent replies one human message may provoke; the
	// last defence against a group talking to itself.
	lastHuman, err := q.LastHumanSeqAtOrBefore(ctx, sqlc.LastHumanSeqAtOrBeforeParams{GroupID: row.GroupID, TriggerSeq: row.TriggerSeq})
	if err != nil {
		return groupBackstop{}, fmt.Errorf("last human seq: %w", err)
	}
	posts, err := q.CountAgentPostsSinceSeq(ctx, sqlc.CountAgentPostsSinceSeqParams{GroupID: row.GroupID, AfterSeq: lastHuman})
	if err != nil {
		return groupBackstop{}, fmt.Errorf("count agent posts: %w", err)
	}
	if posts >= int64(locked.MaxRepliesPerHumanTrigger) {
		return groupBackstop{status: groupTurnSilent, reason: "hard_cap"}, nil
	}
	return groupTurnAccepts, nil
}

// stopGroupTurn records a stopped turn and commits, so every gate retires its
// row the same way and a new gate cannot invent a fifth exit path.
func stopGroupTurn(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, row sqlc.CtxGroupDispatch, verdict groupBackstop) (groupAcceptOutcome, error) {
	var err error
	switch verdict.status {
	case groupTurnHeld:
		_, err = q.MarkGroupDispatchHeld(ctx, sqlc.MarkGroupDispatchHeldParams{ID: row.ID, AttemptCount: row.AttemptCount, HeldUpToSeq: verdict.heldUpTo})
	case groupTurnSilent:
		_, err = q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: row.ID, AttemptCount: row.AttemptCount, Reason: verdict.reason})
	default:
		return groupAcceptOutcome{}, fmt.Errorf("stop group turn: unexpected status %q", verdict.status)
	}
	if err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("mark dispatch %s (%s): %w", verdict.status, verdict.reason, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("commit %s dispatch (%s): %w", verdict.status, verdict.reason, err)
	}
	return groupAcceptOutcome{Status: verdict.status, Reason: verdict.reason}, nil
}
