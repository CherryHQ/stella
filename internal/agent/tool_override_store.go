package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ToolOverrideStore reads persisted tool-visibility overrides for an
// agent+user context. Its Fetch method satisfies ToolOverrideFetcher.
type ToolOverrideStore struct {
	q    *sqlc.Queries
	pool *pgxpool.Pool
}

// NewToolOverrideStore builds a ToolOverrideStore over the given pool.
func NewToolOverrideStore(db *pgxpool.Pool) *ToolOverrideStore {
	return &ToolOverrideStore{q: sqlc.New(db), pool: db}
}

// ToolOverrideDigest is the stable digest used by the settings preview/confirm
// protocol. It intentionally covers only the owner-bound row state that the
// mutation changes, not database timestamps or surrogate IDs.
func ToolOverrideDigest(found, enabled bool) string {
	data, _ := json.Marshal(map[string]any{"found": found, "enabled": enabled})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Fetch returns the tool overrides that apply to the given user+agent pair.
func (s *ToolOverrideStore) Fetch(ctx context.Context, userID, agentID string) ([]ToolOverride, error) {
	rows, err := s.q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ToolOverride, 0, len(rows))
	for _, row := range rows {
		out = append(out, ToolOverride{ToolName: row.ToolName, Scope: row.Scope, Enabled: row.Enabled})
	}
	return out, nil
}

// Get returns one exact owner-bound override. Missing is not an error, because
// clearing an absent override is an idempotent mutation.
func (s *ToolOverrideStore) Get(ctx context.Context, k ToolOverrideKey) (ToolOverride, bool, error) {
	if !isOverrideScope(k.Scope) {
		return ToolOverride{}, false, fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	row, err := s.q.GetToolOverride(ctx, sqlc.GetToolOverrideParams{
		ToolName: k.ToolName, Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolOverride{}, false, nil
	}
	if err != nil {
		return ToolOverride{}, false, err
	}
	return ToolOverride{ToolName: row.ToolName, Scope: row.Scope, Enabled: row.Enabled}, true, nil
}

// ToolOverrideWrite is the durable owner+scope key plus the desired enabled state
// for one tool visibility override.
type ToolOverrideWrite struct {
	ToolName string
	Scope    string
	UserID   string
	AgentID  string
	Enabled  bool
}

// ToolOverrideKey identifies one override row for clearing.
type ToolOverrideKey struct {
	ToolName string
	Scope    string
	UserID   string
	AgentID  string
}

// Set upserts a tool visibility override. It validates the scope vocabulary so a
// caller can never persist an override under an unrecognized scope — the Agent
// domain owns that invariant rather than trusting the transport's request field.
func (s *ToolOverrideStore) Set(ctx context.Context, w ToolOverrideWrite) error {
	if !isOverrideScope(w.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", w.Scope)
	}
	return s.withKeyLock(ctx, ToolOverrideKey{ToolName: w.ToolName, Scope: w.Scope, UserID: w.UserID, AgentID: w.AgentID}, func(q *sqlc.Queries) error {
		_, err := q.UpsertToolOverride(ctx, sqlc.UpsertToolOverrideParams{
			ToolName: w.ToolName, Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
		})
		return err
	})
}

// SetIfDigest compares the current owner-bound row and upserts it in one
// transaction. The row lock prevents a concurrent writer from changing the
// value after the comparison and before the write.
func (s *ToolOverrideStore) SetIfDigest(ctx context.Context, w ToolOverrideWrite, expected string) error {
	if !isOverrideScope(w.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", w.Scope)
	}
	return s.mutateIfDigest(ctx, ToolOverrideKey{ToolName: w.ToolName, Scope: w.Scope, UserID: w.UserID, AgentID: w.AgentID}, expected, func(q *sqlc.Queries) error {
		_, err := q.UpsertToolOverride(ctx, sqlc.UpsertToolOverrideParams{
			ToolName: w.ToolName, Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
		})
		return err
	})
}

// ClearIfDigest compares and deletes the current owner-bound row under one row
// lock. An absent row is a valid compare target, so clear remains idempotent.
func (s *ToolOverrideStore) ClearIfDigest(ctx context.Context, k ToolOverrideKey, expected string) error {
	if !isOverrideScope(k.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	return s.mutateIfDigest(ctx, k, expected, func(q *sqlc.Queries) error {
		return q.DeleteToolOverride(ctx, sqlc.DeleteToolOverrideParams{
			ToolName: k.ToolName, Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID),
		})
	})
}

func (s *ToolOverrideStore) mutateIfDigest(ctx context.Context, k ToolOverrideKey, expected string, mutate func(*sqlc.Queries) error) error {
	return s.withKeyLock(ctx, k, func(q *sqlc.Queries) error {
		row, err := q.GetToolOverrideForUpdate(ctx, sqlc.GetToolOverrideForUpdateParams{
			ToolName: k.ToolName, Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID),
		})
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if expected != ToolOverrideDigest(found, found && row.Enabled) {
			return errors.New("tool override changed since preview")
		}
		return mutate(q)
	})
}

func (s *ToolOverrideStore) withKeyLock(ctx context.Context, k ToolOverrideKey, mutate func(*sqlc.Queries) error) error {
	if s == nil || s.pool == nil {
		return errors.New("tool override: conditional writer unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tool override mutation: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // successful commit makes rollback inert
	q := s.q.WithTx(tx)
	if err := q.LockToolOverrideKey(ctx, toolOverrideLockKey(k)); err != nil {
		return fmt.Errorf("lock tool override key: %w", err)
	}
	if err := mutate(q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func toolOverrideLockKey(k ToolOverrideKey) string {
	data, _ := json.Marshal(k)
	return string(data)
}

// Clear deletes a tool visibility override if present. The scope is validated for
// the same reason as Set.
func (s *ToolOverrideStore) Clear(ctx context.Context, k ToolOverrideKey) error {
	if !isOverrideScope(k.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	return s.withKeyLock(ctx, k, func(q *sqlc.Queries) error {
		return q.DeleteToolOverride(ctx, sqlc.DeleteToolOverrideParams{
			ToolName: k.ToolName, Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID),
		})
	})
}

func isOverrideScope(scope string) bool {
	switch scope {
	case ToolOverrideScopeSystem, ToolOverrideScopeSystemAgent, ToolOverrideScopeUser, ToolOverrideScopeUserAgent:
		return true
	default:
		return false
	}
}
