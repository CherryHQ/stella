// Package run owns agent_run, the durable record of accepted agent work.
// A run exists once its input is committed; workers claim it under the
// existing session execution lease (internal/sessionexecution) rather than a
// second lock authority. State moves one way toward a terminal state; a stale
// worker fenced by worker_id cannot overwrite a newer state.
//
// Lock order, matching the sessionexecution convention: channel row (when a
// transaction needs channel coordination) -> session execution -> session and
// business rows -> agent_run.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/db/txlock"
)

// State is the agent_run lifecycle. Valid values are enforced here at the
// write boundary; the column stays plain TEXT so the set can grow.
type State string

const (
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
	StateCanceled    State = "canceled"
	StateInterrupted State = "interrupted"
)

func (s State) Valid() bool {
	switch s {
	case StateQueued, StateRunning, StateCompleted, StateFailed, StateCanceled, StateInterrupted:
		return true
	}
	return false
}

// Terminal reports whether the state can never transition again. A terminal
// run is never re-queued; a bounded recovery retry is a new run linked by
// retry_of_run_id.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCanceled, StateInterrupted:
		return true
	}
	return false
}

// Stable machine-readable error codes for agent_run.error_code.
const (
	// ErrCodeWorkerLost marks a run whose session execution lease expired
	// while running: the reaper cannot prove what the worker did, so the run
	// is interrupted, never silently re-queued.
	ErrCodeWorkerLost = "worker_lost"
	// ErrCodeTurnFailed marks a run whose turn returned an error or whose
	// executor aborted without a more specific code.
	ErrCodeTurnFailed = "turn_failed"
	// ErrCodeTargetGone marks a run whose session/binding was archived,
	// deleted, or unauthorized between routing and execution.
	ErrCodeTargetGone = "target_gone"
)

// EnvelopeVersion stamps actor/input/reply_address value objects. Bump on any
// shape change; readers must tolerate older versions.
const EnvelopeVersion = 1

// Actor is a snapshot of who requested the run — the persisted identity facts
// a worker re-derives authority from, never an authority object, context, or
// credential.
type Actor struct {
	V                int    `json:"v"`
	Kind             string `json:"kind"` // user / guest / system
	UserID           string `json:"user_id,omitempty"`
	Role             string `json:"role,omitempty"`               // user kind only
	GuestID          string `json:"guest_id,omitempty"`           // guest kind only
	GroupID          string `json:"group_id,omitempty"`           // group authority only
	ChannelBindingID string `json:"channel_binding_id,omitempty"` // dedicated channel grant
	Platform         string `json:"platform,omitempty"`
	PlatformID       string `json:"platform_id,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
}

// Input is the normalized request payload. Content carries the normalized
// []ai.ContentBlock JSON; media arrive as durable references (staged asset
// paths), not transient platform URLs.
type Input struct {
	V       int             `json:"v"`
	Kind    string          `json:"kind"` // message / command / notification
	Text    string          `json:"text,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	// ExcludedTools carries a per-turn tool allowlist delta for web sends.
	ExcludedTools []string `json:"excluded_tools,omitempty"`
}

// ReplyAddress fixes where the final answer goes at enqueue time, so a later
// /new or rebinding cannot redirect it.
type ReplyAddress struct {
	V          int    `json:"v"`
	ChannelID  string `json:"channel_id"`
	AccountKey string `json:"account_key"`
	ChatKey    string `json:"chat_key"`
	ThreadKey  string `json:"thread_key,omitempty"`
	ReplyToKey string `json:"reply_to_key,omitempty"`
	Scope      string `json:"scope,omitempty"` // platform conversation kind (e.g. qq "group"/"c2c")
	Token      string `json:"token,omitempty"` // platform reply credential (e.g. weixin context_token)
}

// RequestKeyInbox namespaces run idempotency to one accepted inbox event: the
// same channel event mints at most one run no matter how often it is
// redelivered or retried.
func RequestKeyInbox(inboxID string) string { return "inbox:" + inboxID }

// RequestKeyRequest namespaces run idempotency to a caller-supplied request
// id: a retried web/API send with the same key attaches to the existing run
// instead of executing twice.
func RequestKeyRequest(key string) string { return "request:" + key }

