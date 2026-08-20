package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/eventlog"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// groupPublishDriver owns egress for an accepted group reply: routing it to a
// publisher, recording the delivery on the canonical message, waking the peers
// it unblocks, and compensating when delivery is finally lost.
//
// It deliberately decides no retry policy. Attempt counting, requeue and the
// terminal state of a dispatch row belong to GroupDispatcher, which calls
// failDispatch/completeDispatch on what run returns. The dependency edge only
// ever points dispatcher -> driver.
type groupPublishDriver struct {
	db         *pgxpool.Pool
	q          *sqlc.Queries
	publishers *PublisherRegistry
	events     *GroupEventHub
	log        *slog.Logger
	// wake re-polls the dispatcher after a successor outbox is committed.
	wake func()
	// abort stops the turn still running behind a session key, for publishers
	// that expose a Cancel control. The session queue lives in the chat resolver.
	abort func(sessionKey string) bool
}

func newGroupPublishDriver(db *pgxpool.Pool, q *sqlc.Queries, publishers *PublisherRegistry, log *slog.Logger, wake func(), abort func(string) bool) *groupPublishDriver {
	return &groupPublishDriver{db: db, q: q, publishers: publishers, log: log, wake: wake, abort: abort}
}

// publishJob is one egress attempt: the accepted reply, the trigger it answers,
// and the routing state it is delivered through.
type publishJob struct {
	row       sqlc.CtxGroupDispatch
	trigger   sqlc.CtxGroupMessage
	state     sqlc.CtxGroupState
	publisher GroupPublisher
	response  groupResponse
	envelope  GroupOutboxEnvelope
	// acceptedMessageID is the canonical row this publish is rendering. It is
	// empty on the recovery path, where the row already carries the id.
	acceptedMessageID string
	// finalAttempt tells the publisher this is the last try, so it can render a
	// terminal notice instead of a retryable one. The dispatcher owns the count.
	finalAttempt bool
}

// run performs one egress attempt and returns the dispatch row it worked on
// (its result marker may have been filled in here). A non-nil error is for the
// dispatcher's retry policy to apply; the row's state has not been retired.
func (p *groupPublishDriver) run(ctx context.Context, job publishJob) (sqlc.CtxGroupDispatch, error) {
	row := job.row
	if row.ResultMessageID == "" && job.acceptedMessageID != "" {
		row.ResultMessageID = job.acceptedMessageID
	}
	sessionKey := agent.BuildGroupSessionKey(row.AgentID, row.GroupID)
	if row.ResultMessageID != "" {
		if row.PublishStartedAt.Valid {
			// The previous attempt reached the platform and never reported back,
			// so this reply may already be visible. Chosen deliberately: a group
			// assistant that repeats itself is recoverable, one that silently
			// drops an answer is not. Platform-side dedup needs an idempotency
			// key the channel APIs do not offer today.
			p.log.Warn("republishing an accepted group reply whose delivery outcome is unknown", "dispatch_id", row.ID, "result_message_id", row.ResultMessageID, "upgrade_trigger", "per-platform idempotency keys would remove the duplicate")
		} else if _, err := p.q.MarkGroupDispatchPublishStarted(ctx, sqlc.MarkGroupDispatchPublishStartedParams{ID: row.ID, AttemptCount: row.AttemptCount}); err != nil {
			return row, fmt.Errorf("mark publish started: %w", err)
		}
	}
	err := job.publisher.Publish(ctx, GroupPublishRequest{
		GroupID: row.GroupID, AgentID: row.AgentID,
		ReplyChannelID: row.ReplyChannelID, Platform: job.state.Platform,
		PlatformGroupID: job.state.PlatformGroupID, PlatformThreadID: job.state.PlatformThreadID,
		ReplyTo: nullStringValue(job.trigger.PlatformMessageID), Stream: replayGroupResponse(job.response),
		RequesterID: job.trigger.ActorID, LifecycleFeedback: job.envelope.LifecycleFeedback,
		Abort:        func() bool { return p.abort(sessionKey) },
		FinalAttempt: job.finalAttempt,
	})
	if err != nil {
		// A returned error is an outcome: the next attempt is an ordinary retry,
		// not a recovery from an unknown state. An accepted row that has run out
		// of attempts still reaches failAcceptedPublish, via failDispatch.
		if _, clearErr := p.q.ClearGroupDispatchPublishStarted(ctx, sqlc.ClearGroupDispatchPublishStartedParams{ID: row.ID, AttemptCount: row.AttemptCount}); clearErr != nil {
			p.log.Warn("clear publish start marker failed", "dispatch_id", row.ID, "error", clearErr)
		}
		return row, fmt.Errorf("publish: %w", err)
	}
	if err := p.markAcceptedPublished(ctx, row); err != nil {
		return row, err
	}
	return row, nil
}

