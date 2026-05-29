package reflect

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/pluginstate"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func newTestWatermarkStore(t *testing.T) (*watermarkStore, context.Context) {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

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
