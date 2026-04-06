package reflect

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
)

// watermarkStore tracks review progress per session.
type watermarkStore struct {
	q *sqlc.Queries
}

func newWatermarkStore(q *sqlc.Queries) *watermarkStore {
	return &watermarkStore{q: q}
}

// get returns the last reviewed timestamp for a session.
// Returns zero time and nil error if never reviewed (sql.ErrNoRows).
// Returns a non-nil error for actual DB failures.
func (ws *watermarkStore) get(ctx context.Context, sessionID string) (time.Time, error) {
	val, err := ws.q.GetReflectWatermark(ctx, sessionID)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get watermark %s: %w", sessionID, err)
	}
	t, err := time.Parse("2006-01-02 15:04:05", val)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse watermark %s: %w", sessionID, err)
	}
	return t, nil
}

// set records the last reviewed timestamp for a session.
func (ws *watermarkStore) set(ctx context.Context, sessionID string, at time.Time) error {
	return ws.q.UpsertReflectWatermark(ctx, sqlc.UpsertReflectWatermarkParams{
		SessionID:  sessionID,
		ReviewedAt: at.UTC().Format("2006-01-02 15:04:05"),
	})
}
