package reflect

import (
	"context"
	"fmt"
	"math"
	"strconv"
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

type reviewWatermark struct {
	At  time.Time
	Seq int64
}

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

func (ws *watermarkStore) getLine(ctx context.Context, sessionID string, line reflectLine) (reviewWatermark, error) {
	key := lineWatermarkKey(line)
	val, ok, err := ws.store.Get(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   sessionID,
	}, key)
	if err != nil {
		return reviewWatermark{}, fmt.Errorf("get watermark %s %s: %w", sessionID, line, err)
	}
	if ok {
		return parseWatermarkValue(sessionID, key, val)
	}

	legacy, err := ws.get(ctx, sessionID)
	if err != nil {
		return reviewWatermark{}, err
	}
	if legacy.IsZero() {
		return reviewWatermark{}, nil
	}
	mark := reviewWatermark{At: legacy}
	if err := ws.setLine(ctx, sessionID, line, mark); err != nil {
		return reviewWatermark{}, err
	}
	return mark, nil
}

func (ws *watermarkStore) setLine(ctx context.Context, sessionID string, line reflectLine, mark reviewWatermark) error {
	value := map[string]any{}
	if mark.Seq > 0 {
		value["reviewed_seq"] = mark.Seq
	}
	if !mark.At.IsZero() {
		value["reviewed_at"] = mark.At.UTC().Format(time.RFC3339Nano)
	}
	return ws.store.Set(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   sessionID,
	}, lineWatermarkKey(line), value)
}

func parseWatermarkValue(sessionID, key string, val map[string]any) (reviewWatermark, error) {
	var mark reviewWatermark
	if seq, ok, err := parseWatermarkSeq(val["reviewed_seq"]); err != nil {
		return reviewWatermark{}, fmt.Errorf("parse watermark %s %s seq: %w", sessionID, key, err)
	} else if ok {
		mark.Seq = seq
	}
	raw, _ := val["reviewed_at"].(string)
	if raw == "" {
		return mark, nil
	}
	t, err := parseWatermark(raw)
	if err != nil {
		return reviewWatermark{}, fmt.Errorf("parse watermark %s %s: %w", sessionID, key, err)
	}
	mark.At = t
	return mark, nil
}

func parseWatermarkSeq(raw any) (int64, bool, error) {
	switch v := raw.(type) {
	case nil:
		return 0, false, nil
	case int:
		if v < 0 {
			return 0, false, fmt.Errorf("negative seq %d", v)
		}
		return int64(v), true, nil
	case int64:
		if v < 0 {
			return 0, false, fmt.Errorf("negative seq %d", v)
		}
		return v, true, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < 0 {
			return 0, false, fmt.Errorf("invalid non-negative integral seq %v", v)
		}
		// float64(math.MaxInt64) rounds to 1<<63, so equality is out of range too.
		if v >= float64(math.MaxInt64) {
			return 0, false, fmt.Errorf("seq %v is out of int64 range", v)
		}
		return int64(v), true, nil
	case string:
		if v == "" {
			return 0, false, nil
		}
		seq, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, false, err
		}
		if seq < 0 {
			return 0, false, fmt.Errorf("negative seq %d", seq)
		}
		return seq, true, nil
	default:
		return 0, false, fmt.Errorf("unsupported seq type %T", raw)
	}
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
