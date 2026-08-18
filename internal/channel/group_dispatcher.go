package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	defaultGroupDispatchLease       = 5 * time.Minute
	defaultGroupDispatchPoll        = 2 * time.Second
	defaultGroupDispatchMaxAttempts = int64(3)
	// global pool; per-(group, agent) serialization remains enforced by the
	// durable claim and session queue. Raise only after measured saturation.
	defaultGroupDispatchWorkers = 8
	// groupReplyBufferBytes bounds the complete result retained between model
	// completion and egress. Raise only after adding BlobStore spooling.
	defaultGroupReplyBufferBytes = 8 << 20
)

type dispatchChatFunc func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error)

// GroupDispatcher materializes durable group response decisions and executes
// one selected-agent dispatch at a time. Ingest owns facts; this owns work.
type GroupDispatcher struct {
	db         *pgxpool.Pool
	q          *sqlc.Queries
	coord      *Coordinator
	publishers *PublisherRegistry
	log        *slog.Logger

	leaseDuration    time.Duration
	pollInterval     time.Duration
	maxAttempts      int64
	wakeC            chan struct{}
	dispatchC        chan sqlc.CtxGroupDispatch
	workerCount      int
	queue            *sessionQueue
	chat             dispatchChatFunc
	committer        memory.TxGroupCommitter
	events           *GroupEventHub
	triage           GroupTriage
	replyBufferBytes int
}

// SetGroupTriage installs the per-agent fast-model decision boundary.
func (d *GroupDispatcher) SetGroupTriage(triage GroupTriage) { d.triage = triage }

// Coordination bundles the coordinator and its durable group dispatcher. The
// channel domain builds them together and closes the coordinator<->dispatcher
// cycle, so the composition root does not assemble the cycle by hand.
type Coordination struct {
	// Coordinator is the channel MessageHandler for all channels.
	Coordinator *Coordinator
	// GroupDispatcher is the durable group-dispatch runner. The HTTP layer needs
	// only this narrow port, not the whole coordinator.
	GroupDispatcher *GroupDispatcher
}

// NewCoordination constructs the coordinator and its group dispatcher together
// and closes the coordinator<->dispatcher cycle. The dispatcher reuses the
// coordinator's publisher registry (supplied via WithPublisherRegistry). The
// composition root receives the coordinator (as the channel Handler) and the
// narrow GroupDispatcher port without wiring the cycle itself.
func NewCoordination(
	db *pgxpool.Pool,
	pm interface {
		agent.ServiceManager
		userInvalidator
	},
	store config.Store,
	listFn func() []pkgchannel.ModelOption,
	switchFn func(provider, model string) error,
	opts ...CoordinatorOption,
) Coordination {
	coord := NewCoordinator(pm, store, listFn, switchFn, opts...)
	gd := NewGroupDispatcher(db, coord, nil)
	coord.SetGroupDispatcher(gd)
	return Coordination{Coordinator: coord, GroupDispatcher: gd}
}

func NewGroupDispatcher(db *pgxpool.Pool, coord *Coordinator, publishers *PublisherRegistry) *GroupDispatcher {
	if publishers == nil && coord != nil {
		publishers = coord.publisherRegistry
	}
	d := &GroupDispatcher{
		db:               db,
		q:                sqlc.New(db),
		coord:            coord,
		publishers:       publishers,
		log:              slog.With("component", "group_dispatcher"),
		leaseDuration:    defaultGroupDispatchLease,
		pollInterval:     defaultGroupDispatchPoll,
		maxAttempts:      defaultGroupDispatchMaxAttempts,
		wakeC:            make(chan struct{}, 1),
		dispatchC:        make(chan sqlc.CtxGroupDispatch, 25),
		workerCount:      defaultGroupDispatchWorkers,
		replyBufferBytes: defaultGroupReplyBufferBytes,
		queue:            newSessionQueue(),
	}
	d.chat = d.chatDispatch
	return d
}

// SetGroupTurnCommitter supplies the production memory capability that commits
// a deferred group turn inside the dispatcher's acceptance transaction.
func (d *GroupDispatcher) SetGroupTurnCommitter(committer memory.TxGroupCommitter) {
	if d != nil {
		d.committer = committer
	}
}

// SetGroupEventHub attaches the channel-owned post-commit projection feed.
func (d *GroupDispatcher) SetGroupEventHub(events *GroupEventHub) {
	if d != nil {
		d.events = events
	}
}

// ValidateStartup fails closed when the dispatcher could otherwise accept a
// group post without atomically committing its agent history.
func (d *GroupDispatcher) ValidateStartup() error {
	if d == nil || d.committer == nil {
		return errors.New("group dispatcher requires memory.TxGroupCommitter")
	}
	return nil
}

func (d *GroupDispatcher) Wake() {
	if d == nil {
		return
	}
	select {
	case d.wakeC <- struct{}{}:
	default:
	}
}

func (d *GroupDispatcher) startHeartbeat(ctx context.Context, label, id string, extend func(context.Context, time.Time) (int64, error), onLost func()) func() {
	if d == nil || d.leaseDuration <= 0 || extend == nil {
		return func() {}
	}
	interval := d.leaseDuration / 3
	if interval <= 0 {
		interval = time.Minute
	}
	hctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-ticker.C:
				until := time.Now().UTC().Add(d.leaseDuration)
				rows, err := extend(hctx, until)
				if err != nil {
					d.log.Warn("group dispatch heartbeat failed", "type", label, "id", id, "error", err)
					continue
				}
				if rows == 0 {
					d.log.Debug("group dispatch heartbeat lost ownership", "type", label, "id", id)
					if onLost != nil {
						onLost()
					}
					return
				}
			}
		}
	}()
	return cancel
}

