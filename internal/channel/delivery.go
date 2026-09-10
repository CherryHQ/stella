package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agentrun"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var (
	// errDeliveryUnauthorized refuses an outbound send that no durable ownership
	// fact authorizes. It is deliberately not a transport error: the caller must
	// not retry, because the turn that produced the bytes no longer owns them.
	errDeliveryUnauthorized = errors.New("channel: outbound send is not authorized")
	// errDeliverySettled reports a send attempted after this stream's delivery
	// outcome was already recorded.
	errDeliverySettled = errors.New("channel: delivery attempt is already settled")
	// errDeliveryStoreUnavailable means the delivery record cannot be consulted.
	// Failing closed is the only safe answer: an unverifiable send is exactly the
	// duplicate this record exists to prevent.
	errDeliveryStoreUnavailable = errors.New("channel: delivery record store is unavailable")
)

// deliveryTarget is resolved from trusted channel configuration and inbound routing.
type deliveryTarget struct {
	Platform  string
	ChannelID string
	ChatID    string
	ThreadID  string
	ReplyTo   string
}

// addressable reports whether a delivery owner exists for this target. A turn
// with no addressable target has no external delivery claim.
func (t deliveryTarget) addressable() bool { return t.Platform != "" && t.ChatID != "" }

// runDelivery authorizes live output through its Run and committed output through
// agent_run_output. Each external request records an attempt before sending.
type runDelivery struct {
	db     *pgxpool.Pool
	q      *sqlc.Queries
	lease  *agentrun.Lease
	runID  string
	target deliveryTarget

	done      chan struct{}
	settle    sync.Once
	settleErr error
	payloadMu sync.Mutex
	snapshots []string
	nextEvent int64
}

