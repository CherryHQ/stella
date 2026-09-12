// Package sessionevent owns ctx_session_event, the durable per-session event
// log. Live turns append every streamed event with a session-scoped sequence
// so a watcher on any replica replays or tails a turn the local SessionHub
// never saw. The hub only wakes the reader; this log is the cross-process
// truth and the reconnect cursor.
package sessionevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Event is the stored row with its assigned sequence.
type Event struct {
	ID          string
	SessionID   string
	RunID       string
	ExecutionID string
	Seq         int64
	Payload     json.RawMessage
}

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ErrExecutionTerminated rejects an append after the token's turn_terminal
// marker exists: a fenced-out writer (reaped, superseded, or self-released)
// may resume before it notices, and events written after the marker would be
// silently invisible to observers who already saw the turn end.
var ErrExecutionTerminated = errors.New("sessionevent: execution already terminal")

// Append writes one event under the session advisory lock so seq stays
// contiguous even when producers live on different replicas. executionID is
// the claiming lease's token — the coordinate non-run turns are tailed by;
// runID may be empty (non-run turns keep their row link nullable).
func (s *Store) Append(ctx context.Context, sessionID, executionID, runID string, payload json.RawMessage) error {
	return s.AppendBatch(ctx, sessionID, executionID, runID, []json.RawMessage{payload})
}