func (d *GroupDispatcher) Run(ctx context.Context) error {
	if d == nil || d.q == nil {
		return errors.New("group dispatcher not configured")
	}
	workers := max(d.workerCount, 1)
	for range workers {
		go d.runWorker(ctx)
	}
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	for {
		if err := d.poll(ctx); err != nil {
			d.log.Warn("group dispatch poll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.wakeC:
		case <-ticker.C:
		}
	}
}

func (d *GroupDispatcher) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case row := <-d.dispatchC:
			if err := d.ExecuteDispatch(ctx, row); err != nil {
				d.log.Warn("group dispatch worker failed", "dispatch_id", row.ID, "error", err)
			}
		}
	}
}

func (d *GroupDispatcher) ProcessOutbox(ctx context.Context, outbox sqlc.CtxGroupOutbox) error {
	return d.processOutbox(ctx, outbox)
}

// AbortGroupTurn stops the active turn for one group member. It is intentionally
// idempotent: a completed or unknown turn has nothing left to cancel.
func (d *GroupDispatcher) AbortGroupTurn(groupID, agentID string) bool {
	if d == nil || groupID == "" || agentID == "" {
		return false
	}
	return d.queue.Abort(agent.BuildGroupSessionKey(agentID, groupID))
}

func (d *GroupDispatcher) poll(ctx context.Context) error {
	if err := d.reapExpired(ctx); err != nil {
		return err
	}
	pending, err := d.q.ListPendingGroupOutbox(ctx, sqlc.ListPendingGroupOutboxParams{
		Now:        nullTime(time.Now().UTC()),
		LimitCount: 25,
	})
	if err != nil {
		return fmt.Errorf("list pending outbox: %w", err)
	}
	var errs []error
	for _, row := range pending {
		if err := d.ProcessOutbox(ctx, row); err != nil {
			errs = append(errs, err)
		}
	}
	dueDispatch, err := d.q.ListPendingGroupWakePairs(ctx, sqlc.ListPendingGroupWakePairsParams{
		Now:        nullTime(time.Now().UTC()),
		LimitCount: 25,
	})
	if err != nil {
		return fmt.Errorf("list pending wake pairs: %w", err)
	}
	for _, row := range dueDispatch {
		select {
		case d.dispatchC <- row:
		case <-ctx.Done():
			return errors.Join(append(errs, ctx.Err())...)
		}
	}
	return errors.Join(errs...)
}

func (d *GroupDispatcher) reapExpired(ctx context.Context) error {
	now := time.Now().UTC()
	outboxRows, err := d.q.ListExpiredRunningGroupOutbox(ctx, sqlc.ListExpiredRunningGroupOutboxParams{
		Now:        nullTime(now),
		LimitCount: 50,
	})
	if err != nil {
		return fmt.Errorf("list expired outbox: %w", err)
	}
	for _, row := range outboxRows {
		if row.AttemptCount >= d.maxAttempts {
			if _, err := d.q.MarkGroupOutboxFailed(ctx, sqlc.MarkGroupOutboxFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: "lease expired"}); err != nil {
				return fmt.Errorf("fail expired outbox: %w", err)
			}
			continue
		}
		if _, err := d.q.RequeueGroupOutbox(ctx, sqlc.RequeueGroupOutboxParams{ID: row.ID, AttemptCount: row.AttemptCount, NextAttemptAt: nullTime(now), LastError: "lease expired"}); err != nil {
			return fmt.Errorf("requeue expired outbox: %w", err)
		}
	}
	dispatchRows, err := d.q.ListExpiredRunningGroupDispatch(ctx, sqlc.ListExpiredRunningGroupDispatchParams{
		Now:        nullTime(now),
		LimitCount: 50,
	})
	if err != nil {
		return fmt.Errorf("list expired dispatch: %w", err)
	}
	for _, row := range dispatchRows {
		if row.ResultMessageID != "" {
			if _, err := d.q.MarkGroupDispatchCompleted(ctx, sqlc.MarkGroupDispatchCompletedParams{ID: row.ID, AttemptCount: row.AttemptCount}); err != nil {
				return fmt.Errorf("complete expired dispatch with result marker: %w", err)
			}
			continue
		}
		if row.AttemptCount >= d.maxAttempts {
			if _, err := d.q.MarkGroupDispatchFailed(ctx, sqlc.MarkGroupDispatchFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: "lease expired"}); err != nil {
				return fmt.Errorf("fail expired dispatch: %w", err)
			}
			continue
		}
		if _, err := d.q.RequeueGroupDispatch(ctx, sqlc.RequeueGroupDispatchParams{ID: row.ID, AttemptCount: row.AttemptCount, NextAttemptAt: nullTime(now), LastError: "lease expired"}); err != nil {
			return fmt.Errorf("requeue expired dispatch: %w", err)
		}
	}
	return nil
}

