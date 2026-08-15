package channel

import (
	"context"
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
	appdb "github.com/CherryHQ/stella/internal/db"
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

	leaseDuration time.Duration
	pollInterval  time.Duration
	maxAttempts   int64
	wakeC         chan struct{}
	queue         *sessionQueue
	chat          dispatchChatFunc
}

// Coordination bundles the coordinator and its durable group dispatcher. The
// channel domain builds them together and closes the coordinator<->dispatcher
// cycle, so the composition root does not assemble the cycle by hand.
type Coordination struct {
	// Coordinator is the channel MessageHandler for all channels.
	Coordinator *Coordinator
	// GroupDispatcher is the durable group-dispatch runner. The HTTP layer needs
	// only this narrow port (Run + DispatchSync), not the whole coordinator.
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
		db:            db,
		q:             sqlc.New(db),
		coord:         coord,
		publishers:    publishers,
		log:           slog.With("component", "group_dispatcher"),
		leaseDuration: defaultGroupDispatchLease,
		pollInterval:  defaultGroupDispatchPoll,
		maxAttempts:   defaultGroupDispatchMaxAttempts,
		wakeC:         make(chan struct{}, 1),
		queue:         newSessionQueue(),
	}
	d.chat = d.chatDispatch
	return d
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

func (d *GroupDispatcher) ProcessOutbox(ctx context.Context, outbox sqlc.CtxGroupOutbox) error {
	return d.processOutbox(ctx, outbox, nil)
}

