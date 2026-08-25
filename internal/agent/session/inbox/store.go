// Package inbox persists Agent-originated Session inputs without owning their
// execution. Live turns remain caller-driven; pending rows are recovery input.
package inbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrorCode is a stable terminal reason. Raw provider/model errors stay in logs.
type ErrorCode string

const (
	ErrorCanceled          ErrorCode = "canceled"
	ErrorTimeout           ErrorCode = "timeout"
	ErrorQueueFull         ErrorCode = "queue_full"
	ErrorLiveFailed        ErrorCode = "live_failed"
	ErrorTargetUnavailable ErrorCode = "target_unavailable"
	ErrorRunInterrupted    ErrorCode = "run_interrupted"
)

// ErrOutcomeUnknown means PostgreSQL could not confirm whether a pending row
// was terminalized. Callers must not promise that delivery did not happen.
var ErrOutcomeUnknown = errors.New("session inbox delivery outcome unknown")

// Input contains only trusted immutable facts needed for transcript delivery.
type Input struct {
	SourceSessionID string
	TargetSessionID string
	Actor           eventlog.MessageActor
	Content         string
}

// Message identifies a persisted inbox input.
type Message struct {
	ID         string
	EnqueueSeq int64
}

// Store owns durable inbox state transitions, not Agent execution.
type Store struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db, q: sqlc.New(db)}
}

// Enqueue persists one runtime-authored Agent input before it enters the
// process-local turn queue.
func (s *Store) Enqueue(ctx context.Context, input Input) (Message, error) {
	if s == nil || s.q == nil {
		return Message{}, errors.New("session inbox store is not configured")
	}
	if input.SourceSessionID == "" || input.TargetSessionID == "" || input.SourceSessionID == input.TargetSessionID {
		return Message{}, errors.New("session inbox requires distinct source and target sessions")
	}
	if input.Actor.Type != eventlog.ActorAgent || input.Actor.ID == "" || input.Actor.SourceSessionID != input.SourceSessionID {
		return Message{}, errors.New("session inbox requires trusted source Agent provenance")
	}
	id := uuid.Must(uuid.NewV7()).String()
	row, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (sqlc.CtxSessionInbox, error) {
		return q.EnqueueSessionInbox(ctx, sqlc.EnqueueSessionInboxParams{
			ID:              id,
			SourceSessionID: input.SourceSessionID,
			TargetSessionID: input.TargetSessionID,
			ActorID:         input.Actor.ID,
			Content:         input.Content,
		})
	})
	if err != nil {
		// Return the client-generated ID so the caller can run a mandatory
		// pending→failed CAS. An autocommit acknowledgement can be lost after
		// PostgreSQL committed the INSERT.
		return Message{ID: id}, fmt.Errorf("enqueue session inbox: %w", err)
	}
	if !row.EnqueueSeq.Valid {
		return Message{}, errors.New("enqueue session inbox: database returned no sequence")
	}
	return Message{ID: row.ID, EnqueueSeq: row.EnqueueSeq.Int64}, nil
}

// FailPending atomically terminalizes a row only if transcript delivery has not
// won. applied=false means another terminal transition already committed.
func (s *Store) FailPending(ctx context.Context, id string, code ErrorCode) (applied bool, err error) {
	if s == nil || s.q == nil {
		return false, errors.New("session inbox store is not configured")
	}
	if id == "" || !code.valid() {
		return false, errors.New("session inbox failure requires a valid ID and error code")
	}
	rows, err := agentrun.WriteTxValue(ctx, s.db, func(q *sqlc.Queries) (int64, error) {
		return q.FailPendingSessionInbox(ctx, sqlc.FailPendingSessionInboxParams{ID: id, ErrorCode: string(code)})
	})
	if err != nil {
		return false, fmt.Errorf("fail pending session inbox: %w", err)
	}
	return rows == 1, nil
}

func (c ErrorCode) valid() bool {
	switch c {
	case ErrorCanceled, ErrorTimeout, ErrorQueueFull, ErrorLiveFailed, ErrorTargetUnavailable, ErrorRunInterrupted:
		return true
	default:
		return false
	}
}