func (d *GroupDispatcher) processOutbox(ctx context.Context, outbox sqlc.CtxGroupOutbox) error {
	claimed, ok, err := d.claimOutbox(ctx, outbox)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ownedCtx, cancelOwned := context.WithCancel(ctx)
	defer cancelOwned()
	stopHeartbeat := d.startHeartbeat(ownedCtx, "outbox", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return d.q.ExtendRunningGroupOutboxLease(ctx, sqlc.ExtendRunningGroupOutboxLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, cancelOwned)
	defer stopHeartbeat()
	_, err = d.q.GetGroupMessage(ownedCtx, claimed.GroupMessageID)
	if err != nil {
		return d.failOutbox(ctx, claimed, fmt.Errorf("get outbox message: %w", err))
	}
	count, err := d.q.CountGroupDispatchByMessage(ownedCtx, claimed.GroupMessageID)
	if err != nil {
		return d.failOutbox(ctx, claimed, fmt.Errorf("count dispatch rows: %w", err))
	}
	if count == 0 {
		if err := d.materializeDispatchRowsTx(ownedCtx, claimed); err != nil {
			return d.failOutbox(ctx, claimed, err)
		}
	}
	stopHeartbeat()
	rows, err := d.q.MarkGroupOutboxCompleted(ctx, sqlc.MarkGroupOutboxCompletedParams{ID: claimed.ID, AttemptCount: claimed.AttemptCount})
	if err != nil {
		return fmt.Errorf("mark outbox completed: %w", err)
	}
	if rows == 0 {
		return nil
	}
	d.Wake()
	return nil
}

func (d *GroupDispatcher) claimOutbox(ctx context.Context, row sqlc.CtxGroupOutbox) (sqlc.CtxGroupOutbox, bool, error) {
	switch row.Status {
	case "running":
		return row, true, nil
	case "pending":
		claimed, err := d.q.ClaimPendingGroupOutbox(ctx, sqlc.ClaimPendingGroupOutboxParams{
			ID:         row.ID,
			Now:        nullTime(time.Now().UTC()),
			LeaseUntil: nullTime(time.Now().UTC().Add(d.leaseDuration)),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.CtxGroupOutbox{}, false, nil
		}
		if err != nil {
			return sqlc.CtxGroupOutbox{}, false, fmt.Errorf("claim outbox: %w", err)
		}
		return claimed, true, nil
	default:
		return sqlc.CtxGroupOutbox{}, false, nil
	}
}

func (d *GroupDispatcher) materializeDispatchRowsTx(ctx context.Context, outbox sqlc.CtxGroupOutbox) error {
	message, err := d.q.GetGroupMessage(ctx, outbox.GroupMessageID)
	if err != nil {
		return err
	}
	if d.db == nil {
		return d.materializeWakeRows(ctx, d.q, outbox, message)
	}
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin materialize dispatch rows: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := d.materializeWakeRows(ctx, d.q.WithTx(tx), outbox, message); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit materialize dispatch rows: %w", err)
	}
	committed = true
	return nil
}

// materializeWakeRows creates a durable per-member triage opportunity for
// every canonical message. The author has already acted, so never wake it.
func (d *GroupDispatcher) materializeWakeRows(ctx context.Context, q *sqlc.Queries, outbox sqlc.CtxGroupOutbox, message sqlc.CtxGroupMessage) error {
	members, err := q.ListGroupMembers(ctx, outbox.GroupID)
	if err != nil {
		return fmt.Errorf("list group members: %w", err)
	}
	for _, member := range members {
		if message.ActorType == string(eventlog.ActorAgent) && member.AgentID == message.ActorID {
			continue
		}
		if err := q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{
			ID: uuid.Must(uuid.NewV7()).String(), GroupMessageID: outbox.GroupMessageID,
			GroupID: outbox.GroupID, AgentID: member.AgentID, ReplyChannelID: member.ReplyChannelID,
		}); err != nil {
			return fmt.Errorf("create wake row: %w", err)
		}
	}
	return nil
}

func (d *GroupDispatcher) ExecuteDispatch(ctx context.Context, row sqlc.CtxGroupDispatch) error {
	claimed, ok, err := d.claimDispatch(ctx, row)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ownedCtx, cancelOwned := context.WithCancel(ctx)
	defer cancelOwned()
	stopHeartbeat := d.startHeartbeat(ownedCtx, "dispatch", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return d.q.ExtendRunningGroupDispatchLease(ctx, sqlc.ExtendRunningGroupDispatchLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, cancelOwned)
	defer stopHeartbeat()
	message, state, err := d.messageAndState(ownedCtx, claimed.GroupMessageID)
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	outbox, err := d.q.GetGroupOutboxByMessage(ownedCtx, claimed.GroupMessageID)
	if err != nil {
		return d.failDispatch(ctx, claimed, fmt.Errorf("get group outbox metadata: %w", err))
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		// Dispatch rows were already materialized from this envelope. Optional
		// publish metadata must not make an otherwise executable legacy row fail.
		d.log.Debug("ignoring invalid group outbox publish metadata", "dispatch_id", claimed.ID, "error", err)
		envelope = GroupOutboxEnvelope{}
	}
	if claimed.Kind == "wake" {
		act, reason, degraded := d.triageWake(ownedCtx, claimed, message, state, envelope)
		if !act {
			if d.events != nil {
				d.events.AnnounceTurn(claimed.GroupID, claimed.AgentID, "silent", reason)
			}
			if degraded && claimed.AttemptCount < d.maxAttempts {
				return d.failDispatch(ctx, claimed, fmt.Errorf("degraded_triage: %s", reason))
			}
			_, err := d.q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: claimed.ID, AttemptCount: claimed.AttemptCount, Reason: reason})
			return err
		}
	}
	publisher, err := d.publisherFor(state, claimed)
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	if claimed.ResultMessageID != "" {
		if claimed.PublishedAt.Valid {
			return d.completeDispatch(ctx, claimed)
		}
		accepted, err := d.q.GetGroupMessage(ownedCtx, claimed.ResultMessageID)
		if err != nil {
			return d.failDispatch(ctx, claimed, fmt.Errorf("get accepted group result: %w", err))
		}
		d.log.Warn("replaying accepted group reply from canonical text after buffer loss", "dispatch_id", claimed.ID, "result_message_id", accepted.ID, "upgrade_trigger", "cross-process rich replay requires BlobStore event spooling")
		return d.publishAccepted(ownedCtx, claimed, message, state, publisher, groupResponseFromMessage(accepted))
	}
	agentName := claimed.AgentID
	if a, err := d.q.GetAgent(ownedCtx, claimed.AgentID); err == nil && a.Name != "" {
		agentName = a.Name
	}
	// The sink is deliberately installed before Chat. The runtime finalizes it
	// before closing its output, so draining the stream is the handoff barrier.
	sink := memory.NewGroupTurnSink()
	chatCtx := memory.WithGroupTurnSink(ownedCtx, sink)
	stream, err := d.chat(chatCtx, claimed, message, state)
	if errors.Is(err, errGroupTurnSuperseded) {
		// The trigger was consumed by a session boundary while this row waited
		// (restart after `/new`, or a redundant re-execution). Running it now
		// would leak a pre-reset message into the successor session; there is
		// nothing left to do but retire the row.
		return d.completeDispatch(ctx, claimed)
	}
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	response := d.bufferGroupResponse(ownedCtx, stream)
	turn, delivered := sink.Result()
	if response.err != nil || !response.complete || !delivered || !turn.Complete {
		cause := response.err
		if cause == nil {
			cause = errors.New("group turn ended without a complete deferred result")
		}
		if d.events != nil {
			d.events.AnnounceTurn(claimed.GroupID, claimed.AgentID, "failed", cause.Error())
		}
		return d.failDispatch(ctx, claimed, cause)
	}
	accepted, err := d.acceptGroupResponse(ownedCtx, claimed, state, response, turn)
	if errors.Is(err, errGroupTurnHeld) {
		if d.events != nil {
			d.events.AnnounceTurn(claimed.GroupID, claimed.AgentID, "held", "freshness")
		}
		return nil
	}
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	// Keep the display name on the replayed event stream's request. The accepted
	// row is authoritative for retry; the first delivery preserves all events.
	response.agentName = agentName
	return d.publishAcceptedWithEnvelope(ownedCtx, claimed, message, state, publisher, response, envelope, accepted)
}