func (d *GroupDispatcher) DispatchSync(ctx context.Context, outbox sqlc.CtxGroupOutbox, publisherOverride GroupPublisher) error {
	return d.processOutbox(ctx, outbox, publisherOverride)
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
	dueDispatch, err := d.q.ListPendingGroupDispatch(ctx, sqlc.ListPendingGroupDispatchParams{
		Now:        nullTime(time.Now().UTC()),
		LimitCount: 25,
	})
	if err != nil {
		return fmt.Errorf("list pending dispatch: %w", err)
	}
	for _, row := range dueDispatch {
		if err := d.ExecuteDispatch(ctx, row, nil); err != nil {
			errs = append(errs, err)
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
		// A response that was already generated and persisted retires here even
		// when its delivery never finished. Requeueing it for re-delivery would
		// resend the whole response on every platform that does not confirm
		// chunks, turning a heartbeat hiccup into duplicate group messages;
		// resuming after a crash is a separate, opt-in step.
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

func (d *GroupDispatcher) processOutbox(ctx context.Context, outbox sqlc.CtxGroupOutbox, publisherOverride GroupPublisher) error {
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
	return d.executeDispatchesByMessage(ctx, claimed.GroupMessageID, publisherOverride)
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
	responding, err := d.prepareDispatchResponders(ctx, d.q, outbox)
	if err != nil {
		return err
	}
	if d.db == nil {
		return d.createDispatchRows(ctx, d.q, outbox, responding)
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
	if err := d.createDispatchRows(ctx, d.q.WithTx(tx), outbox, responding); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit materialize dispatch rows: %w", err)
	}
	committed = true
	return nil
}

func (d *GroupDispatcher) prepareDispatchResponders(ctx context.Context, q *sqlc.Queries, outbox sqlc.CtxGroupOutbox) ([]string, error) {
	message, state, err := d.messageAndStateWithQueries(ctx, q, outbox.GroupMessageID)
	if err != nil {
		return nil, err
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		return nil, fmt.Errorf("decode outbox envelope: %w", err)
	}
	members, err := q.ListGroupMembers(ctx, outbox.GroupID)
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	groupMembers := make([]GroupMember, len(members))
	for i, m := range members {
		groupMembers[i] = GroupMember{AgentID: m.AgentID, ReplyChannelID: m.ReplyChannelID}
	}
	if d.coord != nil {
		d.coord.resolveMentionAgentsWithMembers(ctx, outbox.GroupID, state.Platform, envelope.Mentions, groupMembers)
	}
	return d.decideResponders(ctx, q, outbox, message, state, envelope, groupMembers), nil
}

func (d *GroupDispatcher) createDispatchRows(ctx context.Context, q *sqlc.Queries, outbox sqlc.CtxGroupOutbox, responding []string) error {
	members, err := q.ListGroupMembers(ctx, outbox.GroupID)
	if err != nil {
		return fmt.Errorf("list group members: %w", err)
	}
	groupMembers := make([]GroupMember, len(members))
	for i, m := range members {
		groupMembers[i] = GroupMember{AgentID: m.AgentID, ReplyChannelID: m.ReplyChannelID}
	}
	for _, agentID := range responding {
		replyChannelID := findMemberReplyChannel(groupMembers, agentID)
		if replyChannelID == "" {
			d.log.Warn("selected group agent has no reply channel", "group_id", outbox.GroupID, "agent_id", agentID)
			continue
		}
		if err := q.CreateGroupDispatch(ctx, sqlc.CreateGroupDispatchParams{
			ID:             uuid.Must(uuid.NewV7()).String(),
			GroupMessageID: outbox.GroupMessageID,
			GroupID:        outbox.GroupID,
			AgentID:        agentID,
			ReplyChannelID: replyChannelID,
			Status:         "pending",
			AttemptCount:   0,
			LeaseUntil:     pgtype.Timestamptz{},
			NextAttemptAt:  pgtype.Timestamptz{},
			LastError:      "",
		}); err != nil {
			return fmt.Errorf("create dispatch row: %w", err)
		}
	}
	return nil
}

// decideResponders selects which agents respond. Explicit mentions that resolve
// to a group member take the deterministic rule path. A mention that resolves to
// no member, and a no-mention message, both fall through to the semantic arbiter
// when one is configured; otherwise the only auto-reply is a single-member web
// group, and every other group stays silent until a semantic arbiter is wired.
func (d *GroupDispatcher) decideResponders(ctx context.Context, q *sqlc.Queries, outbox sqlc.CtxGroupOutbox, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState, envelope GroupOutboxEnvelope, groupMembers []GroupMember) []string {
	if len(envelope.Mentions) > 0 {
		var responding []string
		if d.coord != nil && d.coord.arbiter != nil {
			responding = d.coord.arbiter.Decide(ctx, outbox.GroupID, envelope.Mentions, groupMembers, "", DecideOptions{
				AllMembersFallback: false,
				DisableDebounce:    true,
			}).RespondingAgents
		} else {
			responding = fallbackGroupDecision(envelope.Mentions, groupMembers).RespondingAgents
		}
		if len(responding) > 0 {
			return responding
		}
		// Mentions were present but none resolved to a member agent. Silently
		// dropping the turn here (the old behavior) made explicit mentions
		// strictly less reliable than no-mention routing: a bot-identity/registry
		// miss, a human-only @mention, or a mention of a non-member bot all looked
		// accepted (the platform acked) yet produced no reply (#619). Fall through
		// to the same no-mention path so semantic routing still gets a chance.
		// Debug, not Warn: @-mentioning a human or a non-member bot is ordinary
		// group traffic. The genuinely anomalous case — a mention that hit the bot
		// registry yet still failed to resolve — is logged per-mention in
		// resolveMentionAgentsWithMembers, so a Warn here would only bury it.
		d.log.Debug("group mention resolved to no member agent; falling back to no-mention routing",
			"group_id", outbox.GroupID,
			"platform", state.Platform,
			"mention_count", len(envelope.Mentions),
			"member_count", len(groupMembers),
		)
		// Fall through to the shared no-mention path below; it routes purely off
		// groupMembers and never re-reads envelope.Mentions.
	}
	if d.coord != nil && d.coord.semanticGroupArbiter != nil {
		return d.semanticResponders(ctx, q, outbox.GroupID, message, state, groupMembers)
	}
	// No semantic arbiter (degraded/legacy setup with no routing model). A resolved
	// @mention already returned above; the only remaining auto-reply is a
	// single-member web group. Every other group stays silent until a semantic
	// arbiter is wired — there is no all-members broadcast mode anymore.
	if state.Platform == "web" && len(groupMembers) == 1 {
		return []string{groupMembers[0].AgentID}
	}
	if len(groupMembers) > 1 {
		d.log.Warn("group has multiple members and no semantic arbiter; configure a routing model for group auto-reply", "group_id", outbox.GroupID, "platform", state.Platform, "member_count", len(groupMembers))
	}
	return nil
}

// semanticResponders builds the semantic request from current DB facts and asks
// the arbiter. Returns nil (no rows) on any silence/failure — the arbiter itself
// already collapses failures to silence.
func (d *GroupDispatcher) semanticResponders(ctx context.Context, q *sqlc.Queries, groupID string, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState, groupMembers []GroupMember) []string {
	smembers := make([]SemanticGroupMember, 0, len(groupMembers))
	for _, m := range groupMembers {
		a, err := q.GetAgent(ctx, m.AgentID)
		if err != nil {
			d.log.Warn("semantic routing: get agent failed", "group_id", groupID, "agent_id", m.AgentID, "error", err)
			continue
		}
		summary := a.SystemPrompt
		smembers = append(smembers, SemanticGroupMember{
			AgentID:        m.AgentID,
			Name:           a.Name,
			Scope:          a.Scope,
			CreatorID:      a.CreatorID,
			Summary:        summary,
			ReplyChannelID: m.ReplyChannelID,
		})
	}
	if len(smembers) == 0 {
		return nil
	}
	ownerUserID := ""
	if state.Platform == "web" {
		ownerUserID = nullStringValue(state.CreatedByUserID)
	}
	decision := d.coord.semanticGroupArbiter.Decide(ctx, SemanticGroupRequest{
		Message:       message.Content,
		RecentContext: d.recentGroupContext(ctx, q, groupID, message.Seq),
		Members:       smembers,
		OwnerUserID:   ownerUserID,
	})
	if !decision.ShouldReply {
		return nil
	}
	return decision.RespondingAgents
}

// recentGroupContext returns prior group messages oldest→newest. It caps by
// seq so delayed outbox retries never route using future messages.
func (d *GroupDispatcher) recentGroupContext(ctx context.Context, q *sqlc.Queries, groupID string, currentSeq int64) []SemanticGroupContextMessage {
	rows, err := q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{
		GroupID:   groupID,
		BeforeSeq: currentSeq,
		MaxCount:  int32(semanticMaxContextMessages),
	})
	if err != nil {
		d.log.Warn("semantic routing: list recent messages failed", "group_id", groupID, "error", err)
		return nil
	}
	out := make([]SemanticGroupContextMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		out = append(out, SemanticGroupContextMessage{
			ActorType: r.ActorType,
			ActorID:   r.ActorID,
			Content:   r.Content,
		})
	}
	return out
}

func (d *GroupDispatcher) executeDispatchesByMessage(ctx context.Context, groupMessageID string, publisherOverride GroupPublisher) error {
	for {
		rows, err := d.q.ListPendingGroupDispatchByMessage(ctx, sqlc.ListPendingGroupDispatchByMessageParams{
			GroupMessageID: groupMessageID,
			Now:            nullTime(time.Now().UTC()),
		})
		if err != nil {
			return fmt.Errorf("list dispatch rows: %w", err)
		}
		var errs []error
		for _, row := range rows {
			if err := d.ExecuteDispatch(ctx, row, publisherOverride); err != nil {
				errs = append(errs, err)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
		if publisherOverride == nil {
			return nil
		}
		remaining, err := d.q.CountNonTerminalGroupDispatchByMessage(ctx, groupMessageID)
		if err != nil {
			return fmt.Errorf("count remaining dispatch rows: %w", err)
		}
		if remaining == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// dispatchRun is one claimed attempt at one dispatch row, with the routing
// facts both the live and the re-delivery path need. The row's own fields say
// which path applies: a result_message_id means the agent already ran, and its
// response must be re-delivered rather than regenerated.
type dispatchRun struct {
	row       sqlc.CtxGroupDispatch
	message   sqlc.CtxGroupMessage
	state     sqlc.CtxGroupState
	publisher GroupPublisher
	agentName string
}

func (d *GroupDispatcher) ExecuteDispatch(ctx context.Context, row sqlc.CtxGroupDispatch, publisherOverride GroupPublisher) error {
	claimed, ok, err := d.claimDispatch(ctx, row)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if claimed.ResultMessageID != "" && claimed.DeliveryComplete {
		return d.completeDispatch(ctx, claimed)
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
	publisher, err := d.publisherFor(state, claimed, publisherOverride)
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	agentName := claimed.AgentID
	if a, err := d.q.GetAgent(ownedCtx, claimed.AgentID); err == nil && a.Name != "" {
		agentName = a.Name
	}
	run := dispatchRun{row: claimed, message: message, state: state, publisher: publisher, agentName: agentName}
	if claimed.ResultMessageID != "" {
		return d.redeliverDispatch(ctx, ownedCtx, run)
	}
	return d.generateAndPublish(ctx, ownedCtx, run)
}

// generateAndPublish runs the agent once and delivers its response. It records
// the response whether or not delivery succeeded: the recorded message is what
// a retry re-delivers, and a retry that cannot find one has no choice but to
// run the agent a second time.
func (d *GroupDispatcher) generateAndPublish(ctx, ownedCtx context.Context, run dispatchRun) error {
	claimed := run.row
	// A previous attempt that died mid-delivery left a cursor into a response
	// this run is about to replace. Regenerating voids it: keeping it would make
	// the publisher skip leading chunks of text nobody has seen.
	if claimed.DeliveryCursor > 0 {
		if _, err := d.q.ResetGroupDispatchDelivery(ownedCtx, sqlc.ResetGroupDispatchDeliveryParams{
			ID:           claimed.ID,
			AttemptCount: claimed.AttemptCount,
		}); err != nil {
			return d.failDispatch(ctx, claimed, fmt.Errorf("reset delivery cursor: %w", err))
		}
		claimed.DeliveryCursor = 0
	}
	stream, err := d.chat(ownedCtx, claimed, run.message, run.state)
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
	stream, responseC := d.wrapGroupResponseStream(ownedCtx, stream)
	// Same key chatDispatchUnqueued enqueues under: the per-(agent,group)
	// session queue is the one durable handle on this turn, so a cancel click
	// reuses its existing Abort rather than tracking the turn a second way.
	sessionKey := agent.BuildGroupSessionKey(claimed.AgentID, claimed.GroupID)
	req := d.publishRequest(run)
	req.Stream = stream
	req.Abort = func() bool { return d.queue.Abort(sessionKey) }
	publishErr := run.publisher.Publish(ownedCtx, req)
	response := d.awaitGroupResponse(ownedCtx, stream, responseC)
	if response.complete && response.text != "" {
		if err := d.recordDispatchResult(ctx, claimed, response); err != nil {
			return d.failDispatch(ctx, claimed, err)
		}
	}
	if publishErr != nil {
		return d.failDispatch(ctx, claimed, fmt.Errorf("publish: %w", publishErr))
	}
	d.markDelivered(ctx, claimed)
	return d.completeDispatch(ctx, claimed)
}

// redeliverDispatch finishes a delivery an earlier attempt left incomplete. The
// agent already ran and must not run again, so the only inputs are the
// persisted response and the cursor of chunks already confirmed.
func (d *GroupDispatcher) redeliverDispatch(ctx, ownedCtx context.Context, run dispatchRun) error {
	claimed := run.row
	result, err := d.q.GetGroupMessage(ownedCtx, claimed.ResultMessageID)
	if err != nil {
		// The response is durable but unreadable (a hand-written or orphaned
		// marker). Retire the row: re-running the agent would answer a message
		// the group has already been answered, which is worse than losing the
		// tail of one delivery.
		d.log.Warn("group dispatch cannot read its persisted response for re-delivery; retiring the row",
			"dispatch_id", claimed.ID, "result_message_id", claimed.ResultMessageID, "error", err)
		return d.completeDispatch(ctx, claimed)
	}
	req := d.publishRequest(run)
	req.Text = result.Content
	req.DeliveryCursor = claimed.DeliveryCursor
	if err := run.publisher.Publish(ownedCtx, req); err != nil {
		return d.failDispatch(ctx, claimed, fmt.Errorf("re-deliver: %w", err))
	}
	d.markDelivered(ctx, claimed)
	return d.completeDispatch(ctx, claimed)
}

// publishRequest builds the platform-independent half of a publish request.
// The caller adds exactly one of Stream (live) or Text (re-delivery).
func (d *GroupDispatcher) publishRequest(run dispatchRun) GroupPublishRequest {
	return GroupPublishRequest{
		GroupID:          run.row.GroupID,
		AgentID:          run.row.AgentID,
		AgentName:        run.agentName,
		ReplyChannelID:   run.row.ReplyChannelID,
		Platform:         run.state.Platform,
		PlatformGroupID:  run.state.PlatformGroupID,
		PlatformThreadID: run.state.PlatformThreadID,
		ReplyTo:          nullStringValue(run.message.PlatformMessageID),
		RequesterID:      run.message.ActorID,
		MarkDelivered:    d.deliveryConfirmer(run.row),
	}
}

// deliveryConfirmer returns the durable chunk confirmation for one claimed
// attempt. It fails when the advance does not land, which means this attempt
// lost the row to another owner — exactly when the publisher must stop sending.
func (d *GroupDispatcher) deliveryConfirmer(row sqlc.CtxGroupDispatch) func(context.Context, int64) error {
	return func(ctx context.Context, n int64) error {
		rows, err := d.q.AdvanceGroupDispatchDelivery(ctx, sqlc.AdvanceGroupDispatchDeliveryParams{
			ID:             row.ID,
			AttemptCount:   row.AttemptCount,
			DeliveryCursor: n,
		})
		if err != nil {
			return fmt.Errorf("advance group dispatch delivery cursor: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf("advance group dispatch delivery cursor to %d: dispatch ownership lost", n)
		}
		return nil
	}
}

// markDelivered records that the whole response reached the platform. A failure
// is logged, not returned: the delivery already happened, and requeueing over a
// bookkeeping error would risk publishing the response twice. The row still
// retires through its result marker.
func (d *GroupDispatcher) markDelivered(ctx context.Context, row sqlc.CtxGroupDispatch) {
	if _, err := d.q.MarkGroupDispatchDelivered(ctx, sqlc.MarkGroupDispatchDeliveredParams{
		ID:           row.ID,
		AttemptCount: row.AttemptCount,
	}); err != nil {
		d.log.Warn("mark group dispatch delivered failed", "dispatch_id", row.ID, "error", err)
	}
}

// awaitGroupResponse collects the buffered response after Publish returns. A
// publisher that fails mid-delivery returns without draining the stream, so the
// drain here is what keeps the wrapper goroutine from blocking forever on a
// full forward buffer — and what lets a failed publish still record its
// response for re-delivery.
func (d *GroupDispatcher) awaitGroupResponse(ctx context.Context, stream *pkgchannel.ChatStream, responseC <-chan groupResponse) groupResponse {
	for {
		select {
		case response := <-responseC:
			return response
		case _, ok := <-stream.Events:
			if ok {
				continue
			}
			// The wrapper closes the forwarding channel only after it has sent
			// its response, so this read cannot block for long.
			select {
			case response := <-responseC:
				return response
			case <-ctx.Done():
				return groupResponse{}
			}
		case <-ctx.Done():
			return groupResponse{}
		}
	}
}

func (d *GroupDispatcher) claimDispatch(ctx context.Context, row sqlc.CtxGroupDispatch) (sqlc.CtxGroupDispatch, bool, error) {
	switch row.Status {
	case "running":
		return row, true, nil
	case "pending":
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

func (d *GroupDispatcher) recordDispatchResult(ctx context.Context, row sqlc.CtxGroupDispatch, response groupResponse) error {
	if d.db == nil {
		return errors.New("dispatcher db not configured")
	}
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("record dispatch result: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := appdb.AdvisoryXactLock(ctx, tx, "gid:"+row.GroupID); err != nil {
		return err
	}
	q := sqlc.New(tx)
	result, err := eventlog.AppendToGroupWithQueries(ctx, q, row.GroupID, eventlog.GroupMessage{
		ActorType:      eventlog.ActorAgent,
		ActorID:        row.AgentID,
		Content:        response.text,
		AgentSessionID: response.sessionID,
	})
	if err != nil {
		return err
	}
	updated, err := q.SetGroupDispatchResultMessage(ctx, sqlc.SetGroupDispatchResultMessageParams{
		ID:              row.ID,
		AttemptCount:    row.AttemptCount,
		ResultMessageID: result.Message.ID,
	})
	if err != nil {
		return fmt.Errorf("set dispatch result message: %w", err)
	}
	if updated == 0 {
		return errors.New("set dispatch result message: lost dispatch ownership")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("record dispatch result: commit: %w", err)
	}
	return nil
}

func (d *GroupDispatcher) publisherFor(state sqlc.CtxGroupState, row sqlc.CtxGroupDispatch, override GroupPublisher) (GroupPublisher, error) {
	if override != nil {
		return override, nil
	}
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
var errGroupTurnSuperseded = errors.New("group turn superseded by the agent's ingest cursor")

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
	sessionID string
	complete  bool
}

func (d *GroupDispatcher) wrapGroupResponseStream(ctx context.Context, stream *pkgchannel.ChatStream) (*pkgchannel.ChatStream, <-chan groupResponse) {
	out := make(chan pkgchannel.Event, 100)
	responseC := make(chan groupResponse, 1)
	go func() {
		defer close(out)
		var textBuf strings.Builder
		sawErr := false
		for evt := range stream.Events {
			if evt.Err != nil {
				sawErr = true
			}
			if evt.Text != "" {
				textBuf.WriteString(evt.Text)
			}
			select {
			case out <- evt:
			case <-ctx.Done():
			}
		}
		// The forwarding select can win the race against ctx.Done, so
		// cancellation must be re-checked deterministically before treating the
		// buffered text as a complete reply.
		if ctx.Err() != nil {
			sawErr = true
		}
		responseC <- groupResponse{text: textBuf.String(), sessionID: stream.SessionID, complete: !sawErr}
		close(responseC)
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: stream.SessionID}, responseC
}

// fallbackGroupDecision resolves responders when no arbiter is wired: only
// mentions that name a current member reply; anything else stays silent.
func fallbackGroupDecision(mentions []pkgchannel.Mention, members []GroupMember) ArbiterDecision {
	mentioned := mentionedAgentIDs(mentions)
	if len(mentioned) == 0 {
		return ArbiterDecision{}
	}
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m.AgentID] = struct{}{}
	}
	var responding []string
	for _, id := range mentioned {
		if _, ok := memberSet[id]; ok {
			responding = append(responding, id)
		}
	}
	return ArbiterDecision{RespondingAgents: responding}
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
