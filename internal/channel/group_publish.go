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
	"github.com/jackc/pgx/v5/pgxpool"

	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/internal/eventlog"
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
// acceptedPublishRecoveryPrefix is persisted in last_error solely to preserve
// the distinct retry class across claim/requeue cycles without a schema change.
const acceptedPublishRecoveryPrefix = "accepted_publish_recovery:"

type acceptedPublishBookkeepingError struct{ err error }

func (e *acceptedPublishBookkeepingError) Error() string {
	return "accepted publish bookkeeping: " + e.err.Error()
}
func (e *acceptedPublishBookkeepingError) Unwrap() error { return e.err }

func isAcceptedPublishRecovery(row sqlc.CtxGroupDispatch, err error) bool {
	var bookkeeping *acceptedPublishBookkeepingError
	// Once a prior send outcome is ambiguous, a later explicit failure proves
	// only that later attempt failed; it cannot prove the earlier platform send
	// was absent. Preserve the wider recovery class until its own ceiling.
	return strings.HasPrefix(row.LastError, acceptedPublishRecoveryPrefix) || errors.As(err, &bookkeeping)
}

type groupPublishDriver struct {
	db     *pgxpool.Pool
	q      *sqlc.Queries
	events *GroupEventHub
	log    *slog.Logger
	// outbox routes the platform send through the durable channel_outbox
	// ledger: the accept transaction commits the ops and the channel's
	// lease owner performs the send.
	outbox *choutbox.Store
	// wake re-polls the dispatcher after a successor outbox is committed.
	wake func()
}

func newGroupPublishDriver(db *pgxpool.Pool, q *sqlc.Queries, log *slog.Logger, wake func()) *groupPublishDriver {
	return &groupPublishDriver{db: db, q: q, log: log, wake: wake}
}

// errPublishEnqueued tells publishAccepted the send became a durable outbox op:
// the row stays 'running' until pollPublishOutcomes observes the op's state.
var errPublishEnqueued = errors.New("group publish: enqueued to durable outbox")

// groupReplyPlan maps the reply channel's platform to the group reply
// decomposition: one durable op per platform call, mirroring replyPlanFor.
// QQ folds media into text markers — its group API has no binary upload.
func groupReplyPlan(platform string) choutbox.ReplyPlan {
	switch platform {
	case "telegram":
		return choutbox.ReplyPlan{TextLimit: 4000, PrimaryText: true, Attachments: true}
	case "discord":
		return choutbox.ReplyPlan{TextLimit: 2000, PrimaryText: true, Attachments: true}
	case "qq":
		return choutbox.ReplyPlan{TextLimit: 3500, PrimaryText: true, MediaAsText: true}
	case "dingtalk":
		return choutbox.ReplyPlan{TextLimit: 18000, PrimaryText: true}
	default:
		// feishu cards carry the whole response; testchan takes one send.
		return choutbox.ReplyPlan{PrimaryText: true, Attachments: true}
	}
}

// appendPreparedOps binds the pre-built op chain to the reply channel's
// registered platform account — snapshotted under the accept lock so a
// re-bound account cannot inherit pending replies — then commits it with the
// publish-started marker. The trigger's SourceAccountKey is audit-only: a
// shared trigger can wake members that reply through a different channel and
// account.
func (p *groupPublishDriver) appendPreparedOps(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, row sqlc.CtxGroupDispatch, ops []choutbox.Op) error {
	replyChannel, err := q.GetChannel(ctx, row.ReplyChannelID)
	if err != nil {
		return fmt.Errorf("resolve reply channel account: %w", err)
	}
	for i := range ops {
		ops[i].AccountKey = replyChannel.RuntimeAccountKey.String
	}
	if err := p.outbox.Append(ctx, tx, ops); err != nil {
		return fmt.Errorf("enqueue group publish: append: %w", err)
	}
	if !row.PublishStartedAt.Valid {
		affected, err := q.MarkGroupDispatchPublishStarted(ctx, sqlc.MarkGroupDispatchPublishStartedParams{ID: row.ID, AttemptCount: row.AttemptCount})
		if err != nil {
			return fmt.Errorf("mark publish started: %w", err)
		}
		if affected == 0 {
			return errors.New("lost dispatch ownership")
		}
	}
	return nil
}

// publishJob is one egress confirmation: the accepted reply and the routing
// state it is delivered through.
type publishJob struct {
	row   sqlc.CtxGroupDispatch
	state sqlc.CtxGroupState
	// acceptedMessageID is the canonical row this publish is rendering. It is
	// empty on the recovery path, where the row already carries the id.
	acceptedMessageID string
}

