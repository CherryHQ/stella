package reflect

import (
	"context"
	"fmt"
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// watermarkStore tracks review progress per session.
type watermarkStore struct {
	store pkgplugins.StateStore
}

const reviewWatermarkKey = "review_watermark"

func newWatermarkStore(store pkgplugins.StateStore) *watermarkStore {
	return &watermarkStore{store: store}
}

// get returns the last reviewed timestamp for a session.
// Returns zero time and nil error if never reviewed (sql.ErrNoRows).
// Returns a non-nil error for actual DB failures.
func (ws *watermarkStore) get(ctx context.Context, sessionID string) (time.Time, error) {
	val, ok, err := ws.store.Get(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   sessionID,
	}, reviewWatermarkKey)
	if !ok {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get watermark %s: %w", sessionID, err)
	}
	raw, _ := val["reviewed_at"].(string)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse watermark %s: %w", sessionID, err)
	}
	return t, nil
}

// set records the last reviewed timestamp for a session.
func (ws *watermarkStore) set(ctx context.Context, sessionID string, at time.Time) error {
	return ws.store.Set(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   sessionID,
	}, reviewWatermarkKey, map[string]any{
		"reviewed_at": at.UTC().Format("2006-01-02 15:04:05"),
	})
}
