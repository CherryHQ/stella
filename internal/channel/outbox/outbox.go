// Package outbox owns channel_outbox, the durable ledger of outbound platform
// operations. One row is one decidable external call — a send, an edit, an
// upload — with its payload frozen before the first attempt. The channel
// owner executes rows; every send, result write, and requeue is fenced by
// attempt_token + the channel runtime token, so a fenced-out replica can
// never emit or record a duplicate.
package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Operation kinds. Each kind maps to one platform API call the adapter can
// classify (sent / definitely-not-executed / permanent failure / unknown).
const (
	OpSendText       = "send_text"
	OpSendReply      = "send_reply"
	OpNotify         = "notify"
	OpSendGroupReply = "send_group_reply"
	// OpDraftUpdate edits the run's live platform message in place: create on
	// first send, edit afterwards, superseded by the terminal send_reply.
	OpDraftUpdate    = "draft_update"
	OpEditText       = "edit_text"
	OpSendAttachment = "send_attachment"
	OpDelete         = "delete"
	OpReaction       = "reaction"
	OpTyping         = "typing"
)

// Operation states.
const (
	StatePending  = "pending"
	StateSending  = "sending"
	StateSent     = "sent"
	StateFailed   = "failed"
	StateUnknown  = "unknown"
	StateCanceled = "canceled"
)

// Stable error codes for error_code.
const (
	ErrCodeOwnerLost       = "owner_lost"
	ErrCodeAccountMismatch = "account_mismatch"
	ErrCodePermanent       = "permanent"
	ErrCodeUnknown         = "unknown"
)

// Store composes the short-transaction channel_outbox API.
type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Op is one outbound operation to append. Address carries the versioned
// platform coordinates (chat, thread, reply anchor); Payload the frozen send
// body. DependsOn lists operation_index values in the same delivery that must
// reach 'sent' first.
type Op struct {
	RunID string // "" for non-run operations (group publish, Notify)
	// GroupID anchors a group reply's lifecycle to the group row — the op
	// dies with the group, never with a channel config or dispatch row.
	GroupID     string
	DeliveryKey string
	Index       int
	Kind        string
	ChannelID   string
	AccountKey  string
	Address     json.RawMessage
	Payload     json.RawMessage
	DependsOn   []int
	NotBefore   *time.Time // initial scheduling delay, if any
	// Attachment is a file op's immutable bytes, staged in memory at prepare
	// and written to channel_outbox_attachment in the same transaction as the
	// op. Never serialized into the payload.
	Attachment []byte
}

