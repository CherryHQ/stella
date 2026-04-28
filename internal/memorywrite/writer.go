// Package memorywrite provides shared transactional write helpers for profile and soul
// memory updates. Each write increments the version counter atomically and appends a
// changelog entry in the same transaction.
package memorywrite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
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
