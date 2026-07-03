package reflect

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/pluginstate"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func newTestWatermarkStore(t *testing.T) (*watermarkStore, context.Context) {
	t.Helper()
	db := dbtest.New(t)

	return newWatermarkStore(testStateStore{store: pluginstate.New(db)}), context.Background()
}

type testStateStore struct {
	store *pluginstate.Store
}

func (s testStateStore) Get(ctx context.Context, scope pkgplugins.StateScope, key string) (map[string]any, bool, error) {
	return s.store.Get(ctx, "reflect", scope, key)
}

func (s testStateStore) Set(ctx context.Context, scope pkgplugins.StateScope, key string, value map[string]any) error {
	return s.store.Set(ctx, "reflect", scope, key, value)
}

func (s testStateStore) Delete(ctx context.Context, scope pkgplugins.StateScope, key string) error {
	return s.store.Delete(ctx, "reflect", scope, key)
}

func TestWatermarkStore_GetMissing(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)
	ts, err := ws.get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("expected nil error for missing key, got %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time for missing key, got %v", ts)
	}
}

func TestWatermarkStore_SetAndGet(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	now := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "s1", now); err != nil {
		t.Fatal(err)
	}

	got, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Errorf("expected %v, got %v", now, got)
	}
}

func TestWatermarkStore_Upsert(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	t1 := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 6, 13, 0, 0, 0, time.UTC)

	if err := ws.set(ctx, "s1", t1); err != nil {
		t.Fatal(err)
	}
	if err := ws.set(ctx, "s1", t2); err != nil {
		t.Fatal(err)
	}

	got, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t2) {
		t.Errorf("expected upserted value %v, got %v", t2, got)
	}
}

func TestWatermarkStore_LineGetSeedsFromLegacy(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	legacy := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "s1", legacy); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(legacy) {
		t.Fatalf("expected legacy seed %v, got %v", legacy, got)
	}
}

func TestWatermarkStore_LineSetWritesRFC3339(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	at := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	if err := ws.setLine(ctx, "s1", reflectLineSkill, reviewWatermark{At: at}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineSkill)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(at) {
		t.Fatalf("expected %v, got %v", at, got)
	}

	raw, ok, err := ws.store.Get(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   "s1",
	}, lineWatermarkKey(reflectLineSkill))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected line watermark key to be written")
	}
	if raw["reviewed_at"] != at.Format(time.RFC3339) {
		t.Fatalf("expected RFC3339 value %q, got %#v", at.Format(time.RFC3339), raw["reviewed_at"])
	}
}

func TestWatermarkStore_LinePrefersLineValueOverLegacy(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	legacy := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	line := time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "s1", legacy); err != nil {
		t.Fatal(err)
	}
	if err := ws.setLine(ctx, "s1", reflectLineFact, reviewWatermark{At: line}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(line) {
		t.Fatalf("expected line value %v, got %v", line, got)
	}
}

func TestWatermarkStore_LineSetWritesSeq(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	at := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	if err := ws.setLine(ctx, "s1", reflectLineFact, reviewWatermark{Seq: 42, At: at}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 42 || !got.At.Equal(at) {
		t.Fatalf("expected seq 42 and at %v, got %#v", at, got)
	}
}

func TestWatermarkStore_LineParsesLegacyLayout(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	at := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	if err := ws.store.Set(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   "s1",
	}, lineWatermarkKey(reflectLineFact), map[string]any{
		"reviewed_at": at.Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(at) {
		t.Fatalf("expected fallback parse %v, got %v", at, got)
	}
}
