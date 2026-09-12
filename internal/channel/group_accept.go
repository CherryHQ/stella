package channel

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/sessionexecution"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
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
	// Enqueued means the reply's outbox chain committed with the acceptance:
	// the platform send is already durable, so the publish step only confirms.
	Enqueued bool
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
// It takes no caller-side group state on purpose: the only state that may decide
// anything here is the row this transaction locks itself.
func (d *GroupDispatcher) acceptGroupResponse(ctx context.Context, row sqlc.CtxGroupDispatch, response groupResponse, turn memory.DeferredGroupTurn, ops []choutbox.Op) (groupAcceptOutcome, error) {
	if d.db == nil {
		return groupAcceptOutcome{}, errors.New("dispatcher db not configured")
	}
	if d.committer == nil {
		return groupAcceptOutcome{}, errors.New("group dispatcher requires memory.TxGroupCommitter")
	}
	tx, err := sessionexecution.Begin(ctx, d.db)
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
		return d.stopGroupTurn(ctx, tx, q, row, verdict, turn)
	}

	// Every platform, web included, is born undelivered: web's publisher is a
	// noop, so the same publish step flips it to 'delivered' microseconds later
	// in the same process. One lifecycle means recovery, failure compensation
	// and the peer wake are not three different stories per surface.
	result, err := eventlog.AppendToGroupWithQueries(ctx, q, row.GroupID, eventlog.GroupMessage{
		ActorType:        eventlog.ActorAgent,
		ActorID:          row.AgentID,
		ActorDisplayName: eventlog.NewParticipantNamer(q).Name(ctx, row.GroupID, string(eventlog.ActorAgent), row.AgentID),
		Content:          response.text,
		Reasoning:        response.reasoning,
		AgentSessionID:   response.sessionID,
		DeliveryState:    "pending",
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
	// The platform send intent commits inside the acceptance transaction too:
	// a crash afterwards produces an accepted reply with its outbox chain or
	// neither, never a reply with no durable send to recover. The ops were
	// prepared before this transaction began — including their file bytes — so
	// only the account binding and the write itself happen under the lock.
	// The dispatcher always carries a publish pipeline — there is no
	// accept-without-outbox construction, so an empty chain is the only
	// skip.
	enqueued := false
	if len(ops) > 0 {
		if err := d.publish.appendPreparedOps(ctx, tx, q, row, ops); err != nil {
			return groupAcceptOutcome{}, err
		}
		enqueued = true
	}
	if err := tx.Commit(ctx); err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("accept group response: commit: %w", err)
	}
	if d.events != nil {
		d.events.Announce(result)
	}
	return groupAcceptOutcome{Status: groupTurnAccepted, Accepted: result, Enqueued: enqueued}, nil
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
	switch _, err := q.GetLatestPeerGroupMessageWithContent(ctx, sqlc.GetLatestPeerGroupMessageWithContentParams{GroupID: row.GroupID, AgentID: row.AgentID, TriggerSeq: row.TriggerSeq, Content: response.text}); {
	case err == nil:
		// A duplicate is terminal, not a freshness yield. Counting it as a HOLD
		// would spend the causal-chain retry budget on work the agent must not
		// repeat, then let a later snapshot post through unexpectedly.
		return groupBackstop{status: groupTurnSilent, reason: "duplicate"}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		// A failed lookup is not evidence of no duplicate. Failing the accept
		// retries the turn; treating it as "unique" posts the echo for good.
		return groupBackstop{}, fmt.Errorf("check verbatim duplicate: %w", err)
	}
	// Hard caps shared with the pre-turn gate in exceedsGroupHardCap. The state
	// lock makes this second measurement authoritative against peer posts that
	// committed while this agent was thinking.
	chain, err := d.consecutiveAgentMessages(ctx, q, row.GroupID, maxGroupSequence)
	if err != nil {
		return groupBackstop{}, fmt.Errorf("count consecutive agent messages: %w", err)
	}
	lastHuman, err := q.LastHumanSeqAtOrBefore(ctx, sqlc.LastHumanSeqAtOrBeforeParams{GroupID: row.GroupID, TriggerSeq: row.TriggerSeq})
	if err != nil {
		return groupBackstop{}, fmt.Errorf("last human seq: %w", err)
	}
	posts, err := q.CountAgentPostsSinceSeq(ctx, sqlc.CountAgentPostsSinceSeqParams{GroupID: row.GroupID, AfterSeq: lastHuman})
	if err != nil {
		return groupBackstop{}, fmt.Errorf("count agent posts: %w", err)
	}
	if capped, reason := exceedsGroupHardCap(
		groupCapCheck{count: int64(chain), limit: int64(locked.AgentChainHardLimit)},
		groupCapCheck{count: posts, limit: int64(locked.MaxRepliesPerHumanTrigger)},
	); capped {
		return groupBackstop{status: groupTurnSilent, reason: reason}, nil
	}
	return groupTurnAccepts, nil
}

