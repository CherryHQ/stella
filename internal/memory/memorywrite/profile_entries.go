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

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// GetProfileEntries reads the auto-generated dated profile entries from a user's
// memory row. Returns an empty slice when no row or entries exist.
func GetProfileEntries(ctx context.Context, q *sqlc.Queries, userID, agentID string) ([]memory.ProfileEntry, error) {
	row, err := q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return []memory.ProfileEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get profile entries: %w", err)
	}
	return ParseProfileEntriesJSON(string(row.ProfileEntries))
}

// AddProfileEntry appends an auto-generated dated entry to the profile_entries
// column, bumps version, and records a changelog entry. The manual profile
// content column is never touched.
func AddProfileEntry(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, userID, agentID, text string) ([]memory.ProfileEntry, error) {
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
		beforeJSON = string(old.ProfileEntries)
		beforeVersion = old.Version
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read profile entries: %w", err)
	}

	existing, err := ParseProfileEntriesJSON(beforeJSON)
	if err != nil {
		return nil, fmt.Errorf("parse profile entries: %w", err)
	}

	entry := memory.ProfileEntry{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Text:      text,
		Source:    "auto",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	existing = append(existing, entry)

	afterJSON, err := json.Marshal(existing)
	if err != nil {
		return nil, fmt.Errorf("marshal profile entries: %w", err)
	}

	row, err := qtx.UpsertProfileEntries(ctx, sqlc.UpsertProfileEntriesParams{
		UserID:         userID,
		AgentID:        agentID,
		ProfileEntries: afterJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert profile entries: %w", err)
	}

	source := string(memory.ChangeSourceFromContext(ctx))
	if err := qtx.InsertMemoryChangelog(ctx, sqlc.InsertMemoryChangelogParams{
		ID:                  uuid.Must(uuid.NewV7()).String(),
		UserID:              userID,
		AgentID:             agentID,
		Scope:               "profile_entry",
		Action:              "create",
		Source:              source,
		MemoryVersionBefore: pgtype.Int8{Int64: beforeVersion, Valid: true},
		MemoryVersionAfter:  pgtype.Int8{Int64: row.Version, Valid: true},
		BeforeText:          pgtype.Text{String: beforeJSON, Valid: beforeJSON != "" && beforeJSON != "[]"},
		AfterText:           pgtype.Text{String: string(afterJSON), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("write profile entry changelog: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return existing, nil
}

// ParseProfileEntriesJSON parses profile entries JSON. Returns empty slice for
// "", "[]", or "null".
func ParseProfileEntriesJSON(raw string) ([]memory.ProfileEntry, error) {
	if raw == "" || raw == "[]" || raw == "null" {
		return []memory.ProfileEntry{}, nil
	}
	var entries []memory.ProfileEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse profile entries JSON: %w", err)
	}
	return entries, nil
}