// AppendBatch writes a batch in one transaction, keeping the advisory lock
// hold short.
func (s *Store) AppendBatch(ctx context.Context, sessionID, executionID, runID string, payloads []json.RawMessage) error {
	if sessionID == "" || len(payloads) == 0 {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	if err := q.LockConversationForWrite(ctx, sessionID); err != nil {
		return fmt.Errorf("sessionevent: lock session: %w", err)
	}
	if executionID != "" {
		// The marker check rides the same advisory the finish/reap writers
		// hold while inserting it — a committed terminal makes every later
		// append under this token fail, closing the paused-writer gap where a
		// revived producer keeps writing into a turn observers already ended.
		if _, err := q.GetSessionTerminalEvent(ctx, sqlc.GetSessionTerminalEventParams{
			SessionID:   sessionID,
			ExecutionID: pgtype.Text{String: executionID, Valid: true},
		}); err == nil {
			return ErrExecutionTerminated
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	for _, payload := range payloads {
		// Each call increments the session-row counter, so seqs survive a
		// prune of the log itself.
		seq, err := q.NextSessionEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}
		if err := q.InsertSessionEvent(ctx, sqlc.InsertSessionEventParams{
			SessionID:   sessionID,
			RunID:       pgtype.Text{String: runID, Valid: runID != ""},
			ExecutionID: pgtype.Text{String: executionID, Valid: executionID != ""},
			Seq:         seq,
			Event:       payload,
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// TurnStart commits the execution's turn_start control event — the one
// durable anchor carrying the canonical history boundary (history_before_seq)
// a cold reload rebuilds from. It must land after the turn's input is
// canonically appended and before any observation event; the same advisory +
// terminal check that fence AppendBatch fence it too. Idempotent per token.
func (s *Store) TurnStart(ctx context.Context, sessionID, executionID, runID string) error {
	if sessionID == "" || executionID == "" {
		return errors.New("sessionevent: turn_start needs session and execution ids")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	// The execution fence is real, not implied: lock this exact session+token
	// and reject it when the lease is expired or superseded — a stale writer
	// must not mint a new turn boundary. Lock order stays execution row →
	// conversation advisory → conversation row write.
	if _, err := q.LockSessionExecutionForFinish(ctx, sqlc.LockSessionExecutionForFinishParams{
		SessionID: sessionID, Token: executionID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return ErrExecutionTerminated
	} else if err != nil {
		return err
	}
	if valid, err := q.SessionExecutionValid(ctx, sqlc.SessionExecutionValidParams{
		SessionID: sessionID, Token: executionID,
	}); err != nil {
		return err
	} else if !valid.Valid || !valid.Bool {
		return ErrExecutionTerminated
	}
	if err := q.LockConversationForWrite(ctx, sessionID); err != nil {
		return fmt.Errorf("sessionevent: lock session: %w", err)
	}
	execID := pgtype.Text{String: executionID, Valid: true}
	if _, err := q.GetSessionStartEvent(ctx, sqlc.GetSessionStartEventParams{
		SessionID: sessionID, ExecutionID: execID,
	}); err == nil {
		return tx.Commit(ctx) // already started
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := q.GetSessionTerminalEvent(ctx, sqlc.GetSessionTerminalEventParams{
		SessionID: sessionID, ExecutionID: execID,
	}); err == nil {
		return ErrExecutionTerminated
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// B is the conversation's canonical message watermark at this instant:
	// everything at or below it belongs to history; the turn's own replay
	// begins after it. ctx_message is keyed by conversation_id — resolve the
	// session's conversation row rather than passing the session id through.
	conv, err := q.GetConversationForSessionAccess(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("sessionevent: resolve conversation: %w", err)
	}
	boundary, err := q.GetMaxSeq(ctx, conv.ID)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"type": "turn_start", "history_before_seq": boundary})
	seq, err := q.NextSessionEventSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := q.InsertSessionEvent(ctx, sqlc.InsertSessionEventParams{
		SessionID:   sessionID,
		RunID:       pgtype.Text{String: runID, Valid: runID != ""},
		ExecutionID: execID,
		Seq:         seq,
		Event:       payload,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TurnStartBoundary returns the committed history boundary for an execution,
// or ok=false while its start marker has not landed.
func (s *Store) TurnStartBoundary(ctx context.Context, sessionID, executionID string) (boundary int64, ok bool, err error) {
	row, err := sqlc.New(s.db).GetSessionStartEvent(ctx, sqlc.GetSessionStartEventParams{
		SessionID:   sessionID,
		ExecutionID: pgtype.Text{String: executionID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var payload struct {
		Boundary int64 `json:"history_before_seq"`
	}
	if err := json.Unmarshal(row.Event, &payload); err != nil {
		return 0, false, err
	}
	return payload.Boundary, true, nil
}

// TurnStartBoundaryForRun resolves the same start marker by run id — the
// coordinate a run tail holds.
func (s *Store) TurnStartBoundaryForRun(ctx context.Context, sessionID, runID string) (boundary int64, ok bool, err error) {
	row, err := sqlc.New(s.db).GetSessionStartEventForRun(ctx, sqlc.GetSessionStartEventForRunParams{
		SessionID: sessionID,
		RunID:     pgtype.Text{String: runID, Valid: runID != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var payload struct {
		Boundary int64 `json:"history_before_seq"`
	}
	if err := json.Unmarshal(row.Event, &payload); err != nil {
		return 0, false, err
	}
	return payload.Boundary, true, nil
}

// ReadSince returns at most limit events after the cursor, in seq order.
func (s *Store) ReadSince(ctx context.Context, sessionID string, afterSeq int64, limit int32) ([]Event, error) {
	rows, err := sqlc.New(s.db).ReadSessionEventsSince(ctx, sqlc.ReadSessionEventsSinceParams{
		SessionID: sessionID,
		Seq:       afterSeq,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowEvent(r))
	}
	return out, nil
}

// ReadForRun pages one run's events after the cursor — the durable replay a
// cross-replica watcher tails.
func (s *Store) ReadForRun(ctx context.Context, sessionID, runID string, afterSeq int64, limit int32) ([]Event, error) {
	rows, err := sqlc.New(s.db).ReadSessionEventsForRun(ctx, sqlc.ReadSessionEventsForRunParams{
		SessionID: sessionID,
		RunID:     pgtype.Text{String: runID, Valid: runID != ""},
		Seq:       afterSeq,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowEvent(r))
	}
	return out, nil
}

// RunDone reports whether the run reached a terminal state — the watcher
// drains the log and exits, independent of what other runs may open next.
func (s *Store) RunDone(ctx context.Context, sessionID, runID string) (bool, error) {
	r, err := sqlc.New(s.db).GetAgentRun(ctx, runID)
	if err != nil {
		return false, err
	}
	if r.SessionID != sessionID {
		// A run that does not belong to this session is not this session's
		// turn — the pin check must not leak another session's run state.
		return false, pgx.ErrNoRows
	}
	switch r.State {
	case "queued", "running":
		return false, nil
	default:
		return true, nil
	}
}

// RunState returns a run's terminal/queue state and error code — the durable
// watcher uses it to emit a terminal error instead of a clean finish when the
// run died without a persisted terminal event.
func (s *Store) RunState(ctx context.Context, sessionID, runID string) (state, errorCode string, err error) {
	r, err := sqlc.New(s.db).GetAgentRun(ctx, runID)
	if err != nil {
		return "", "", err
	}
	if r.SessionID != sessionID {
		return "", "", pgx.ErrNoRows
	}
	return r.State, r.ErrorCode.String, nil
}

// MinSeqForRun returns the earliest stored seq for the run (0 when none).
func (s *Store) MinSeqForRun(ctx context.Context, sessionID, runID string) (int64, error) {
	return sqlc.New(s.db).MinSessionEventSeqForRun(ctx, sqlc.MinSessionEventSeqForRunParams{
		SessionID: sessionID,
		RunID:     pgtype.Text{String: runID, Valid: runID != ""},
	})
}

// OpenRunID returns the newest open (queued/running) run on the session, or
// "" when none — a watcher uses it to decide whether there is a live turn to
// tail and, combined with ReadForRun draining to the cursor, when to stop.
func (s *Store) OpenRunID(ctx context.Context, sessionID string) (string, error) {
	runs, err := sqlc.New(s.db).ListOpenAgentRunsBySession(ctx, sessionID)
	if err != nil || len(runs) == 0 {
		return "", err
	}
	// The running run is the live turn — later queued runs must not shadow its
	// events. Queued-only still returns the earliest queued run: its events
	// attach to this same run id once it starts, and a 204 here would leave an
	// observer's one-shot history check racing a fast turn.
	for _, r := range runs {
		if r.State == "running" {
			return r.ID, nil
		}
	}
	return runs[0].ID, nil
}

// LatestSeq reports the highest stored sequence (0 when empty) — a watcher
// compares it with its cursor to decide whether to poll again.
func (s *Store) LatestSeq(ctx context.Context, sessionID string) (int64, error) {
	return sqlc.New(s.db).LatestSessionEventSeq(ctx, sessionID)
}

// Prune drops events past the retention window; run from the janitor loop.
func (s *Store) Prune(ctx context.Context) (int64, error) {
	return sqlc.New(s.db).PruneSessionEvents(ctx)
}

func rowEvent(r sqlc.CtxSessionEvent) Event {
	return Event{
		ID:          r.ID,
		SessionID:   r.SessionID,
		RunID:       r.RunID.String,
		ExecutionID: r.ExecutionID.String,
		Seq:         r.Seq,
		Payload:     r.Event,
	}
}

// ReadForExecution pages one execution's events after the cursor — the tail
// for turns that carry no agent_run (scheduler, delegate, session.send).
func (s *Store) ReadForExecution(ctx context.Context, sessionID, executionID string, afterSeq int64, limit int32) ([]Event, error) {
	rows, err := sqlc.New(s.db).ReadSessionEventsForExecution(ctx, sqlc.ReadSessionEventsForExecutionParams{
		SessionID:   sessionID,
		ExecutionID: pgtype.Text{String: executionID, Valid: executionID != ""},
		Seq:         afterSeq,
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowEvent(r))
	}
	return out, nil
}

// ExecutionTerminal returns the execution's explicit terminal marker — result
// and reason — or ok=false while the turn is still in flight. The lease row
// disappearing is never the signal; only this marker is.
func (s *Store) ExecutionTerminal(ctx context.Context, sessionID, executionID string) (result, reason string, ok bool, err error) {
	row, err := sqlc.New(s.db).GetSessionTerminalEvent(ctx, sqlc.GetSessionTerminalEventParams{
		SessionID:   sessionID,
		ExecutionID: pgtype.Text{String: executionID, Valid: executionID != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	var payload struct {
		Result string `json:"result"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(row.Event, &payload); err != nil {
		return "", "", false, err
	}
	return payload.Result, payload.Reason, true, nil
}

// OpenExecution returns the session's currently claimed execution token and
// its linked run id ("" for non-run turns), or empty strings when no lease is
// held. The row exists from claim to finish, so a watcher attaches between
// claim and the first persisted event instead of answering 204 into a fast
// turn. A run-linked execution must keep the run's observation identity —
// callers tail it by run, not by token, so the POST that started the turn and
// a sibling GET share one cursor space.
func (s *Store) OpenExecution(ctx context.Context, sessionID string) (token, runID string, err error) {
	row, err := sqlc.New(s.db).GetSessionExecution(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return row.Token, row.RunID.String, nil
}
