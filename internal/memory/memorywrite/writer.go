// Package memorywrite provides shared transactional write helpers for durable
// memory surfaces. Mutating helpers keep ctx_agent_memory.version and changelog
// entries in the same transaction.
package memorywrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// lockMemory serializes concurrent writers for one (user, agent) memory row so
// the read-modify-write of version + changelog cannot interleave. Under SQLite
// this was implicit (single writer); under PostgreSQL it needs an explicit
// transaction-scoped advisory lock, released automatically on commit/rollback.
func lockMemory(ctx context.Context, tx pgx.Tx, userID, agentID string) error {
	if err := agentrun.ValidateTx(ctx, tx); err != nil {
		return err
	}
	return appdb.AdvisoryXactLock(ctx, tx, "mem:"+userID+":"+agentID)
}

// ---------------------------------------------------------------------------
// Constraint helpers
// ---------------------------------------------------------------------------

// GetConstraints reads the constraints JSON array from the DB row.
// Returns an empty slice (not an error) when no row exists or constraints is empty/default.
func GetConstraints(ctx context.Context, q *sqlc.Queries, userID string, agentID string) ([]memory.ConstraintEntry, error) {
	row, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return []memory.ConstraintEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get constraints: %w", err)
	}
	return parseConstraints(string(row.Constraints))
}

// AddConstraint appends a new constraint entry transactionally, bumps version,
// and records a changelog entry with scope='constraint', action='create'.
func AddConstraint(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, userID string, agentID string, text string) ([]memory.ConstraintEntry, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMemory(ctx, tx, userID, agentID); err != nil {
		return nil, err
	}

	qtx := q.WithTx(tx)

	old, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	var beforeJSON string
	var beforeVersion int64
	if err == nil {
		beforeJSON = string(old.Constraints)
		beforeVersion = old.Version
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read constraints: %w", err)
	}

	existing, err := parseConstraints(beforeJSON)
	if err != nil {
		return nil, fmt.Errorf("parse constraints: %w", err)
	}

	entry := memory.ConstraintEntry{
		ID:        fmt.Sprintf("c%d", time.Now().UnixNano()),
		Text:      text,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	existing = append(existing, entry)
	updated := existing

	afterJSON, err := json.Marshal(updated)
	if err != nil {
		return nil, fmt.Errorf("marshal constraints: %w", err)
	}

	row, err := qtx.UpsertAgentConstraints(ctx, sqlc.UpsertAgentConstraintsParams{
		UserID:      userID,
		AgentID:     agentID,
		Constraints: afterJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert constraints: %w", err)
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.Must(uuid.NewV7()).String(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "constraint",
		Action:              "create",
		Source:              source,
		MemoryVersionBefore: pgtype.Int8{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  pgtype.Int8{Int64: row.Version, Valid: true},
		BeforeText:          pgtype.Text{String: beforeJSON, Valid: beforeJSON != "" && beforeJSON != "[]"},
		AfterText:           pgtype.Text{String: string(afterJSON), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("write constraint changelog: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// RemoveConstraint removes a constraint by ID transactionally, bumps version,
// and records a changelog entry with scope='constraint', action='delete'.
func RemoveConstraint(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, userID string, agentID string, id string) ([]memory.ConstraintEntry, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockMemory(ctx, tx, userID, agentID); err != nil {
		return nil, err
	}

	qtx := q.WithTx(tx)

	old, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	var beforeJSON string
	var beforeVersion int64
	if err == nil {
		beforeJSON = string(old.Constraints)
		beforeVersion = old.Version
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read constraints: %w", err)
	}

	existing, err := parseConstraints(beforeJSON)
	if err != nil {
		return nil, fmt.Errorf("parse constraints: %w", err)
	}

	updated := make([]memory.ConstraintEntry, 0, len(existing))
	for _, c := range existing {
		if c.ID != id {
			updated = append(updated, c)
		}
	}

	afterJSON, err := json.Marshal(updated)
	if err != nil {
		return nil, fmt.Errorf("marshal constraints: %w", err)
	}

	row, err := qtx.UpsertAgentConstraints(ctx, sqlc.UpsertAgentConstraintsParams{
		UserID:      userID,
		AgentID:     agentID,
		Constraints: afterJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert constraints: %w", err)
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.Must(uuid.NewV7()).String(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "constraint",
		Action:              "delete",
		Source:              source,
		MemoryVersionBefore: pgtype.Int8{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  pgtype.Int8{Int64: row.Version, Valid: true},
		BeforeText:          pgtype.Text{String: beforeJSON, Valid: beforeJSON != "" && beforeJSON != "[]"},
		AfterText:           pgtype.Text{String: string(afterJSON), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("write constraint changelog: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// ParseConstraintsJSON parses a constraints JSON string into a slice.
// Returns empty slice for "", "[]", or null.
func ParseConstraintsJSON(raw string) ([]memory.ConstraintEntry, error) {
	return parseConstraints(raw)
}

// parseConstraints parses a constraints JSON string into a slice.
// Returns empty slice for "", "[]", or null.
func parseConstraints(raw string) ([]memory.ConstraintEntry, error) {
	if raw == "" || raw == "[]" || raw == "null" {
		return []memory.ConstraintEntry{}, nil
	}
	var entries []memory.ConstraintEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse constraints JSON: %w", err)
	}
	return entries, nil
}
