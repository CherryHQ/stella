package sessionctl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type sqlNonceStore struct {
	q   *sqlc.Queries
	log *slog.Logger
}

// NewSQLNonceStore builds the durable nonce store on an existing query set.
func NewSQLNonceStore(q *sqlc.Queries) NonceStore {
	return sqlNonceStore{q: q, log: slog.With("component", "sessionctl")}
}

// NewSQLNonceStoreForPool builds the durable nonce store on a connection pool,
// owning construction of its sqlc query set.
func NewSQLNonceStoreForPool(pool *pgxpool.Pool) NonceStore {
	return NewSQLNonceStore(sqlc.New(pool))
}

func (s sqlNonceStore) Create(ctx context.Context, n Nonce) error {
	if s.q == nil {
		return fmt.Errorf("sessionctl: sql queries are required")
	}
	if n.ID == "" {
		n.ID = uuid.Must(uuid.NewV7()).String()
	}
	// Every nonce leaves the table one of two ways: Claim deletes it, or it
	// expires and this sweep collects it. Sweeping globally rather than per
	// binding is what actually bounds the table — a binding that asks once and
	// never returns would otherwise keep its expired row forever — and
	// idx_agent_session_rotation_nonce_expires_at serves the predicate.
	if err := s.q.DeleteExpiredSessionRotationNonce(ctx); err != nil {
		s.log.WarnContext(ctx, "failed to prune spent session rotation nonces", "error", err)
	}
	if _, err := s.q.CreateSessionRotationNonce(ctx, sqlc.CreateSessionRotationNonceParams{
		ID:         n.ID,
		SessionID:  n.SessionID,
		BindingKey: n.BindingKey,
		ActorID:    n.ActorID,
		TurnMarker: n.TurnMarker,
		ExpiresAt:  n.ExpiresAt.UTC(),
	}); err != nil {
		return fmt.Errorf("sessionctl: create rotation nonce: %w", err)
	}
	return nil
}

func (s sqlNonceStore) Get(ctx context.Context, id string) (Nonce, error) {
	if s.q == nil {
		return Nonce{}, fmt.Errorf("sessionctl: sql queries are required")
	}
	if !validNonceID(id) {
		return Nonce{}, ErrNonceNotFound
	}
	row, err := s.q.GetSessionRotationNonce(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Nonce{}, ErrNonceNotFound
		}
		return Nonce{}, fmt.Errorf("sessionctl: get rotation nonce: %w", err)
	}
	return nonceFromRow(row), nil
}

// Claim spends a nonce. The claim query deletes the row and returns it, so the
// single-use gate and the cleanup are the same statement: whoever gets the row
// back is the only caller that may rotate, and no spent row is left behind.
func (s sqlNonceStore) Claim(ctx context.Context, id string) (Nonce, error) {
	if s.q == nil {
		return Nonce{}, fmt.Errorf("sessionctl: sql queries are required")
	}
	if !validNonceID(id) {
		return Nonce{}, ErrNonceNotFound
	}
	row, err := s.q.ClaimSessionRotationNonce(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Nonce{}, ErrNonceNotFound
		}
		return Nonce{}, fmt.Errorf("sessionctl: claim rotation nonce: %w", err)
	}
	return nonceFromRow(row), nil
}

// validNonceID keeps a model-supplied string away from a uuid-typed column,
// where a malformed value would surface as a database error instead of the
// ordinary "no such pending rotation" answer.
func validNonceID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

func nonceFromRow(row sqlc.AgentSessionRotationNonce) Nonce {
	n := Nonce{
		ID:         row.ID,
		SessionID:  row.SessionID,
		BindingKey: row.BindingKey,
		ActorID:    row.ActorID,
		TurnMarker: row.TurnMarker,
		ExpiresAt:  row.ExpiresAt.UTC(),
	}
	if row.UsedAt.Valid {
		n.UsedAt = row.UsedAt.Time.UTC()
	}
	return n
}