var (
	// ErrTargetGone marks executor failures where the run's durable target
	// (session/binding/agent grant) no longer exists; mapped to
	// ErrCodeTargetGone at finish.
	ErrTargetGone = errors.New("agent run target gone")
	// ErrNotFound reports a missing run.
	ErrNotFound = errors.New("agent run not found")
	// ErrConflict reports a lost fencing race: the run moved past the state
	// the caller still believed it held.
	ErrConflict = errors.New("agent run state conflict")
)

// Store composes the short-transaction agent_run API. Methods that mutate take
// the caller's pgx.Tx so routing, claiming, and completion stay atomic with
// their sibling writes.
type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// EnqueueParams carries everything a run fixes at creation.
type EnqueueParams struct {
	InboxID      string // "" for non-channel input (Web)
	SessionID    string
	AgentID      string
	RequestKey   string
	Actor        Actor
	Input        Input
	ReplyAddress ReplyAddress
	RetryOfRunID string // "" unless a bounded recovery retry
}

// Enqueue inserts the run inside tx and returns (run, true). The caller must
// hold the ctx_conversation row lock for SessionID so enqueue_seq is
// allocated without interleaving. When the request key already exists the
// existing run is returned with created=false — attach, never duplicate.
func (s *Store) Enqueue(ctx context.Context, tx pgx.Tx, p EnqueueParams) (sqlc.AgentRun, bool, error) {
	if p.SessionID == "" || p.AgentID == "" || p.RequestKey == "" {
		return sqlc.AgentRun{}, false, fmt.Errorf("run: session, agent and request key are required")
	}
	// enqueue_seq is allocated under a transaction-scoped advisory lock on the
	// session id rather than a row lock: a rotation must be able to archive the
	// session row on another connection while an earlier run for it is being
	// enqueued inside the routing transaction.
	if err := txlock.AdvisoryXactLock(ctx, tx, "agent-run-enqueue:"+p.SessionID); err != nil {
		return sqlc.AgentRun{}, false, err
	}
	q := sqlc.New(tx)
	seq, err := q.NextSessionEnqueueSeq(ctx, p.SessionID)
	if err != nil {
		return sqlc.AgentRun{}, false, err
	}
	actor, err := json.Marshal(p.Actor)
	if err != nil {
		return sqlc.AgentRun{}, false, err
	}
	input, err := json.Marshal(p.Input)
	if err != nil {
		return sqlc.AgentRun{}, false, err
	}
	reply, err := json.Marshal(p.ReplyAddress)
	if err != nil {
		return sqlc.AgentRun{}, false, err
	}
	row, err := q.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		InboxID:      textOrNull(p.InboxID),
		SessionID:    p.SessionID,
		AgentID:      p.AgentID,
		RequestKey:   p.RequestKey,
		Actor:        actor,
		Input:        input,
		ReplyAddress: reply,
		EnqueueSeq:   seq,
		RetryOfRunID: textOrNull(p.RetryOfRunID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := q.GetAgentRunByRequestKey(ctx, p.RequestKey)
		if getErr != nil {
			return sqlc.AgentRun{}, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return sqlc.AgentRun{}, false, err
	}
	return row, true, nil
}

// Get loads a run by id.
func (s *Store) Get(ctx context.Context, id string) (sqlc.AgentRun, error) {
	row, err := sqlc.New(s.db).GetAgentRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.AgentRun{}, ErrNotFound
	}
	return row, err
}

// ByRequestKey loads a run by its idempotency key.
func (s *Store) ByRequestKey(ctx context.Context, key string) (sqlc.AgentRun, error) {
	row, err := sqlc.New(s.db).GetAgentRunByRequestKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.AgentRun{}, ErrNotFound
	}
	return row, err
}

// ListQueued scans claimable runs oldest-first inside the caller's
// transaction (FOR UPDATE SKIP LOCKED). Scanning is not claiming: the final
// claim transaction must still pass Start.
func (s *Store) ListQueued(ctx context.Context, tx pgx.Tx) ([]sqlc.AgentRun, error) {
	return sqlc.New(tx).ListQueuedAgentRuns(ctx)
}

