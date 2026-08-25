// Package runtimeops backs the `stellad runtime` operator commands: the
// attributed human transitions out of the two distributed fencing states that
// deliberately never resolve on their own (a poison FIFO head that exhausted
// automatic retry, and a fenced SessionSandbox generation whose cleanup cannot
// prove resource absence). It owns the queries and the operator database
// resolution so the composition root only wires and renders.
package runtimeops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// FifoItem is one channel input item still occupying admission budget.
// RetryExhausted marks a blocked head that waits for an operator rejection.
type FifoItem struct {
	ID              string     `json:"id"`
	ChannelID       string     `json:"channel_id"`
	BindingKey      string     `json:"binding_key"`
	PrincipalID     string     `json:"principal_id"`
	SourceKey       string     `json:"source_key"`
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	AttemptCount    int64      `json:"attempt_count"`
	PayloadBytes    int64      `json:"payload_bytes"`
	AttachmentBytes int64      `json:"attachment_bytes"`
	BlockedReason   string     `json:"blocked_reason"`
	RetryExhausted  bool       `json:"retry_exhausted"`
	CreatedAt       time.Time  `json:"created_at"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
}

// SandboxGeneration is one SessionSandbox generation that still holds (or may
// hold) a resource.
type SandboxGeneration struct {
	SessionID       string     `json:"session_id"`
	Generation      int64      `json:"generation"`
	State           string     `json:"state"`
	ResourceBackend string     `json:"resource_backend"`
	ResourceID      string     `json:"resource_id"`
	FencedAt        *time.Time `json:"fenced_at,omitempty"`
}

// Store answers operator queries over one deployment database.
type Store struct {
	q *sqlc.Queries
}

// NewStore wraps an existing pool; Open resolves the pool for CLI use.
func NewStore(db *pgxpool.Pool) *Store { return &Store{q: sqlc.New(db)} }

// Open connects to the deployment database the way the server resolves it:
// STELLA_DATABASE_URL when set, otherwise the embedded cluster under the
// stella home. Deliberately not config.LoadServerConfig: an operator command
// must not be blocked by an unrelated bad server variable (#701). The embedded
// cluster is single-owner, so with a running embedded server this fails and
// says to stop it first; move to an admin HTTP API if these commands need to
// run against a live embedded server.
func Open(ctx context.Context, fn func(context.Context, *Store) error) error {
	dsn := os.Getenv("STELLA_DATABASE_URL")
	if dsn == "" {
		emb, err := appdb.StartEmbedded(filepath.Join(config.StellaHome(), "postgres"), 0)
		if err != nil {
			return fmt.Errorf("start embedded postgres: %w (a running stellad owns the embedded cluster; stop it first with `stellad service stop`, or set STELLA_DATABASE_URL)", err)
		}
		defer func() { _ = emb.Stop() }()
		dsn = emb.DSN()
	}
	db, err := appdb.OpenDB(dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	return fn(ctx, NewStore(db))
}

// ListFifo returns every channel input item in pending/running/blocked order,
// without payloads (a poison head can carry 32 MiB).
func (s *Store) ListFifo(ctx context.Context) ([]FifoItem, error) {
	rows, err := s.q.ListLiveChannelBindingFIFO(ctx)
	if err != nil {
		return nil, fmt.Errorf("list channel input: %w", err)
	}
	items := make([]FifoItem, 0, len(rows))
	for _, r := range rows {
		item := FifoItem{
			ID: r.ID, ChannelID: r.ChannelID, BindingKey: r.BindingKey,
			PrincipalID: r.PrincipalID, SourceKey: r.SourceKey, Kind: r.Kind,
			Status: r.Status, AttemptCount: r.AttemptCount,
			PayloadBytes: r.PayloadBytes, AttachmentBytes: r.AttachmentBytes,
			BlockedReason:  r.BlockedReason,
			RetryExhausted: r.Status == "blocked" && !r.NextAttemptAt.Valid,
			CreatedAt:      r.CreatedAt,
		}
		if r.NextAttemptAt.Valid {
			next := r.NextAttemptAt.Time
			item.NextAttemptAt = &next
		}
		items = append(items, item)
	}
	return items, nil
}

// RejectFifo is the terminal, attributed operator transition for a live item.
// It reports whether an item was transitioned; false means it does not exist
// or is already terminal.
func (s *Store) RejectFifo(ctx context.Context, id, reason, rejectedBy string) (bool, error) {
	rows, err := s.q.RejectChannelBindingFIFO(ctx, sqlc.RejectChannelBindingFIFOParams{
		ID: id, Reason: reason, RejectedBy: rejectedBy,
	})
	if err != nil {
		return false, fmt.Errorf("reject channel input %s: %w", id, err)
	}
	return rows == 1, nil
}

// ListSandbox returns every generation that still holds (or may hold) a
// resource.
func (s *Store) ListSandbox(ctx context.Context) ([]SandboxGeneration, error) {
	rows, err := s.q.ListLiveSessionSandbox(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sandbox generations: %w", err)
	}
	generations := make([]SandboxGeneration, 0, len(rows))
	for _, r := range rows {
		generations = append(generations, sandboxGeneration(r))
	}
	return generations, nil
}

// GetFencedSandbox returns the session's generation when it is fenced; any
// other state is an error naming the state, so the caller can explain why
// there is nothing to mark destroyed.
func (s *Store) GetFencedSandbox(ctx context.Context, sessionID string) (SandboxGeneration, error) {
	row, err := s.q.GetSessionSandbox(ctx, sessionID)
	if err != nil {
		return SandboxGeneration{}, fmt.Errorf("session %s has no sandbox record", sessionID)
	}
	if row.State != "fenced" {
		return SandboxGeneration{}, fmt.Errorf("generation %d of session %s is %q, not fenced", row.Generation, sessionID, row.State)
	}
	return sandboxGeneration(row), nil
}

// MarkSandboxDestroyed records the fenced generation as destroyed on the
// operator's authority. It reports whether the transition happened; false
// means the row changed state concurrently.
func (s *Store) MarkSandboxDestroyed(ctx context.Context, sessionID string, generation int64) (bool, error) {
	rows, err := s.q.DestroySessionSandboxGeneration(ctx, sqlc.DestroySessionSandboxGenerationParams{
		SessionID: sessionID, Generation: generation,
	})
	if err != nil {
		return false, fmt.Errorf("mark sandbox destroyed: %w", err)
	}
	return rows == 1, nil
}

func sandboxGeneration(r sqlc.AgentSessionSandbox) SandboxGeneration {
	g := SandboxGeneration{
		SessionID: r.SessionID, Generation: r.Generation, State: r.State,
		ResourceBackend: r.ResourceBackend, ResourceID: r.ResourceID,
	}
	if r.FencedAt.Valid {
		fenced := r.FencedAt.Time
		g.FencedAt = &fenced
	}
	return g
}