func (d *GroupDispatcher) claimDispatch(ctx context.Context, row sqlc.CtxGroupDispatch) (sqlc.CtxGroupDispatch, bool, error) {
	switch row.Status {
	case "running":
		return row, true, nil
	case "pending":
		if row.Kind == "wake" {
			claimed, err := d.q.ClaimNewestGroupWake(ctx, sqlc.ClaimNewestGroupWakeParams{
				GroupID: row.GroupID, AgentID: row.AgentID, Now: nullTime(time.Now().UTC()),
				LeaseUntil: nullTime(time.Now().UTC().Add(d.leaseDuration)),
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return sqlc.CtxGroupDispatch{}, false, nil
			}
			if err != nil {
				return sqlc.CtxGroupDispatch{}, false, fmt.Errorf("claim newest wake: %w", err)
			}
			return claimed, true, nil
		}
		claimed, err := d.q.ClaimPendingGroupDispatch(ctx, sqlc.ClaimPendingGroupDispatchParams{
			ID:         row.ID,
			Now:        nullTime(time.Now().UTC()),
			LeaseUntil: nullTime(time.Now().UTC().Add(d.leaseDuration)),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.CtxGroupDispatch{}, false, nil
		}
		if err != nil {
			return sqlc.CtxGroupDispatch{}, false, fmt.Errorf("claim dispatch: %w", err)
		}
		return claimed, true, nil
	default:
		return sqlc.CtxGroupDispatch{}, false, nil
	}
}

func (d *GroupDispatcher) completeDispatch(ctx context.Context, row sqlc.CtxGroupDispatch) error {
	rows, err := d.q.MarkGroupDispatchCompleted(ctx, sqlc.MarkGroupDispatchCompletedParams{ID: row.ID, AttemptCount: row.AttemptCount})
	if err != nil {
		return fmt.Errorf("mark dispatch completed: %w", err)
	}
	if rows == 0 {
		return nil
	}
	return nil
}

func (d *GroupDispatcher) acceptGroupResponse(ctx context.Context, row sqlc.CtxGroupDispatch, state sqlc.CtxGroupState, response groupResponse, turn memory.DeferredGroupTurn) (eventlog.AppendResult, error) {
	if d.db == nil {
		return eventlog.AppendResult{}, errors.New("dispatcher db not configured")
	}
	if d.committer == nil {
		return eventlog.AppendResult{}, errors.New("group dispatcher requires memory.TxGroupCommitter")
	}
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("accept group response: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)
	locked, err := q.GetGroupStateByIDForUpdate(ctx, row.GroupID)
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("lock group state for accept: %w", err)
	}
	lastHuman, err := q.LastHumanSeqAtOrBefore(ctx, sqlc.LastHumanSeqAtOrBeforeParams{GroupID: row.GroupID, TriggerSeq: row.TriggerSeq})
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("last human seq: %w", err)
	}
	held, err := q.CountHeldGroupDispatchesInChain(ctx, sqlc.CountHeldGroupDispatchesInChainParams{GroupID: row.GroupID, AgentID: row.AgentID, AfterOwnPostSeq: 0, AfterHumanSeq: lastHuman})
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("count held dispatches: %w", err)
	}
	if held < int64(locked.HoldLimit) {
		peers, err := q.CountPeerMessagesAfterSeq(ctx, sqlc.CountPeerMessagesAfterSeqParams{GroupID: row.GroupID, AfterSeq: row.TriggerSeq, AgentID: row.AgentID})
		if err != nil {
			return eventlog.AppendResult{}, fmt.Errorf("count peer messages after snapshot: %w", err)
		}
		if peers > 0 {
			upTo, err := q.MaxPeerMessageSeqAfterSeq(ctx, sqlc.MaxPeerMessageSeqAfterSeqParams{GroupID: row.GroupID, AfterSeq: row.TriggerSeq, AgentID: row.AgentID})
			if err != nil {
				return eventlog.AppendResult{}, fmt.Errorf("max peer message seq: %w", err)
			}
			if _, err := q.MarkGroupDispatchHeld(ctx, sqlc.MarkGroupDispatchHeldParams{ID: row.ID, AttemptCount: row.AttemptCount, HeldUpToSeq: pgtype.Int8{Int64: upTo, Valid: true}}); err != nil {
				return eventlog.AppendResult{}, fmt.Errorf("mark held: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return eventlog.AppendResult{}, fmt.Errorf("commit held dispatch: %w", err)
			}
			return eventlog.AppendResult{}, errGroupTurnHeld
		}
	}
	if _, err := q.GetLatestPeerGroupMessageWithContent(ctx, sqlc.GetLatestPeerGroupMessageWithContentParams{GroupID: row.GroupID, AgentID: row.AgentID, Content: response.text}); err == nil {
		if _, err := q.MarkGroupDispatchHeld(ctx, sqlc.MarkGroupDispatchHeldParams{ID: row.ID, AttemptCount: row.AttemptCount}); err != nil {
			return eventlog.AppendResult{}, fmt.Errorf("mark duplicate held: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return eventlog.AppendResult{}, fmt.Errorf("commit duplicate held: %w", err)
		}
		return eventlog.AppendResult{}, errGroupTurnHeld
	}
	posts, err := q.CountAgentPostsSinceSeq(ctx, sqlc.CountAgentPostsSinceSeqParams{GroupID: row.GroupID, AfterSeq: lastHuman})
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("count agent posts: %w", err)
	}
	if posts >= int64(locked.MaxRepliesPerHumanTrigger) {
		if _, err := q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: row.ID, AttemptCount: row.AttemptCount, Reason: "hard_cap"}); err != nil {
			return eventlog.AppendResult{}, fmt.Errorf("mark cap silent: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return eventlog.AppendResult{}, fmt.Errorf("commit cap silent: %w", err)
		}
		return eventlog.AppendResult{}, errGroupTurnHeld
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
		return eventlog.AppendResult{}, err
	}
	updated, err := q.SetGroupDispatchResultMessage(ctx, sqlc.SetGroupDispatchResultMessageParams{
		ID:              row.ID,
		AttemptCount:    row.AttemptCount,
		ResultMessageID: result.Message.ID,
	})
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("set dispatch result message: %w", err)
	}
	if updated == 0 {
		return eventlog.AppendResult{}, errors.New("set dispatch result message: lost dispatch ownership")
	}
	if err := d.committer.CommitGroupTurn(ctx, q, turn); err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("commit deferred group turn: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("accept group response: commit: %w", err)
	}
	if d.events != nil {
		d.events.Announce(result)
	}
	return result, nil
}