// ListOpenBySession returns non-terminal runs of a session in queue order.
func (s *Store) ListOpenBySession(ctx context.Context, sessionID string) ([]sqlc.AgentRun, error) {
	return sqlc.New(s.db).ListOpenAgentRunsBySession(ctx, sessionID)
}

// Start claims a queued run for workerID inside tx. The caller must have just
// acquired the session execution lease for the run's session in the same
// transaction; false means another worker won or the run already left queued.
func (s *Store) Start(ctx context.Context, tx pgx.Tx, id, workerID string) (bool, error) {
	n, err := sqlc.New(tx).StartAgentRun(ctx, sqlc.StartAgentRunParams{
		ID:       id,
		WorkerID: textOrNull(workerID),
	})
	return n > 0, err
}

// Finish lands a terminal state, fenced by workerID and the running state.
// Caller's transaction also writes the outbox operations and releases the
// session execution lease so result, run state, and ledger commit together.
func (s *Store) Finish(ctx context.Context, tx pgx.Tx, id, workerID string, state State, errorCode string) (bool, error) {
	if !state.Terminal() {
		return false, fmt.Errorf("run: finish requires a terminal state, got %q", state)
	}
	n, err := sqlc.New(tx).FinishAgentRun(ctx, sqlc.FinishAgentRunParams{
		ID:        id,
		WorkerID:  textOrNull(workerID),
		State:     string(state),
		ErrorCode: textOrNull(errorCode),
	})
	return n > 0, err
}

// CancelQueued drops a run nobody started. A running run is not cancelable
// here — cancellation of running work goes through the session execution
// lease's cancel_requested flag.
func (s *Store) CancelQueued(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	n, err := sqlc.New(tx).CancelQueuedAgentRun(ctx, id)
	return n > 0, err
}

// EnqueueDirect wraps Enqueue in its own transaction for callers that have no
// outer unit of work to join (web sends; channel routing holds a route tx).
func (s *Store) EnqueueDirect(ctx context.Context, p EnqueueParams) (sqlc.AgentRun, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return sqlc.AgentRun{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	run, created, err := s.Enqueue(ctx, tx, p)
	if err != nil {
		return sqlc.AgentRun{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.AgentRun{}, false, err
	}
	return run, created, nil
}

// CancelSessionTurn cancels a session's queued runs and flags its live
// execution lease in one transaction — the cross-replica /abort equivalent.
// A running worker observes cancel_requested at its next lease check.
func (s *Store) CancelSessionTurn(ctx context.Context, sessionID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	if _, err := q.CancelQueuedAgentRunsBySession(ctx, sessionID); err != nil {
		return err
	}
	if row, err := q.GetSessionExecution(ctx, sessionID); err == nil {
		if _, err := q.CancelSessionExecution(ctx, sqlc.CancelSessionExecutionParams{SessionID: sessionID, Token: row.Token}); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return tx.Commit(ctx)
}

// ReapExpired marks running runs whose linked session execution lease died as
// interrupted (ErrCodeWorkerLost) and terminates that lease in the same
// transaction — activity finish plus execution-row delete, exactly as
// Lease.Finish would. It never re-queues: an unknown in-flight turn is safer
// reported interrupted than executed twice. Returns the number of runs
// interrupted.
func (s *Store) ReapExpired(ctx context.Context) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	rows, err := q.ListExpiredRunningAgentRuns(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if _, err := q.MarkAgentRunInterrupted(ctx, r.ID); err != nil {
			return 0, err
		}
		if err := q.FinishSessionExecutionActivity(ctx, sqlc.FinishSessionExecutionActivityParams{
			SessionID: r.SessionID,
			Result:    pgtype.Text{String: "interrupted", Valid: true},
		}); err != nil {
			return 0, err
		}
		if _, err := q.DeleteSessionExecution(ctx, sqlc.DeleteSessionExecutionParams{
			SessionID: r.SessionID,
			Token:     r.LeaseToken,
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// NewID mints a time-ordered run id so the caller knows it before insert.
func NewID() string { return uuid.Must(uuid.NewV7()).String() }
