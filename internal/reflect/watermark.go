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

type reflectLine string

const (
	reflectLineFact  reflectLine = "fact"
	reflectLineSkill reflectLine = "skill"
)

func newWatermarkStore(store pkgplugins.StateStore) *watermarkStore {
	return &watermarkStore{store: store}
}

func lineWatermarkKey(line reflectLine) string {
	return "reflect_watermark:" + string(line)
}

// get returns the last reviewed timestamp for a session.
// Returns zero time and nil error if never reviewed (pgx.ErrNoRows).
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
	t, err := parseWatermark(raw)
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

func (ws *watermarkStore) getLine(ctx context.Context, sessionID string, line reflectLine) (time.Time, error) {
	key := lineWatermarkKey(line)
	val, ok, err := ws.store.Get(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   sessionID,
	}, key)
	if err != nil {
		return time.Time{}, fmt.Errorf("get watermark %s %s: %w", sessionID, line, err)
	}
	if ok {
		return parseWatermarkValue(sessionID, key, val)
	}

	legacy, err := ws.get(ctx, sessionID)
	if err != nil {
		return time.Time{}, err
	}
	if legacy.IsZero() {
		return time.Time{}, nil
	}
	if err := ws.setLine(ctx, sessionID, line, legacy); err != nil {
		return time.Time{}, err
	}
	return legacy, nil
}

func (ws *watermarkStore) setLine(ctx context.Context, sessionID string, line reflectLine, at time.Time) error {
	return ws.store.Set(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   sessionID,
	}, lineWatermarkKey(line), map[string]any{
		"reviewed_at": at.UTC().Format(time.RFC3339Nano),
	})
}

func parseWatermarkValue(sessionID, key string, val map[string]any) (time.Time, error) {
	raw, _ := val["reviewed_at"].(string)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := parseWatermark(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse watermark %s %s: %w", sessionID, key, err)
	}
	return t, nil
}

func parseWatermark(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", raw)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