func (d *GroupDispatcher) publisherFor(state sqlc.CtxGroupState, row sqlc.CtxGroupDispatch) (GroupPublisher, error) {
	if publisher, ok := d.publishers.Get(row.ReplyChannelID); ok {
		return publisher, nil
	}
	if state.Platform == "web" {
		return NoopGroupPublisher(), nil
	}
	return nil, fmt.Errorf("publisher %q not registered", row.ReplyChannelID)
}

func (d *GroupDispatcher) failOutbox(ctx context.Context, row sqlc.CtxGroupOutbox, cause error) error {
	if row.AttemptCount >= d.maxAttempts {
		if _, err := d.q.MarkGroupOutboxFailed(ctx, sqlc.MarkGroupOutboxFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: cause.Error()}); err != nil {
			return fmt.Errorf("mark outbox failed: %w", err)
		}
		return cause
	}
	if _, err := d.q.RequeueGroupOutbox(ctx, sqlc.RequeueGroupOutboxParams{
		ID:            row.ID,
		AttemptCount:  row.AttemptCount,
		NextAttemptAt: nullTime(time.Now().UTC().Add(backoff(row.AttemptCount))),
		LastError:     cause.Error(),
	}); err != nil {
		return fmt.Errorf("requeue outbox: %w", err)
	}
	return cause
}

func (d *GroupDispatcher) failDispatch(ctx context.Context, row sqlc.CtxGroupDispatch, cause error) error {
	if row.AttemptCount >= d.maxAttempts {
		if _, err := d.q.MarkGroupDispatchFailed(ctx, sqlc.MarkGroupDispatchFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: cause.Error()}); err != nil {
			return fmt.Errorf("mark dispatch failed: %w", err)
		}
		return cause
	}
	if _, err := d.q.RequeueGroupDispatch(ctx, sqlc.RequeueGroupDispatchParams{
		ID:            row.ID,
		AttemptCount:  row.AttemptCount,
		NextAttemptAt: nullTime(time.Now().UTC().Add(backoff(row.AttemptCount))),
		LastError:     cause.Error(),
	}); err != nil {
		return fmt.Errorf("requeue dispatch: %w", err)
	}
	return cause
}

func (d *GroupDispatcher) messageAndState(ctx context.Context, messageID string) (sqlc.CtxGroupMessage, sqlc.CtxGroupState, error) {
	return d.messageAndStateWithQueries(ctx, d.q, messageID)
}

func (d *GroupDispatcher) messageAndStateWithQueries(ctx context.Context, q *sqlc.Queries, messageID string) (sqlc.CtxGroupMessage, sqlc.CtxGroupState, error) {
	message, err := q.GetGroupMessage(ctx, messageID)
	if err != nil {
		return sqlc.CtxGroupMessage{}, sqlc.CtxGroupState{}, fmt.Errorf("get group message: %w", err)
	}
	state, err := q.GetGroupStateByID(ctx, message.GroupID)
	if err != nil {
		return sqlc.CtxGroupMessage{}, sqlc.CtxGroupState{}, fmt.Errorf("get group state: %w", err)
	}
	return message, state, nil
}

func (d *GroupDispatcher) chatDispatch(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
	sessionKey := agent.BuildGroupSessionKey(row.AgentID, row.GroupID)
	stream, doneC, err := d.queue.Enqueue(ctx, sessionKey, func(qctx context.Context) (*pkgchannel.ChatStream, error) {
		return d.chatDispatchUnqueued(qctx, row, message, state)
	})
	if err != nil {
		return nil, err
	}
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(doneC)
		defer close(out)
		for evt := range stream.Events {
			select {
			case out <- evt:
			case <-ctx.Done():
				out <- pkgchannel.Event{Err: ctx.Err()}
				for range stream.Events {
				}
				return
			}
		}
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: stream.SessionID}, nil
}

