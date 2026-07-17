package agent

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ToolOverrideStore reads persisted tool-visibility overrides for an
// agent+user context. Its Fetch method satisfies ToolOverrideFetcher.
type ToolOverrideStore struct {
	q *sqlc.Queries
}

// NewToolOverrideStore builds a ToolOverrideStore over the given pool.
func NewToolOverrideStore(db *pgxpool.Pool) *ToolOverrideStore {
	return &ToolOverrideStore{q: sqlc.New(db)}
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
	_, err := s.q.UpsertToolOverride(ctx, sqlc.UpsertToolOverrideParams{
		ToolName: w.ToolName,
		Scope:    w.Scope,
		UserID:   pgnull.Text(w.UserID),
		AgentID:  pgnull.Text(w.AgentID),
		Enabled:  w.Enabled,
	})
	return err
}

// Clear deletes a tool visibility override if present. The scope is validated for
// the same reason as Set.
func (s *ToolOverrideStore) Clear(ctx context.Context, k ToolOverrideKey) error {
	if !isOverrideScope(k.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	return s.q.DeleteToolOverride(ctx, sqlc.DeleteToolOverrideParams{
		ToolName: k.ToolName,
		Scope:    k.Scope,
		UserID:   pgnull.Text(k.UserID),
		AgentID:  pgnull.Text(k.AgentID),
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
