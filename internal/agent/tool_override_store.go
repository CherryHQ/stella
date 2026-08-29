package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ToolOverrideAbsentVersion is the only version accepted when creating an exact
// override row. It makes a first write conditional without inventing a row to
// version before the user has chosen an override.
const ToolOverrideAbsentVersion = "absent"

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
		UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID),
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

// ToolOverrideVersion is the safe exact-row projection used by management tools.
type ToolOverrideVersion struct {
	ToolName string `json:"tool_name"`
	Scope    string `json:"scope"`
	Enabled  bool   `json:"enabled"`
	Version  string `json:"version"`
	Present  bool   `json:"present"`
}

// Get returns one exact owner-bound override. Missing rows receive the stable
// absent sentinel so a caller can conditionally create rather than race an
// unguarded upsert.
func (s *ToolOverrideStore) Get(ctx context.Context, k ToolOverrideKey) (ToolOverrideVersion, error) {
	if !isOverrideScope(k.Scope) {
		return ToolOverrideVersion{}, fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	row, err := s.q.GetToolOverride(ctx, overrideParams(k))
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolOverrideVersion{ToolName: k.ToolName, Scope: k.Scope, Version: ToolOverrideAbsentVersion}, nil
	}
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	return overrideVersion(row), nil
}

// Set upserts a tool visibility override for existing HTTP callers, which keep
// their historical unconditional-write contract.
func (s *ToolOverrideStore) Set(ctx context.Context, w ToolOverrideWrite) error {
	if !isOverrideScope(w.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", w.Scope)
	}
	_, err := s.q.UpsertToolOverride(ctx, sqlc.UpsertToolOverrideParams{
		ToolName: w.ToolName, Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
	})
	return err
}

// SetIfVersion updates an existing row or creates an absent row only when its
// version still matches. A zero-row conditional write is a conflict, never a
// silent retry or an unconditional upsert.
func (s *ToolOverrideStore) SetIfVersion(ctx context.Context, w ToolOverrideWrite, expected string) (ToolOverrideVersion, error) {
	if !isOverrideScope(w.Scope) {
		return ToolOverrideVersion{}, fmt.Errorf("tool override: invalid scope %q", w.Scope)
	}
	if expected == ToolOverrideAbsentVersion {
		row, err := s.q.InsertToolOverrideIfAbsent(ctx, sqlc.InsertToolOverrideIfAbsentParams{
			ToolName: w.ToolName, Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), Enabled: w.Enabled,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ToolOverrideVersion{}, config.ErrAgentVersionConflict
		}
		if err != nil {
			return ToolOverrideVersion{}, err
		}
		return overrideVersion(row), nil
	}
	expectedAt, err := parseOverrideVersion(expected)
	if err != nil {
		return ToolOverrideVersion{}, config.ErrAgentVersionConflict
	}
	row, err := s.q.UpdateToolOverrideIfVersion(ctx, sqlc.UpdateToolOverrideIfVersionParams{
		Enabled: w.Enabled, ToolName: w.ToolName, Scope: w.Scope, UserID: pgnull.Text(w.UserID), AgentID: pgnull.Text(w.AgentID), ExpectedUpdatedAt: expectedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolOverrideVersion{}, config.ErrAgentVersionConflict
	}
	if err != nil {
		return ToolOverrideVersion{}, err
	}
	return overrideVersion(row), nil
}

// Clear deletes a tool visibility override if present for existing HTTP callers.
func (s *ToolOverrideStore) Clear(ctx context.Context, k ToolOverrideKey) error {
	if !isOverrideScope(k.Scope) {
		return fmt.Errorf("tool override: invalid scope %q", k.Scope)
	}
	return s.q.DeleteToolOverride(ctx, sqlc.DeleteToolOverrideParams{
		ToolName: k.ToolName, Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID),
	})
}

// ClearIfVersion deletes only an existing row at the version returned by Get.
func (s *ToolOverrideStore) ClearIfVersion(ctx context.Context, k ToolOverrideKey, expected string) error {
	if !isOverrideScope(k.Scope) || expected == ToolOverrideAbsentVersion {
		return config.ErrAgentVersionConflict
	}
	expectedAt, err := parseOverrideVersion(expected)
	if err != nil {
		return config.ErrAgentVersionConflict
	}
	_, err = s.q.DeleteToolOverrideIfVersion(ctx, sqlc.DeleteToolOverrideIfVersionParams{
		ToolName: k.ToolName, Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID), ExpectedUpdatedAt: expectedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return config.ErrAgentVersionConflict
	}
	return err
}

func overrideParams(k ToolOverrideKey) sqlc.GetToolOverrideParams {
	return sqlc.GetToolOverrideParams{ToolName: k.ToolName, Scope: k.Scope, UserID: pgnull.Text(k.UserID), AgentID: pgnull.Text(k.AgentID)}
}

func overrideVersion(row sqlc.ToolOverride) ToolOverrideVersion {
	return ToolOverrideVersion{ToolName: row.ToolName, Scope: row.Scope, Enabled: row.Enabled, Present: true, Version: row.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func parseOverrideVersion(version string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, version)
}

func isOverrideScope(scope string) bool {
	switch scope {
	case ToolOverrideScopeSystem, ToolOverrideScopeSystemAgent, ToolOverrideScopeUser, ToolOverrideScopeUserAgent:
		return true
	default:
		return false
	}
}
