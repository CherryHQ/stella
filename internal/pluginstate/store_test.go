package pluginstate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/orgctx"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func newTestStore(t *testing.T) (*Store, *sql.DB, context.Context) {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orgID, err := appdb.EnsureDefaultOrg(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultOrg: %v", err)
	}
	return New(db), db, orgctx.WithOrgID(context.Background(), orgID)
}

func TestStoreSetGetDelete(t *testing.T) {
	store, _, ctx := newTestStore(t)
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
	store, _, ctx := newTestStore(t)

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

func TestStoreSameKeyIsOrgScoped(t *testing.T) {
	store, db, ctx1 := newTestStore(t)
	if _, err := db.Exec(`INSERT INTO auth_organization (id, name, external_id, source) VALUES (?, ?, ?, ?)`, "org-2", "Org 2", "org-2", "test"); err != nil {
		t.Fatalf("create org2: %v", err)
	}
	ctx2 := orgctx.WithOrgID(context.Background(), "org-2")
	scope := pkgplugins.StateScope{Kind: pkgplugins.StateScopeGlobal}

	if err := store.Set(ctx1, "reflect", scope, "config", map[string]any{"org": "one"}); err != nil {
		t.Fatalf("Set org1: %v", err)
	}
	if err := store.Set(ctx2, "reflect", scope, "config", map[string]any{"org": "two"}); err != nil {
		t.Fatalf("Set org2: %v", err)
	}

	got1, ok, err := store.Get(ctx1, "reflect", scope, "config")
	if err != nil || !ok {
		t.Fatalf("Get org1: ok=%v err=%v", ok, err)
	}
	got2, ok, err := store.Get(ctx2, "reflect", scope, "config")
	if err != nil || !ok {
		t.Fatalf("Get org2: ok=%v err=%v", ok, err)
	}
	if got1["org"] != "one" || got2["org"] != "two" {
		t.Fatalf("plugin state crossed orgs: org1=%v org2=%v", got1, got2)
	}
}
