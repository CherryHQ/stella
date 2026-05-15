// Package memorywrite provides shared transactional write helpers for profile and soul
// memory updates. Each write increments the version counter atomically and appends a
// changelog entry in the same transaction.
package memorywrite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/memory"
)

// SetProfile writes a profile update within a transaction.
// It increments the version, reads the before-state, and appends a changelog entry,
// all atomically. The source is derived from ctx via memory.ChangeSourceFromContext.
func SetProfile(ctx context.Context, db *sql.DB, q *sqlc.Queries, userID int64, agentID string, content string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := q.WithTx(tx)

	old, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	var beforeText string
	var beforeVersion int64
	if err == nil {
		beforeText = old.Content
		beforeVersion = old.Version
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read current profile: %w", err)
	}

	row, err := qtx.UpsertUserAgentMemoryVersioned(ctx, sqlc.UpsertUserAgentMemoryVersionedParams{
		UserID:  userID,
		AgentID: agentID,
		Content: content,
	})
	if err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}

	action := "update"
	if beforeText == "" && beforeVersion == 0 {
		action = "create"
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.NewString(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "profile",
		Action:              action,
		Source:              source,
		MemoryVersionBefore: sql.NullInt64{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  sql.NullInt64{Int64: row.Version, Valid: true},
		BeforeText:          sql.NullString{String: beforeText, Valid: beforeText != ""},
		AfterText:           sql.NullString{String: content, Valid: content != ""},
	}); err != nil {
		return fmt.Errorf("write profile changelog: %w", err)
	}

	return tx.Commit()
}

// SetAgentSoul writes a soul update within a transaction, incrementing version
// and appending a changelog entry atomically.
func SetAgentSoul(ctx context.Context, db *sql.DB, q *sqlc.Queries, userID int64, agentID string, content string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := q.WithTx(tx)

	old, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	var beforeText string
	var beforeVersion int64
	if err == nil {
		beforeText = old.Soul
		beforeVersion = old.Version
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read current soul: %w", err)
	}

	row, err := qtx.UpsertAgentSoulVersioned(ctx, sqlc.UpsertAgentSoulVersionedParams{
		UserID:  userID,
		AgentID: agentID,
		Soul:    content,
	})
	if err != nil {
		return fmt.Errorf("upsert soul: %w", err)
	}

	action := "update"
	if beforeText == "" && beforeVersion == 0 {
		action = "create"
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.NewString(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "soul",
		Action:              action,
		Source:              source,
		MemoryVersionBefore: sql.NullInt64{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  sql.NullInt64{Int64: row.Version, Valid: true},
		BeforeText:          sql.NullString{String: beforeText, Valid: beforeText != ""},
		AfterText:           sql.NullString{String: content, Valid: content != ""},
	}); err != nil {
		return fmt.Errorf("write soul changelog: %w", err)
	}

	return tx.Commit()
}

// DeleteProfile writes a changelog entry for a profile deletion, then deletes the record.
func DeleteProfile(ctx context.Context, db *sql.DB, q *sqlc.Queries, userID int64, agentID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := q.WithTx(tx)

	old, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	var beforeText string
	var beforeVersion int64
	if err == nil {
		beforeText = old.Content
		beforeVersion = old.Version
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read current profile: %w", err)
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.NewString(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "profile",
		Action:              "delete",
		Source:              source,
		MemoryVersionBefore: sql.NullInt64{Int64: beforeVersion, Valid: beforeVersion > 0},
		MemoryVersionAfter:  sql.NullInt64{},
		BeforeText:          sql.NullString{String: beforeText, Valid: beforeText != ""},
		AfterText:           sql.NullString{},
	}); err != nil {
		return fmt.Errorf("write delete changelog: %w", err)
	}

	if err := qtx.DeleteUserAgentMemory(ctx, sqlc.DeleteUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	}); err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}

	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Constraint helpers
// ---------------------------------------------------------------------------

// GetConstraints reads the constraints JSON array from the DB row.
// Returns an empty slice (not an error) when no row exists or constraints is empty/default.
func GetConstraints(ctx context.Context, q *sqlc.Queries, userID int64, agentID string) ([]memory.ConstraintEntry, error) {
	row, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return []memory.ConstraintEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get constraints: %w", err)
	}
	return parseConstraints(row.Constraints)
}

// AddConstraint appends a new constraint entry transactionally, bumps version,
// and records a changelog entry with scope='constraint', action='create'.
func AddConstraint(ctx context.Context, db *sql.DB, q *sqlc.Queries, userID int64, agentID string, text string) ([]memory.ConstraintEntry, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := q.WithTx(tx)

	old, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	var beforeJSON string
	var beforeVersion int64
	if err == nil {
		beforeJSON = old.Constraints
		beforeVersion = old.Version
	} else if !errors.Is(err, sql.ErrNoRows) {
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
		Constraints: string(afterJSON),
	})
	if err != nil {
		return nil, fmt.Errorf("upsert constraints: %w", err)
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.NewString(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "constraint",
		Action:              "create",
		Source:              source,
		MemoryVersionBefore: sql.NullInt64{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  sql.NullInt64{Int64: row.Version, Valid: true},
		BeforeText:          sql.NullString{String: beforeJSON, Valid: beforeJSON != "" && beforeJSON != "[]"},
		AfterText:           sql.NullString{String: string(afterJSON), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("write constraint changelog: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// RemoveConstraint removes a constraint by ID transactionally, bumps version,
// and records a changelog entry with scope='constraint', action='delete'.
func RemoveConstraint(ctx context.Context, db *sql.DB, q *sqlc.Queries, userID int64, agentID string, id string) ([]memory.ConstraintEntry, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := q.WithTx(tx)

	old, err := qtx.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	var beforeJSON string
	var beforeVersion int64
	if err == nil {
		beforeJSON = old.Constraints
		beforeVersion = old.Version
	} else if !errors.Is(err, sql.ErrNoRows) {
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
		Constraints: string(afterJSON),
	})
	if err != nil {
		return nil, fmt.Errorf("upsert constraints: %w", err)
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.NewString(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "constraint",
		Action:              "delete",
		Source:              source,
		MemoryVersionBefore: sql.NullInt64{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  sql.NullInt64{Int64: row.Version, Valid: true},
		BeforeText:          sql.NullString{String: beforeJSON, Valid: beforeJSON != "" && beforeJSON != "[]"},
		AfterText:           sql.NullString{String: string(afterJSON), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("write constraint changelog: %w", err)
	}

	if err := tx.Commit(); err != nil {
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
