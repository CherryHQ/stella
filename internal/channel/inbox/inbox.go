// Package inbox owns channel_inbox, the durable record of accepted inbound
// channel events. Receiving and starting the model are two separate commits:
// Receive only lands the event (dedup-safe) and a per-channel sequence; the
// router later resolves identity and mints a run, then marks the event
// routed. No SDK, model, or tool call ever happens inside these transactions.
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Event kinds. A platform edit or button callback is a distinct kind, never
// collapsed onto a message_id dedup that would swallow legitimate updates.
const (
	KindMessage     = "message"
	KindMessageEdit = "message_edit"
	KindCallback    = "callback"
	KindCommand     = "command"
	KindLifecycle   = "lifecycle"
)

// Inbox states.
const (
	StateReceived = "received"
	StateReady    = "ready"
	StateRouted   = "routed"
	StateRejected = "rejected"
	StateFailed   = "failed"
)

// Stable error codes for error_code.
const (
	ErrCodeNoEventKey  = "no_event_key"
	ErrCodeUnboundChat = "unbound_chat"
	ErrCodeTargetGone  = "target_gone"
)

// ErrChannelGone reports that the channel row the event arrived under no
// longer exists — the receive cannot be sequenced.
var ErrChannelGone = errors.New("channel gone")

// Store composes the short-transaction channel_inbox API.
type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ReceiveParams is one normalized inbound event.
type ReceiveParams struct {
	ChannelID        string
	SourceAccountKey string
	EventKey         string // stable platform event id; empty is refused
	EventKind        string
	PayloadVersion   int
	Payload          json.RawMessage
	ChatKey          string
	// Ready promotes straight past 'received' when no async preparation
	// (attachment staging) is needed.
	Ready bool
}

// Receive lands the event in a short transaction: it locks the channel row,
// assigns the next ingress_seq, and inserts dedup-safe. A platform redelivery
// returns the original row with created=false — the caller attaches or stops,
// never re-executes.
func (s *Store) Receive(ctx context.Context, p ReceiveParams) (sqlc.ChannelInbox, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return sqlc.ChannelInbox{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, created, err := s.ReceiveTx(ctx, tx, p)
	if err != nil {
		return sqlc.ChannelInbox{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.ChannelInbox{}, false, err
	}
	return row, created, nil
}

// ReceiveTx is Receive inside the caller's transaction. It acquires the
// channel row lock itself, so callers composing a larger receive transaction
// must not already hold a conflicting lock on the same row.
func (s *Store) ReceiveTx(ctx context.Context, tx pgx.Tx, p ReceiveParams) (sqlc.ChannelInbox, bool, error) {
	if p.EventKey == "" {
		return sqlc.ChannelInbox{}, false, fmt.Errorf("inbox: %s", ErrCodeNoEventKey)
	}
	if p.ChannelID == "" || p.SourceAccountKey == "" || p.EventKind == "" {
		return sqlc.ChannelInbox{}, false, fmt.Errorf("inbox: channel, account and event kind are required")
	}
	q := sqlc.New(tx)
	if _, err := q.GetChannelForUpdate(ctx, p.ChannelID); errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ChannelInbox{}, false, ErrChannelGone
	} else if err != nil {
		return sqlc.ChannelInbox{}, false, err
	}
	seq, err := q.NextChannelIngressSeq(ctx, p.ChannelID)
	if err != nil {
		return sqlc.ChannelInbox{}, false, err
	}
	payload := p.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	row, err := q.InsertChannelInbox(ctx, sqlc.InsertChannelInboxParams{
		ChannelID:        p.ChannelID,
		SourceAccountKey: p.SourceAccountKey,
		EventKey:         p.EventKey,
		EventKind:        p.EventKind,
		PayloadVersion:   int32(p.PayloadVersion),
		Payload:          payload,
		IngressSeq:       seq,
		ChatKey:          p.ChatKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := q.GetChannelInboxByEvent(ctx, sqlc.GetChannelInboxByEventParams{
			ChannelID:        p.ChannelID,
			SourceAccountKey: p.SourceAccountKey,
			EventKey:         p.EventKey,
		})
		if getErr != nil {
			return sqlc.ChannelInbox{}, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return sqlc.ChannelInbox{}, false, err
	}
	if p.Ready {
		if _, err := q.UpdateChannelInboxState(ctx, sqlc.UpdateChannelInboxStateParams{
			ID:    row.ID,
			State: StateReady,
		}); err != nil {
			return sqlc.ChannelInbox{}, false, err
		}
		row.State = StateReady
	}
	return row, true, nil
}

// ListPending scans due pending events of one channel inside the caller's
// transaction — per-chat receive order, FOR UPDATE SKIP LOCKED. The router
// holds the channel row lock so only one replica routes a channel at a time.
func (s *Store) ListPending(ctx context.Context, tx pgx.Tx, channelID string) ([]sqlc.ChannelInbox, error) {
	return sqlc.New(tx).ListPendingChannelInbox(ctx, channelID)
}

// MarkRouted records that the event produced its durable successor (a run)
// inside the same transaction that created it.
func (s *Store) MarkRouted(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	n, err := sqlc.New(tx).MarkChannelInboxRouted(ctx, id)
	return n > 0, err
}

// Transition moves a pending event to rejected/failed, or defers it to a
// later attempt. Deferral keeps state 'received'/'ready' and only sets
// next_attempt_at.
func (s *Store) Transition(ctx context.Context, tx pgx.Tx, id, state, errorCode string, nextAttemptAt *time.Time) (bool, error) {
	var next pgtype.Timestamptz
	if nextAttemptAt != nil {
		next = pgtype.Timestamptz{Time: nextAttemptAt.UTC(), Valid: true}
	}
	n, err := sqlc.New(tx).UpdateChannelInboxState(ctx, sqlc.UpdateChannelInboxStateParams{
		ID:            id,
		State:         state,
		ErrorCode:     textOrNull(errorCode),
		NextAttemptAt: next,
	})
	return n > 0, err
}

func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