// run performs one egress attempt and returns the dispatch row it worked on.
// Platform success is committed before any local finalization, so a retry that
// sees published_at repairs only DB bookkeeping and never repeats the side effect.
func (p *groupPublishDriver) run(ctx context.Context, job publishJob) (sqlc.CtxGroupDispatch, error) {
	row := job.row
	if row.ResultMessageID == "" && job.acceptedMessageID != "" {
		row.ResultMessageID = job.acceptedMessageID
	}
	if row.PublishedAt.Valid {
		if err := p.finalizeAcceptedPublished(ctx, row); err != nil {
			return row, &acceptedPublishBookkeepingError{err: err}
		}
		return row, nil
	}
	if p.outbox == nil {
		return row, errors.New("publish: durable outbox not configured")
	}
	// Web replies carry no platform send — the event log is their egress — so
	// the accepted reply publishes 'noop' and converges immediately.
	if job.state.Platform == webGroupPlatform {
		if err := p.markPublished(ctx, row); err != nil {
			return row, &acceptedPublishBookkeepingError{err: err}
		}
		// MarkGroupDispatchPublished is a committed standalone statement. Carry
		// the durable fact locally so a finalization error cannot fall back
		// into the ordinary publish-failure path merely because a follow-up
		// read failed.
		row.PublishedAt = nullTime(time.Now().UTC())
		if err := p.finalizeAcceptedPublished(ctx, row); err != nil {
			return row, &acceptedPublishBookkeepingError{err: err}
		}
		return row, nil
	}
	// The send ops committed inside the accept transaction; the channel's
	// lease owner performs the send and pollPublishOutcomes retires the row.
	return row, errPublishEnqueued
}

// markPublished is deliberately one statement after the publisher returns.
// It is the durable boundary between an externally successful send and local
// recovery work; do not fold it into finalizeAcceptedPublished below.
func (p *groupPublishDriver) markPublished(ctx context.Context, row sqlc.CtxGroupDispatch) error {
	updated, err := p.q.MarkGroupDispatchPublished(ctx, sqlc.MarkGroupDispatchPublishedParams{ID: row.ID, AttemptCount: row.AttemptCount, ResultMessageID: row.ResultMessageID})
	if err != nil {
		return fmt.Errorf("mark dispatch published: %w", err)
	}
	if updated == 0 {
		return errors.New("mark dispatch published: lost dispatch ownership")
	}
	return nil
}

// finalizeAcceptedPublished contains only idempotent local DB work. A retry
// after published_at is set may call it any number of times without publishing.
func (p *groupPublishDriver) finalizeAcceptedPublished(ctx context.Context, row sqlc.CtxGroupDispatch) error {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("finalize accepted publish: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.q.WithTx(tx)
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
		return fmt.Errorf("finalize accepted publish: commit: %w", err)
	}
	p.wake()
	if p.events != nil {
		p.events.Announce(eventlog.AppendResult{GroupID: row.GroupID, Seq: message.Seq, Message: message})
		// The terminal frame for an accepted turn, emitted after the message so a
		// subscriber never retires the presence badge before the reply it was
		// waiting for lands. Without it the browser would have to guess when the
		// dispatcher's 'running' frame stopped being true.
		p.events.AnnounceTurn(row.GroupID, row.AgentID, "done", "")
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

func (p *groupPublishDriver) failAcceptedPublishWithExpiryFence(ctx context.Context, row sqlc.CtxGroupDispatch, cause error, expiredAt time.Time) error {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fail accepted publish: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.q.WithTx(tx)
	var updated int64
	if expiredAt.IsZero() {
		updated, err = q.MarkGroupDispatchFailed(ctx, sqlc.MarkGroupDispatchFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: cause.Error()})
	} else {
		updated, err = q.MarkExpiredGroupDispatchFailed(ctx, sqlc.MarkExpiredGroupDispatchFailedParams{
			ID: row.ID, AttemptCount: row.AttemptCount, LastError: cause.Error(), Now: nullTime(expiredAt),
		})
	}
	if err != nil {
		return fmt.Errorf("mark dispatch failed: %w", err)
	}
	if updated == 0 {
		return errors.New("mark dispatch failed: lost dispatch ownership")
	}
	message, err := q.SetGroupMessageDeliveryState(ctx, sqlc.SetGroupMessageDeliveryStateParams{ID: row.ResultMessageID, DeliveryState: "failed"})
	if err != nil {
		return fmt.Errorf("mark group message failed: %w", err)
	}
	if _, err := q.RequeueHeldGroupDispatchesAfterAcceptedPost(ctx, sqlc.RequeueHeldGroupDispatchesAfterAcceptedPostParams{
		GroupID: row.GroupID, AcceptedSeq: message.Seq,
	}); err != nil {
		return fmt.Errorf("requeue held peers after failed delivery: %w", err)
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