// createRunDelivery records routing before any external output can leave.
func createRunDelivery(ctx context.Context, db *pgxpool.Pool, lease *agentrun.Lease, target deliveryTarget) (*runDelivery, error) {
	if lease == nil || !target.addressable() {
		return nil, nil
	}
	if db == nil {
		return nil, fmt.Errorf("%w: %w", errDeliveryStoreUnavailable, errors.New("delivery record database is not configured"))
	}
	q := sqlc.New(db)
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentrun.ValidateTx(lease.ContextWith(ctx), tx); err != nil {
		return nil, err
	}
	if _, err := q.WithTx(tx).CreateAgentRunOutput(ctx, sqlc.CreateAgentRunOutputParams{
		RunID: lease.Guard.RunID, SessionID: lease.Guard.SessionID,
		Platform: target.Platform, ChannelID: target.ChannelID, ChatID: target.ChatID,
		ThreadID: target.ThreadID, ReplyTo: target.ReplyTo,
	}); err != nil {
		return nil, fmt.Errorf("create delivery record: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &runDelivery{
		db: db, q: q, lease: lease, runID: lease.Guard.RunID, target: target,
		done: make(chan struct{}),
	}, nil
}

// Authorize records the attempt before allowing an external request.
func (d *runDelivery) Authorize(ctx context.Context, kind pkgchannel.SendKind) error {
	if d == nil {
		return errDeliveryUnauthorized
	}
	select {
	case <-d.done:
		return errDeliverySettled
	default:
	}
	if d.q == nil || d.runID == "" {
		return errDeliveryStoreUnavailable
	}
	state, err := d.q.AuthorizeAgentRunOutputSend(ctx, sqlc.AuthorizeAgentRunOutputSendParams{
		RunID: d.runID, Platform: d.target.Platform, ChannelID: d.target.ChannelID,
		ChatID: d.target.ChatID, ThreadID: d.target.ThreadID, ReplyTo: d.target.ReplyTo,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// No record for this Run and this exact target: this delivery attempt is
		// over, was superseded, or belongs to someone else's routing.
		return errDeliveryUnauthorized
	}
	if err != nil {
		return fmt.Errorf("authorize channel send: %w", err)
	}
	if state == outputStateReady {
		return nil
	}
	// The Run still owns the turn. Model-derived output may only be sent while
	// that ownership is valid; a channel-owned operational message may still be
	// composed while this handler holds the turn, but never once the record is
	// settled (checked above).
	if d.lease != nil {
		err := agentrun.Check(d.lease.ContextWith(ctx))
		if err == nil {
			return nil
		}
		if !errors.Is(err, agentrun.ErrLeaseLost) && !errors.Is(err, agentrun.ErrInvalidGuard) {
			return err
		}
		// Finish may have committed between the marker and the execution check.
		// The target is immutable; only its now-committed output can replace the
		// execution authority we just lost.
		output, readErr := d.q.GetAgentRunOutput(ctx, d.runID)
		if readErr != nil {
			return fmt.Errorf("read committed channel output: %w", readErr)
		}
		if output.State == outputStateReady {
			return nil
		}
	}
	if kind == pkgchannel.SendControl {
		return nil
	}
	return fmt.Errorf("%w: execution ownership is not valid", errDeliveryUnauthorized)
}

// Settle records delivery once and releases the chat FIFO even if the write fails.
// An unsettled attempted record remains unknown and must not be replayed.
func (d *runDelivery) Settle(ctx context.Context, result pkgchannel.DeliveryResult) error {
	if d == nil {
		return nil
	}
	d.settle.Do(func() {
		defer func() {
			d.payloadMu.Lock()
			defer d.payloadMu.Unlock()
			close(d.done)
			for _, path := range d.snapshots {
				_ = os.Remove(path)
			}
			d.snapshots = nil
		}()
		if d.q == nil || d.runID == "" {
			d.settleErr = errDeliveryStoreUnavailable
			return
		}
		rows, err := d.q.SettleAgentRunOutput(ctx, sqlc.SettleAgentRunOutputParams{
			RunID: d.runID, State: string(result),
		})
		if err != nil {
			d.settleErr = fmt.Errorf("settle channel delivery: %w", err)
		} else if rows != 1 {
			d.settleErr = errDeliveryUnauthorized
		}
	})
	return d.settleErr
}

// Done closes when the delivery attempt is over. It reports delivery state only;
// Run execution state is not visible here.
func (d *runDelivery) Done() <-chan struct{} {
	if d == nil {
		return nil
	}
	return d.done
}

// outputStateReady is the agent_run_output state that means "the committed output
// exists and its channel may deliver it".
const outputStateReady = "ready"

// dispatchDelivery uses the group dispatcher's committed publish attempt as its
// authority. The dispatcher owns the durable delivery result.
type dispatchDelivery struct {
	q       *sqlc.Queries
	rowID   string
	attempt int64

	done      chan struct{}
	settle    sync.Once
	settleErr error
}

func newDispatchDelivery(q *sqlc.Queries, rowID string, attempt int64) *dispatchDelivery {
	return &dispatchDelivery{q: q, rowID: rowID, attempt: attempt, done: make(chan struct{})}
}

func (d *dispatchDelivery) Authorize(ctx context.Context, _ pkgchannel.SendKind) error {
	if d == nil || d.q == nil {
		return errDeliveryStoreUnavailable
	}
	select {
	case <-d.done:
		return errDeliverySettled
	default:
	}
	_, err := d.q.LockGroupDispatchPublishOwnership(ctx, sqlc.LockGroupDispatchPublishOwnershipParams{
		ID: d.rowID, AttemptCount: d.attempt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return errDeliveryUnauthorized
	}
	if err != nil {
		return fmt.Errorf("authorize group publish: %w", err)
	}
	return nil
}

// Settle confirms this attempt still owns the dispatch row and releases the
// per-group FIFO slot.
func (d *dispatchDelivery) Settle(ctx context.Context, _ pkgchannel.DeliveryResult) error {
	if d == nil {
		return nil
	}
	d.settle.Do(func() {
		defer close(d.done)
		if checkErr := d.Authorize(ctx, pkgchannel.SendOutput); checkErr != nil && !errors.Is(checkErr, errDeliveryUnauthorized) {
			d.settleErr = fmt.Errorf("settle group publish: %w", checkErr)
		}
	})
	return d.settleErr
}

func (d *dispatchDelivery) Done() <-chan struct{} {
	if d == nil {
		return nil
	}
	return d.done
}

// commitTurnHandoff publishes output and finishes its Run atomically. A durable
// abort can override the requested status and prevent output publication.
func commitTurnHandoff(ctx context.Context, lease *agentrun.Lease, outcome agentruntime.TurnOutcome, deliverable bool) error {
	if lease == nil {
		// Admission failed: the runtime released ownership itself and returned an
		// error, so the caller has nothing to finish.
		return nil
	}
	if !deliverable {
		return lease.Finish(ctx, outcome.Status, outcome.Reason)
	}
	err := lease.FinishWith(ctx, outcome.Status, outcome.Reason, func(ctx context.Context, tx pgx.Tx, written string) error {
		if written != outcome.Status {
			// A durable stop request won. Nothing may be published for delivery:
			// the channel may only settle its attempt or send its own error notice.
			return nil
		}
		media, err := json.Marshal(outcome.Output.Media)
		if err != nil {
			return fmt.Errorf("encode delivery media references: %w", err)
		}
		rows, err := sqlc.New(tx).CompleteAgentRunOutput(ctx, sqlc.CompleteAgentRunOutputParams{
			RunID: lease.Guard.RunID, Text: outcome.Output.Text, Media: media,
		})
		if err != nil {
			return fmt.Errorf("publish delivery record: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf("%w: delivery record is missing or already published", errDeliveryUnauthorized)
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, agentrun.ErrLeaseLost) {
		// Recovery or a replacement decided this Run; there is nothing to publish.
		return err
	}
	// The output commit failed. The Run must not be reported as completed, and the
	// caller must not be told the reply exists.
	return finishFailedHandoff(ctx, lease, "commit delivery handoff: "+err.Error())
}

// deliverableTurn includes durable timeout notices from an otherwise failed turn.
func deliverableTurn(outcome agentruntime.TurnOutcome, target deliveryTarget, delivery *runDelivery) bool {
	return delivery != nil && target.addressable() &&
		outcome.CommitErr == nil && (outcome.Status == agentrun.StatusCompleted ||
		(outcome.Status == agentrun.StatusFailed && outcome.Output.Deliverable()))
}

// A missing output handoff is a failed execution, never a successful reply.
func finishFailedHandoff(ctx context.Context, lease *agentrun.Lease, cause string) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryCommitTimeout)
	defer cancel()
	err := lease.Finish(finishCtx, agentrun.StatusFailed, cause)
	if err != nil && !errors.Is(err, agentrun.ErrLeaseLost) && !errors.Is(err, agentrun.ErrStoreClosed) {
		return errors.Join(errors.New(cause), fmt.Errorf("fail AgentRun after handoff error: %w", err))
	}
	if err != nil {
		return errors.Join(errors.New(cause), err)
	}
	return errors.New(cause)
}

// deliveryCommitTimeout bounds the receipt/terminal commit so a database stall
// cannot pin its goroutine for the Run's whole lease.
const deliveryCommitTimeout = 5 * time.Second
