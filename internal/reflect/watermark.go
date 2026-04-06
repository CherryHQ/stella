package reflect

import (
	"context"
	"database/sql"
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
// Returns zero time if never reviewed.
func (ws *watermarkStore) get(ctx context.Context, sessionID string) time.Time {
	val, err := ws.q.GetReflectWatermark(ctx, sessionID)
	if err == sql.ErrNoRows || err != nil {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02 15:04:05", val)
	if err != nil {
		return time.Time{}
	}
	return t
}

// set records the last reviewed timestamp for a session.
func (ws *watermarkStore) set(ctx context.Context, sessionID string, at time.Time) error {
	return ws.q.UpsertReflectWatermark(ctx, sqlc.UpsertReflectWatermarkParams{
		SessionID:  sessionID,
		ReviewedAt: at.UTC().Format("2006-01-02 15:04:05"),
	})
}
