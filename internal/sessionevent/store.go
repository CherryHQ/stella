// Package sessionevent owns ctx_session_event, the durable per-session event
// log. Live turns append every streamed event with a session-scoped sequence
// so a watcher on any replica replays or tails a turn the local SessionHub
// never saw. The hub stays the fast path; this log is the cross-process truth
// and the reconnect cursor.
package sessionevent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Event is the stored row with its assigned sequence.
type Event struct {
	ID        string
	SessionID string
	RunID     string
	Seq       int64
	Payload   json.RawMessage
}

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Append writes one event under the session advisory lock so seq stays
// contiguous even when producers live on different replicas. runID may be
// empty (non-run turns keep their row link nullable).
func (s *Store) Append(ctx context.Context, sessionID, runID string, payload json.RawMessage) error {
	return s.AppendBatch(ctx, sessionID, runID, []json.RawMessage{payload})
}

// AppendBatch writes a batch in one transaction, keeping the advisory lock
// hold short.
func (s *Store) AppendBatch(ctx context.Context, sessionID, runID string, payloads []json.RawMessage) error {
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
	seq, err := q.NextSessionEventSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, payload := range payloads {
		if err := q.InsertSessionEvent(ctx, sqlc.InsertSessionEventParams{
			SessionID: sessionID,
			RunID:     pgtype.Text{String: runID, Valid: runID != ""},
			Seq:       int64(seq),
			Event:     payload,
		}); err != nil {
			return err
		}
		seq++
	}
	return tx.Commit(ctx)
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
		out = append(out, Event{
			ID:        r.ID,
			SessionID: r.SessionID,
			RunID:     r.RunID.String,
			Seq:       r.Seq,
			Payload:   r.Event,
		})
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
		out = append(out, Event{
			ID:        r.ID,
			SessionID: r.SessionID,
			RunID:     r.RunID.String,
			Seq:       r.Seq,
			Payload:   r.Event,
		})
	}
	return out, nil
}

// RunDone reports whether the run reached a terminal state — the watcher
// drains the log and exits, independent of what other runs may open next.
func (s *Store) RunDone(ctx context.Context, runID string) (bool, error) {
	r, err := sqlc.New(s.db).GetAgentRun(ctx, runID)
	if err != nil {
		return false, err
	}
	switch r.State {
	case "queued", "running":
		return false, nil
	default:
		return true, nil
	}
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
	// Only the running run is a live turn — queued runs must not shadow its
	// events, and a queued-only session has nothing live yet (204 is honest;
	// the sender's own request stream covers that run when it starts).
	for _, r := range runs {
		if r.State == "running" {
			return r.ID, nil
		}
	}
	return "", nil
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