// errGroupTurnSuperseded reports that a dispatch row's trigger message sits at
// or below the agent's ingest cursor: a session rotation (or a completed later
// turn) already consumed it. The row is finished work, not a failure.
var (
	errGroupTurnSuperseded = errors.New("group turn superseded by the agent's ingest cursor")
	errGroupTurnHeld       = errors.New("group turn held for a newer peer message")
)

func (d *GroupDispatcher) chatDispatchUnqueued(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
	if d.coord == nil {
		return nil, errors.New("coordinator not configured")
	}
	// The dispatch row is routing state, not authority. Re-check the originating
	// persisted channel after this turn reaches the head of its execution queue.
	// resolveGroupChat separately re-checks that the agent itself is enabled.
	if state.Platform != "web" {
		if err := ValidateGroupMembership(ctx, d.coord.store, state.Platform, row.AgentID, row.ReplyChannelID); err != nil {
			return nil, fmt.Errorf("validate queued group channel: %w", err)
		}
	}
	// This runs inside the per-(agent,group) queue — the same queue that
	// serializes `/new` — so the cursor read cannot interleave with a rotation:
	// either the rotation committed first and its boundary is visible here, or
	// this turn runs first and the rotation waits. Checking outside the queue
	// would reopen exactly the race this closes: a dispatch row restarted after
	// a rotation would run a pre-reset trigger against the successor session.
	cursor, err := d.q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  row.GroupID,
		Pipeline: memory.GroupIngestPipeline(row.AgentID),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No cursor yet: nothing has been consumed, the turn runs.
	case err != nil:
		return nil, fmt.Errorf("read group ingest cursor: %w", err)
	case message.Seq <= cursor.LastSeq:
		return nil, errGroupTurnSuperseded
	}
	ctx = memory.WithGroupSeq(ctx, message.Seq)
	content := groupMessageContentBlocks(message)
	var stream *pkgchannel.ChatStream
	if state.Platform == "web" {
		stream, err = d.chatWeb(ctx, row, message)
	} else {
		var rc *ResolvedChat
		rc, err = d.coord.resolveGroupChat(ctx, pkgchannel.IncomingMessage{
			Platform:  state.Platform,
			ChannelID: row.ReplyChannelID,
			SenderID:  message.ActorID,
			ChatID:    state.PlatformGroupID,
			IsGroup:   true,
			ThreadID:  state.PlatformThreadID,
			Content:   content,
			MessageID: nullStringValue(message.PlatformMessageID),
			ReplyTo:   nullStringValue(message.ReplyTo),
		}, row.GroupID, row.AgentID, row.ReplyChannelID)
		if err == nil {
			stream, err = d.coord.chatWithRC(ctx, rc, content)
		}
	}
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// groupMessageContentBlocks rebuilds the structured blocks persisted for a
// group message (images survive the event log via content_blocks), falling
// back to the plain-text projection for text-only or legacy rows.
func groupMessageContentBlocks(message sqlc.CtxGroupMessage) []ai.ContentBlock {
	if blocks, err := ai.UnmarshalContentBlocks(message.ContentBlocks); err == nil && blocks != nil {
		return blocks
	}
	return []ai.ContentBlock{ai.TextContent{Text: message.Content}}
}

// groupMessageChatContent is groupMessageContentBlocks for agent.ChatRequest,
// which keeps plain strings for text-only messages.
func groupMessageChatContent(message sqlc.CtxGroupMessage) agent.MessageContent {
	if blocks, err := ai.UnmarshalContentBlocks(message.ContentBlocks); err == nil && blocks != nil {
		return blocks
	}
	return message.Content
}

// resolveWebGroupChat builds the group chat binding for an agent in a Web group.
// A persisted membership is not a standing execute grant: the authority is
// minted fresh for this exact group/member and re-authorized here, so service
// selection and the group authority live in exactly one place.
func (d *GroupDispatcher) resolveWebGroupChat(ctx context.Context, groupID, agentID string) (*ResolvedChat, error) {
	if d == nil || d.coord == nil || d.coord.agentAccess == nil {
		return nil, ErrAgentAccessDenied
	}
	authority, err := agentaccess.GroupAgentAuthority(groupID, agentID)
	if err != nil {
		return nil, ErrAgentAccessDenied
	}
	if _, err := d.coord.agentAccess.Use(ctx, authority, agentID); err != nil {
		return nil, ErrAgentAccessDenied
	}
	svc := d.coord.serviceManager.GetService(agentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found", agentID)
	}
	return &ResolvedChat{
		Service:    svc,
		AgentID:    agentID,
		SessionKey: agent.BuildGroupSessionKey(agentID, groupID),
		Channel:    session.Channel("group:" + groupID),
		GroupID:    groupID,
		Authority:  authority,
	}, nil
}

func (d *GroupDispatcher) chatWeb(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage) (*pkgchannel.ChatStream, error) {
	speaker := webGroupSpeaker(message)
	// A persisted group membership is not an execute grant forever. The human
	// speaker is audit/personalization only; never borrow their private user
	// authority to execute a group turn. resolveWebGroupChat mints and re-checks
	// the group authority.
	rc, err := d.resolveWebGroupChat(ctx, row.GroupID, row.AgentID)
	if err != nil {
		return nil, err
	}
	info, err := rc.Service.ResolveChatChannelSession(ctx, rc.chatChannelRequest())
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}
	// CtxGroupMessage carries no display name; fill it best-effort from the auth
	// user so the prompt shows a real name instead of "Unknown". Fail-soft.
	if speaker.UserID != "" && speaker.DisplayName == "" && d.coord.auth != nil {
		if u, err := d.coord.auth.GetUser(ctx, speaker.UserID); err == nil && u.Name != "" {
			speaker.DisplayName = u.Name
		}
	}
	// The Web group turn does not go through ResolvedChat.Chat, so it attaches
	// the same durable chat-binding marker here; without it the group turn would
	// look like a Web send to tools that require a channel-backed chat.
	events := rc.Service.Chat(rc.withChatBinding(ctx), agent.ChatRequest{
		SessionID:      info.ID,
		UserID:         row.GroupID,
		AgentID:        row.AgentID,
		Kind:           session.KindChat,
		GroupID:        row.GroupID,
		Channel:        rc.Channel,
		Message:        groupMessageChatContent(message),
		CurrentSpeaker: speaker,
		Authority:      rc.Authority,
	})
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		for evt := range events {
			select {
			case out <- convertEvent(evt):
			case <-ctx.Done():
				out <- pkgchannel.Event{Err: ctx.Err()}
				for range events {
				}
				return
			}
		}
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: info.ID}, nil
}