// stopGroupTurn records a stopped turn and commits the history it consumed, so
// every gate retires its row the same way and a new gate cannot invent a fifth
// exit path. A non-accepted reply is stale, but its tool effects and read cursor
// are real. Dropping only the final text response keeps that durable history
// consistent with the group message that was intentionally not published.
func (d *GroupDispatcher) stopGroupTurn(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, row sqlc.CtxGroupDispatch, verdict groupBackstop, turn memory.DeferredGroupTurn) (groupAcceptOutcome, error) {
	var (
		updated int64
		err     error
	)
	switch verdict.status {
	case groupTurnHeld:
		updated, err = q.MarkGroupDispatchHeld(ctx, sqlc.MarkGroupDispatchHeldParams{ID: row.ID, AttemptCount: row.AttemptCount, HeldUpToSeq: verdict.heldUpTo, Reason: verdict.reason})
	case groupTurnSilent:
		updated, err = q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: row.ID, AttemptCount: row.AttemptCount, Reason: verdict.reason})
	default:
		return groupAcceptOutcome{}, fmt.Errorf("stop group turn: unexpected status %q", verdict.status)
	}
	if err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("mark dispatch %s (%s): %w", verdict.status, verdict.reason, err)
	}
	if updated == 0 {
		return groupAcceptOutcome{}, fmt.Errorf("mark dispatch %s (%s): lost dispatch ownership", verdict.status, verdict.reason)
	}
	turn.OwnRows = stripTrailingTextOnlyAssistant(turn.OwnRows)
	if err := d.committer.CommitGroupTurn(ctx, q, turn); err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("commit stopped deferred group turn: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return groupAcceptOutcome{}, fmt.Errorf("commit %s dispatch (%s): %w", verdict.status, verdict.reason, err)
	}
	return groupAcceptOutcome{Status: verdict.status, Reason: verdict.reason}, nil
}

// dispatchResult is used serially by runtime, then read by the dispatcher after
// EOF. It owns only acceptance; the existing publish driver owns delivery.
type dispatchResult struct {
	dispatcher *GroupDispatcher
	row        sqlc.CtxGroupDispatch
	trigger    sqlc.CtxGroupMessage
	envelope   GroupOutboxEnvelope
	state      sqlc.CtxGroupState
	response   groupResponse
	outcome    groupAcceptOutcome
	used       int
	committed  bool
}

func (r *dispatchResult) Observe(event agentruntime.Event) error {
	return r.response.append(convertEvent(event), &r.used)
}

func (r *dispatchResult) Commit(ctx context.Context, turn memory.DeferredGroupTurn) error {
	if !turn.Complete {
		return errors.New("cannot commit an incomplete group turn")
	}
	r.response.sessionID = turn.Session.ID
	var err error
	if isModelPass(r.response.text) {
		err = r.dispatcher.retireModelPass(ctx, r.row, turn)
		r.outcome = groupAcceptOutcome{Status: groupTurnSilent, Reason: groupSilentModelPass}
	} else {
		// The full op chain — including immutable file bytes — is built before
		// the transaction: the accept step only fences and writes.
		ops, ferr := r.prepareReplyOps()
		if ferr != nil {
			return ferr
		}
		r.outcome, err = r.dispatcher.acceptGroupResponse(ctx, r.row, r.response, turn, ops)
	}
	r.committed = err == nil
	return err
}

// prepareReplyOps decomposes the accepted response into its durable outbox
// chain before the accept transaction opens: file bytes are read off their
// mutable workspace paths here (and only when the platform plans attachment
// ops at all — a file-less platform must not lose the text reply to an
// unreadable path), and the chain is marshaled with the account left open.
// appendPreparedOps binds the reply channel's registered account under the
// accept lock. Web produces no ops — its egress is the event log.
func (r *dispatchResult) prepareReplyOps() ([]choutbox.Op, error) {
	if r.state.Platform == webGroupPlatform {
		return nil, nil
	}
	plan := groupReplyPlan(r.state.Platform)
	if plan.Attachments {
		if err := readReplyFiles(r.response.events); err != nil {
			return nil, err
		}
	}
	payload := choutbox.GroupReplyPayload{
		V:                 choutbox.PayloadVersion,
		Platform:          r.state.Platform,
		PlatformGroupID:   r.state.PlatformGroupID,
		PlatformThreadID:  r.state.PlatformThreadID,
		ReplyTo:           nullStringValue(r.trigger.PlatformMessageID),
		DeliveryID:        r.row.ID,
		RequesterID:       r.trigger.ActorID,
		SessionID:         r.response.sessionID,
		Events:            r.response.events,
		LifecycleFeedback: r.envelope.LifecycleFeedback,
	}
	return choutbox.GroupReplyChain("group:"+r.row.ID, r.row.ReplyChannelID, "", r.row.GroupID, payload, r.response.events, plan)
}
