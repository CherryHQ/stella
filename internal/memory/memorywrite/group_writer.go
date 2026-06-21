package memorywrite

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// SetGroupMemory writes a group's shared memory content, incrementing version
// atomically. Group memory has no auth_user FK — it is keyed solely by group_id.
// This function MUST NOT be called from DM write paths; the type-level separation
// between group and user writers enforces the private→group wall.
func SetGroupMemory(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, groupID string, content string) error {
	if groupID == "" {
		return fmt.Errorf("group_id is required")
	}
	_, err := q.UpsertGroupMemoryVersioned(ctx, sqlc.UpsertGroupMemoryVersionedParams{
		GroupID: groupID,
		Content: content,
	})
	if err != nil {
		return fmt.Errorf("upsert group memory: %w", err)
	}
	return nil
}

// GetGroupMemory reads the shared memory content for a group.
// Returns ("", nil) when no row exists.
func GetGroupMemory(ctx context.Context, q *sqlc.Queries, groupID string) (string, error) {
	row, err := q.GetGroupMemory(ctx, groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get group memory: %w", err)
	}
	return row.Content, nil
}