func (p *groupPublishDriver) publisherFor(state sqlc.CtxGroupState, row sqlc.CtxGroupDispatch) (GroupPublisher, error) {
	if publisher, ok := p.publishers.Get(row.ReplyChannelID); ok {
		return publisher, nil
	}
	if state.Platform == webGroupPlatform {
		// Web is a platform whose egress is the event log the browser already
		// reads, so its publisher does nothing. Everything else about the turn
		// -- publish markers, delivery state, compensation -- stays identical.
		return NoopGroupPublisher(), nil
	}
	return nil, fmt.Errorf("publisher %q not registered", row.ReplyChannelID)
}

func (p *groupPublishDriver) markAcceptedPublished(ctx context.Context, row sqlc.CtxGroupDispatch) error {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark accepted publish: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.q.WithTx(tx)
	updated, err := q.MarkGroupDispatchPublished(ctx, sqlc.MarkGroupDispatchPublishedParams{ID: row.ID, AttemptCount: row.AttemptCount, ResultMessageID: row.ResultMessageID})
	if err != nil {
		return fmt.Errorf("mark dispatch published: %w", err)
	}
	if updated == 0 {
		return errors.New("mark dispatch published: lost dispatch ownership")
	}
	// Every platform gets the successor outbox: it is what wakes the peers, and
	// without it agent-to-agent collaboration silently exists on web only. No
	// platform echoes a bot's own message back through ingest, so this is the
	// only path that carries an agent post to its peers. Chain length stays
	// bounded by triage (agent_lap, agent_chain_hard_limit) and the accept caps.
	if err := p.createAgentReplyOutbox(ctx, q, row); err != nil {
		return err
	}
	// Delivery is a fact about the publisher returning, not about which one ran:
	// the noop web publisher earns 'delivered' the same way a platform API call
	// does, and the browser gets the pending -> delivered frame every other
	// surface already emits.
	message, err := q.SetGroupMessageDeliveryState(ctx, sqlc.SetGroupMessageDeliveryStateParams{ID: row.ResultMessageID, DeliveryState: "delivered"})
	if err != nil {
		return fmt.Errorf("mark group message delivered: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mark accepted publish: commit: %w", err)
	}
	p.wake()
	if p.events != nil {
		p.events.Announce(eventlog.AppendResult{GroupID: row.GroupID, Seq: message.Seq, Message: message})
	}
	return nil
}

func (p *groupPublishDriver) createAgentReplyOutbox(ctx context.Context, q *sqlc.Queries, row sqlc.CtxGroupDispatch) error {
	message, err := q.GetGroupMessage(ctx, row.ResultMessageID)
	if err != nil {
		return fmt.Errorf("get accepted agent reply: %w", err)
	}
	members, err := q.ListGroupMembers(ctx, row.GroupID)
	if err != nil {
		return fmt.Errorf("list group members: %w", err)
	}
	mentions := parseGroupMentions(ctx, q, message.Content, members)
	envelope, err := encodeGroupOutboxEnvelope(GroupOutboxEnvelope{Mentions: mentions})
	if err != nil {
		return fmt.Errorf("encode agent reply outbox: %w", err)
	}
	err = q.CreateGroupOutboxIfAbsent(ctx, sqlc.CreateGroupOutboxIfAbsentParams{
		ID: uuid.Must(uuid.NewV7()).String(), GroupMessageID: row.ResultMessageID, GroupID: row.GroupID,
		Envelope: envelope, Status: "pending", LastError: "",
	})
	if err != nil {
		return fmt.Errorf("create agent reply outbox: %w", err)
	}
	return nil
}

func (p *groupPublishDriver) failAcceptedPublish(ctx context.Context, row sqlc.CtxGroupDispatch, cause error) error {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fail accepted publish: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.q.WithTx(tx)
	message, err := q.SetGroupMessageDeliveryState(ctx, sqlc.SetGroupMessageDeliveryStateParams{ID: row.ResultMessageID, DeliveryState: "failed"})
	if err != nil {
		return fmt.Errorf("mark group message failed: %w", err)
	}
	if _, err := q.RequeueHeldGroupDispatchesAfterAcceptedPost(ctx, sqlc.RequeueHeldGroupDispatchesAfterAcceptedPostParams{
		GroupID: row.GroupID, AcceptedAt: message.CreatedAt,
	}); err != nil {
		return fmt.Errorf("requeue held peers after failed delivery: %w", err)
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
	p.log.Warn("group reply delivery failed permanently", "dispatch_id", row.ID, "result_message_id", row.ResultMessageID, "error", cause)
	if p.events != nil {
		p.events.Announce(eventlog.AppendResult{GroupID: row.GroupID, Seq: message.Seq, Message: message})
		p.events.AnnounceTurn(row.GroupID, row.AgentID, "failed", cause.Error())
	}
	return cause
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