// webGroupSpeaker derives the per-turn speaker for a Web group dispatch. Web
// senders authenticate and SendGroupMessage persists the auth user id as
// actor_id, so it is a safe profile target — but only for a genuine human actor.
// Any other actor type or an empty id fails closed (zero speaker) so a malformed
// row never injects an arbitrary user's private profile.
func webGroupSpeaker(message sqlc.CtxGroupMessage) memory.CurrentSpeaker {
	if message.ActorType != string(eventlog.ActorHuman) || message.ActorID == "" {
		return memory.CurrentSpeaker{}
	}
	return memory.CurrentSpeaker{
		Platform:       "web",
		PlatformUserID: message.ActorID,
		UserID:         message.ActorID,
	}
}

type groupResponse struct {
	text      string
	reasoning string
	sessionID string
	agentName string
	events    []pkgchannel.Event
	complete  bool
	err       error
}

// bufferGroupResponse drains the runtime completely before any platform side
// effect. The ceiling is an intentional in-memory limit: use BlobStore spooling
// when a deployment needs responses larger than this.
func (d *GroupDispatcher) bufferGroupResponse(ctx context.Context, stream *pkgchannel.ChatStream) groupResponse {
	response := groupResponse{sessionID: stream.SessionID, complete: true}
	limit := d.replyBufferBytes
	if limit <= 0 {
		limit = defaultGroupReplyBufferBytes
	}
	var used int
	for evt := range stream.Events {
		if evt.Err != nil {
			response.complete = false
			if response.err == nil {
				response.err = evt.Err
			}
			continue
		}
		encoded, err := json.Marshal(evt)
		if err != nil {
			response.complete = false
			if response.err == nil {
				response.err = fmt.Errorf("encode group reply event: %w", err)
			}
			continue
		}
		used += len(encoded)
		if used > limit {
			response.complete = false
			if response.err == nil {
				response.err = fmt.Errorf("group reply exceeded %d-byte buffer", limit)
			}
			continue
		}
		response.events = append(response.events, evt)
		response.text += evt.Text
		response.reasoning += evt.Reasoning
	}
	if ctx.Err() != nil {
		response.complete = false
		if response.err == nil {
			response.err = ctx.Err()
		}
	}
	return response
}

func groupResponseFromMessage(message sqlc.CtxGroupMessage) groupResponse {
	// Ceiling: event envelopes live only in this process. After a restart, an
	// accepted unpublished reply replays canonical text/reasoning only. Spool the
	// buffered event sequence to BlobStore when cross-process rich replay matters.
	events := make([]pkgchannel.Event, 0, 2)
	if message.Reasoning != "" {
		events = append(events, pkgchannel.Event{Reasoning: message.Reasoning})
	}
	if message.Content != "" {
		events = append(events, pkgchannel.Event{Text: message.Content})
	}
	return groupResponse{
		text: message.Content, reasoning: message.Reasoning, sessionID: message.AgentSessionID,
		events: events, complete: true,
	}
}

func replayGroupResponse(response groupResponse) *pkgchannel.ChatStream {
	events := make(chan pkgchannel.Event, len(response.events))
	for _, evt := range response.events {
		events <- evt
	}
	close(events)
	return &pkgchannel.ChatStream{Events: events, SessionID: response.sessionID}
}

func (d *GroupDispatcher) publishAccepted(ctx context.Context, row sqlc.CtxGroupDispatch, trigger sqlc.CtxGroupMessage, state sqlc.CtxGroupState, publisher GroupPublisher, response groupResponse) error {
	return d.publishAcceptedWithEnvelope(ctx, row, trigger, state, publisher, response, GroupOutboxEnvelope{}, eventlog.AppendResult{})
}

func (d *GroupDispatcher) publishAcceptedWithEnvelope(ctx context.Context, row sqlc.CtxGroupDispatch, trigger sqlc.CtxGroupMessage, state sqlc.CtxGroupState, publisher GroupPublisher, response groupResponse, envelope GroupOutboxEnvelope, accepted eventlog.AppendResult) error {
	if row.ResultMessageID == "" && accepted.Message.ID != "" {
		row.ResultMessageID = accepted.Message.ID
	}
	if accepted.Message.ID == "" && row.ResultMessageID != "" {
		canonical, err := d.q.GetGroupMessage(ctx, row.ResultMessageID)
		if err != nil {
			return d.failDispatch(ctx, row, fmt.Errorf("get canonical group result: %w", err))
		}
		accepted = eventlog.AppendResult{GroupID: row.GroupID, Seq: canonical.Seq, Message: canonical}
	}
	agentName := response.agentName
	if agentName == "" {
		agentName = row.AgentID
		if a, err := d.q.GetAgent(ctx, row.AgentID); err == nil && a.Name != "" {
			agentName = a.Name
		}
	}
	sessionKey := agent.BuildGroupSessionKey(row.AgentID, row.GroupID)
	err := publisher.Publish(ctx, GroupPublishRequest{
		GroupID: row.GroupID, AgentID: row.AgentID, AgentName: agentName,
		ReplyChannelID: row.ReplyChannelID, Platform: state.Platform,
		PlatformGroupID: state.PlatformGroupID, PlatformThreadID: state.PlatformThreadID,
		ReplyTo: nullStringValue(trigger.PlatformMessageID), Stream: replayGroupResponse(response),
		RequesterID: trigger.ActorID, LifecycleFeedback: envelope.LifecycleFeedback,
		Abort:              func() bool { return d.queue.Abort(sessionKey) },
		FinalAttempt:       row.AttemptCount >= d.maxAttempts,
		AcceptedMessageID:  accepted.Message.ID,
		AcceptedMessageSeq: accepted.Seq,
	})
	if err != nil {
		if row.AttemptCount >= d.maxAttempts && row.ResultMessageID != "" {
			return d.failAcceptedPublish(ctx, row, fmt.Errorf("publish: %w", err))
		}
		return d.failDispatch(ctx, row, fmt.Errorf("publish: %w", err))
	}
	if err := d.markAcceptedPublished(ctx, row, state.Platform); err != nil {
		return d.failDispatch(ctx, row, err)
	}
	return d.completeDispatch(ctx, row)
}