// Append writes a delivery's operations inside the caller's transaction —
// the run-completion tx makes result, run terminal state, and this ledger
// commit or roll back together. Re-appending the same (delivery_key,
// operation_index) is a no-op, so a crashed completion retry is safe.
func (s *Store) Append(ctx context.Context, tx pgx.Tx, ops []Op) error {
	q := sqlc.New(tx)
	for _, op := range ops {
		if op.DeliveryKey == "" || op.Kind == "" || op.ChannelID == "" {
			return fmt.Errorf("outbox: delivery key, kind and channel are required")
		}
		// AccountKey may be empty for work not triggered through a receiving
		// account — e.g. a group reply whose trigger is another agent's event.
		// Empty means unfenced at dispatch: the channel's current account
		// delivers it, which is correct because no account ever owned it.
		deps, err := json.Marshal(op.DependsOn)
		if err != nil {
			return err
		}
		if string(deps) == "null" {
			deps = json.RawMessage(`[]`)
		}
		var notBefore pgtype.Timestamptz
		if op.NotBefore != nil {
			notBefore = pgtype.Timestamptz{Time: op.NotBefore.UTC(), Valid: true}
		}
		address, payload := op.Address, op.Payload
		if len(address) == 0 {
			address = json.RawMessage(`{}`)
		}
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		row, err := q.CreateChannelOutbox(ctx, sqlc.CreateChannelOutboxParams{
			RunID:            textOrNull(op.RunID),
			DeliveryKey:      op.DeliveryKey,
			OperationIndex:   int32(op.Index),
			OperationKind:    op.Kind,
			ChannelID:        op.ChannelID,
			SourceAccountKey: op.AccountKey,
			Address:          address,
			Payload:          payload,
			DependsOn:        deps,
			NextAttemptAt:    notBefore,
			GroupID:          textOrNull(op.GroupID),
		})
		switch {
		case err == nil:
			// Attachment presence is nil-vs-non-nil: an empty file is a real
			// zero-byte attachment, not a missing one.
			if op.Attachment != nil {
				if err := q.CreateChannelOutboxAttachment(ctx, sqlc.CreateChannelOutboxAttachmentParams{
					OutboxID: row.ID, Data: op.Attachment,
				}); err != nil {
					return fmt.Errorf("outbox: attachment body for %s/%d: %w", op.DeliveryKey, op.Index, err)
				}
			}
		case errors.Is(err, pgx.ErrNoRows):
			// Dedup hit: the stored op must match semantically, and so must its
			// attachment — a same-key different-body retry is corruption, not
			// a crash-safe re-append.
			if err := verifyExistingOp(ctx, q, op); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// verifyExistingOp confirms a dedup'd op matches what this append would have
// written: frozen identity columns compare with JSONB semantics inside the
// query, and the attachment bytes compare exactly — row and body committed
// together, so equality proves the original write is intact.
func verifyExistingOp(ctx context.Context, q *sqlc.Queries, op Op) error {
	deps, err := json.Marshal(op.DependsOn)
	if err != nil {
		return err
	}
	if string(deps) == "null" {
		deps = json.RawMessage(`[]`)
	}
	address, payload := op.Address, op.Payload
	if len(address) == 0 {
		address = json.RawMessage(`{}`)
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	match, err := q.ChannelOutboxOpMatches(ctx, sqlc.ChannelOutboxOpMatchesParams{
		DeliveryKey: op.DeliveryKey, OperationIndex: int32(op.Index),
		OperationKind: op.Kind, ChannelID: op.ChannelID, SourceAccountKey: op.AccountKey,
		RunID: textOrNull(op.RunID), GroupID: textOrNull(op.GroupID),
		Address: address, Payload: payload, DependsOn: deps,
	})
	if err != nil {
		return fmt.Errorf("outbox: dedup check %s/%d: %w", op.DeliveryKey, op.Index, err)
	}
	if !match {
		return fmt.Errorf("outbox: %s/%d already exists with different content", op.DeliveryKey, op.Index)
	}
	stored, err := q.GetChannelOutboxAttachmentByKey(ctx, sqlc.GetChannelOutboxAttachmentByKeyParams{
		DeliveryKey: op.DeliveryKey, OperationIndex: int32(op.Index),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if op.Attachment != nil {
			return fmt.Errorf("outbox: %s/%d stored without its attachment body", op.DeliveryKey, op.Index)
		}
		return nil
	case err != nil:
		return fmt.Errorf("outbox: attachment dedup check %s/%d: %w", op.DeliveryKey, op.Index, err)
	case op.Attachment == nil:
		return fmt.Errorf("outbox: %s/%d carries an attachment the op does not expect", op.DeliveryKey, op.Index)
	case !bytes.Equal(stored, op.Attachment):
		return fmt.Errorf("outbox: %s/%d attachment differs from stored bytes", op.DeliveryKey, op.Index)
	}
	return nil
}

// ListByDelivery loads a delivery's operations in index order — status reads
// and dependency checks.
func (s *Store) ListByDelivery(ctx context.Context, deliveryKey string) ([]sqlc.ChannelOutbox, error) {
	return sqlc.New(s.db).GetChannelOutboxByDelivery(ctx, deliveryKey)
}

// ListDue scans due pending operations for a channel inside the caller's
// transaction (FOR UPDATE SKIP LOCKED). Only the current channel owner calls
// this, after verifying its runtime token is still fresh.
func (s *Store) ListDue(ctx context.Context, tx pgx.Tx, channelID string) ([]sqlc.ChannelOutbox, error) {
	return sqlc.New(tx).ListPendingChannelOutbox(ctx, channelID)
}

// ClaimAttempt moves a due pending op to 'sending' under a fresh attempt
// token and the caller's channel runtime token. Returns ("", false, nil) when
// the op was already claimed or deferred.
func (s *Store) ClaimAttempt(ctx context.Context, tx pgx.Tx, id, ownerToken string) (string, bool, error) {
	attempt := uuid.Must(uuid.NewV7()).String()
	n, err := sqlc.New(tx).ClaimChannelOutboxAttempt(ctx, sqlc.ClaimChannelOutboxAttemptParams{
		ID:           id,
		AttemptToken: pgtype.Text{String: attempt, Valid: true},
		OwnerToken:   textOrNull(ownerToken),
	})
	if err != nil {
		return "", false, err
	}
	if n == 0 {
		return "", false, nil
	}
	return attempt, true, nil
}

// Outcome classifies a finished attempt.
type Outcome struct {
	State             string // sent / failed / unknown
	PlatformMessageID string
	ErrorCode         string
	NextAttemptAt     *time.Time // retry schedule for failed/unknown
}

// CompleteAttempt records an attempt's outcome, fenced by the attempt token:
// a superseded attempt cannot overwrite a newer state. Returns false when the
// fence rejected the write.
func (s *Store) CompleteAttempt(ctx context.Context, tx pgx.Tx, id, attemptToken string, o Outcome) (bool, error) {
	switch o.State {
	// StatePending here means "attempt failed retryably, re-scheduled": the
	// row returns to pending with NextAttemptAt instead of terminating.
	// StateCanceled is the superseded-outcome for draft ops: claimed but no
	// longer deliverable (run terminal or a newer snapshot already sent).
	case StateSent, StateFailed, StateUnknown, StatePending, StateCanceled:
	default:
		return false, fmt.Errorf("outbox: outcome must be sent/failed/unknown/pending, got %q", o.State)
	}
	if o.State == StatePending && o.NextAttemptAt == nil {
		return false, fmt.Errorf("outbox: retry outcome requires next_attempt_at")
	}
	var next pgtype.Timestamptz
	if o.NextAttemptAt != nil {
		next = pgtype.Timestamptz{Time: o.NextAttemptAt.UTC(), Valid: true}
	}
	n, err := sqlc.New(tx).CompleteChannelOutboxAttempt(ctx, sqlc.CompleteChannelOutboxAttemptParams{
		ID:                id,
		AttemptToken:      textOrNull(attemptToken),
		State:             o.State,
		PlatformMessageID: textOrNull(o.PlatformMessageID),
		ErrorCode:         textOrNull(o.ErrorCode),
		NextAttemptAt:     next,
	})
	return n > 0, err
}

// ExpireAttempts moves 'sending' attempts past their deadline to 'unknown'
// in its own transaction: the outcome can no longer be trusted, and unknown
// is the state that stops blind resend until a platform probe decides.
// Returns the number of attempts expired.
func (s *Store) ExpireAttempts(ctx context.Context) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	rows, err := q.ListExpiredChannelOutboxAttempts(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if _, err := q.MarkChannelOutboxAttemptUnknown(ctx, r.ID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// Requeue moves an 'unknown' op back to 'pending' after a probe decided the
// send never landed. Only explicit operator flow requeues 'failed'.
func (s *Store) Requeue(ctx context.Context, tx pgx.Tx, id string, nextAttemptAt *time.Time) (bool, error) {
	var next pgtype.Timestamptz
	if nextAttemptAt != nil {
		next = pgtype.Timestamptz{Time: nextAttemptAt.UTC(), Valid: true}
	}
	n, err := sqlc.New(tx).RequeueChannelOutboxAttempt(ctx, sqlc.RequeueChannelOutboxAttemptParams{
		ID:            id,
		NextAttemptAt: next,
	})
	return n > 0, err
}

// UpsertDraft merges a newer progress snapshot into the run's live delivery:
// a still-pending latest op is rewritten in place (unsent intermediate
// versions are never sent), a sent/sending predecessor gets an ordered
// append, and a terminally finished predecessor starts the next index free
// of ordering deps. Returns true when a send was scheduled or merged.
func (s *Store) UpsertDraft(ctx context.Context, tx pgx.Tx, op Op, seq int64) (bool, error) {
	q := sqlc.New(tx)
	latest, err := q.GetLatestChannelOutboxOp(ctx, op.DeliveryKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return true, s.Append(ctx, tx, []Op{op})
	case err != nil:
		return false, err
	}
	if latest.State == StatePending {
		// Same-position merge: keep the op identity, replace the snapshot.
		// Skip the write when the stored snapshot already covers this seq.
		var stored struct {
			Seq int64 `json:"seq"`
		}
		if err := json.Unmarshal(latest.Payload, &stored); err == nil && stored.Seq >= seq {
			return false, nil
		}
		op.Index = int(latest.OperationIndex)
		n, err := q.UpdatePendingChannelOutboxPayload(ctx, sqlc.UpdatePendingChannelOutboxPayloadParams{
			ID:      latest.ID,
			Payload: op.Payload,
		})
		return n > 0, err
	}
	op.Index = int(latest.OperationIndex) + 1
	if latest.State == StateSending {
		// Keep edit order: this snapshot may only send after the in-flight
		// one resolves. A failed predecessor parks the draft — the terminal
		// reply still lands on its own delivery.
		op.DependsOn = []int{int(latest.OperationIndex)}
	}
	return true, s.Append(ctx, tx, []Op{op})
}

// CancelByRun drops every still-deliverable op of a run (session teardown,
// config removal): pending and unknown both become canceled; sent history is
// untouched.
func (s *Store) CancelByRun(ctx context.Context, tx pgx.Tx, runID string) (int64, error) {
	return sqlc.New(tx).CancelChannelOutboxByRun(ctx, textOrNull(runID))
}

func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
