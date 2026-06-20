package pluginstate

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	db := dbtest.New(t)
	return New(db), context.Background()
}

func TestStoreSetGetDelete(t *testing.T) {
	store, ctx := newTestStore(t)
	scope := pkgplugins.StateScope{Kind: pkgplugins.StateScopeSession, ID: "sess-1"}

	got, ok, err := store.Get(ctx, "reflect", scope, "watermark")
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("missing get = (%v, %v), want (nil, false)", got, ok)
	}

	if err := store.Set(ctx, "reflect", scope, "watermark", map[string]any{"reviewed_at": "2026-04-08 10:00:00"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err = store.Get(ctx, "reflect", scope, "watermark")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected stored value")
	}
	if got["reviewed_at"] != "2026-04-08 10:00:00" {
		t.Fatalf("reviewed_at = %#v", got["reviewed_at"])
	}

	if err := store.Delete(ctx, "reflect", scope, "watermark"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, ok, err = store.Get(ctx, "reflect", scope, "watermark")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("post-delete get = (%v, %v), want (nil, false)", got, ok)
	}
}

func TestStoreNormalizesGlobalScope(t *testing.T) {
	store, ctx := newTestStore(t)

	if err := store.Set(ctx, "reflect", pkgplugins.StateScope{}, "config", map[string]any{"v": "x"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := store.Get(ctx, "reflect", pkgplugins.StateScope{Kind: pkgplugins.StateScopeGlobal, ID: "ignored"}, "config")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got["v"] != "x" {
		t.Fatalf("unexpected value: %#v ok=%v", got, ok)
	}
}