func (d *GroupDispatcher) markAcceptedPublished(ctx context.Context, row sqlc.CtxGroupDispatch, platform string) error {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark accepted publish: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)
	updated, err := q.MarkGroupDispatchPublished(ctx, sqlc.MarkGroupDispatchPublishedParams{ID: row.ID, AttemptCount: row.AttemptCount, ResultMessageID: row.ResultMessageID})
	if err != nil {
		return fmt.Errorf("mark dispatch published: %w", err)
	}
	if updated == 0 {
		return errors.New("mark dispatch published: lost dispatch ownership")
	}
	if platform == "web" {
		if err := d.createAgentReplyOutbox(ctx, q, row); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("mark accepted publish: commit: %w", err)
		}
		d.Wake()
		return nil
	}
	message, err := q.SetGroupMessageDeliveryState(ctx, sqlc.SetGroupMessageDeliveryStateParams{ID: row.ResultMessageID, DeliveryState: "delivered"})
	if err != nil {
		return fmt.Errorf("mark group message delivered: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mark accepted publish: commit: %w", err)
	}
	d.Wake()
	if d.events != nil {
		d.events.Announce(eventlog.AppendResult{GroupID: row.GroupID, Seq: message.Seq, Message: message})
	}
	return nil
}

func (d *GroupDispatcher) createAgentReplyOutbox(ctx context.Context, q *sqlc.Queries, row sqlc.CtxGroupDispatch) error {
	message, err := q.GetGroupMessage(ctx, row.ResultMessageID)
	if err != nil {
		return fmt.Errorf("get accepted agent reply: %w", err)
	}
	members, err := q.ListGroupMembers(ctx, row.GroupID)
	if err != nil {
		return fmt.Errorf("list group members: %w", err)
	}
	mentions := parseGroupMentions(ctx, q, message.Content, members)
	envelope, err := encodeGroupOutboxEnvelope(GroupOutboxEnvelope{ActorType: string(eventlog.ActorAgent), Mentions: mentions})
	if err != nil {
		return fmt.Errorf("encode agent reply outbox: %w", err)
	}
	_, err = q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
		ID: uuid.Must(uuid.NewV7()).String(), GroupMessageID: row.ResultMessageID, GroupID: row.GroupID,
		Envelope: envelope, Status: "pending", LastError: "",
	})
	if err != nil {
		return fmt.Errorf("create agent reply outbox: %w", err)
	}
	return nil
}

// parseGroupMentions keeps agent-authored outboxes self-contained. Web ingress
// already has member IDs; agent text may use either an ID or a display name.
func parseGroupMentions(ctx context.Context, q *sqlc.Queries, content string, members []sqlc.ChannelGroupMember) []pkgchannel.Mention {
	byToken := make(map[string]string, len(members)*2)
	for _, member := range members {
		byToken[member.AgentID] = member.AgentID
		if a, err := q.GetAgent(ctx, member.AgentID); err == nil && a.Name != "" {
			byToken[a.Name] = member.AgentID
		}
	}
	seen := make(map[string]struct{}, len(members))
	mentions := make([]pkgchannel.Mention, 0)
	for word := range strings.FieldsSeq(content) {
		token, ok := strings.CutPrefix(strings.Trim(word, "()[]{}:;,.!?"), "@")
		if !ok {
			continue
		}
		agentID, ok := byToken[token]
		if !ok {
			continue
		}
		if _, duplicate := seen[agentID]; duplicate {
			continue
		}
		seen[agentID] = struct{}{}
		mentions = append(mentions, pkgchannel.Mention{Raw: "@" + token, AgentID: agentID})
	}
	return mentions
}

func (d *GroupDispatcher) failAcceptedPublish(ctx context.Context, row sqlc.CtxGroupDispatch, cause error) error {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fail accepted publish: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)
	message, err := q.SetGroupMessageDeliveryState(ctx, sqlc.SetGroupMessageDeliveryStateParams{ID: row.ResultMessageID, DeliveryState: "failed"})
	if err != nil {
		return fmt.Errorf("mark group message failed: %w", err)
	}
	updated, err := q.MarkGroupDispatchFailed(ctx, sqlc.MarkGroupDispatchFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: cause.Error()})
	if err != nil {
		return fmt.Errorf("mark dispatch failed: %w", err)
	}
	if updated == 0 {
		return errors.New("mark dispatch failed: lost dispatch ownership")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("fail accepted publish: commit: %w", err)
	}
	d.log.Warn("group reply delivery failed permanently", "dispatch_id", row.ID, "result_message_id", row.ResultMessageID, "error", cause)
	if d.events != nil {
		d.events.Announce(eventlog.AppendResult{GroupID: row.GroupID, Seq: message.Seq, Message: message})
		d.events.AnnounceTurn(row.GroupID, row.AgentID, "failed", cause.Error())
	}
	return cause
}

func backoff(attempts int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(attempts) * time.Second
	if d > time.Minute {
		return time.Minute
	}
	return d
}

func nullTime(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func nullStringValue(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}
