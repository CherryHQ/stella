package pluginstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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

func TestStoreRejectsStaleAgentRunMutation(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	const sessionID = "plugin-state-fence"
	if _, err := q.CreateConversation(t.Context(), sqlc.CreateConversationParams{
		ID: uuid.NewString(), SessionID: sessionID, Channel: "web", Kind: "chat",
		LastActive: time.Now().UTC(), AgentID: pgtype.Text{String: "agent", Valid: true},
		UserID: pgtype.Text{String: uuid.NewString(), Valid: true},
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	firstBootID := agentrun.NewBootID()
	if _, err := q.CreateExecutorBoot(t.Context(), sqlc.CreateExecutorBootParams{ID: firstBootID}); err != nil {
		t.Fatalf("register first executor boot: %v", err)
	}
	first, err := agentrun.NewStore(db, firstBootID).Acquire(t.Context(), sessionID, "web")
	if err != nil {
		t.Fatalf("acquire first run: %v", err)
	}
	if err := first.Finish(t.Context(), agentrun.StatusCompleted, ""); err != nil {
		t.Fatalf("finish first run: %v", err)
	}
	secondBootID := agentrun.NewBootID()
	if _, err := q.CreateExecutorBoot(t.Context(), sqlc.CreateExecutorBootParams{ID: secondBootID}); err != nil {
		t.Fatalf("register replacement executor boot: %v", err)
	}
	second, err := agentrun.NewStore(db, secondBootID).Acquire(t.Context(), sessionID, "web")
	if err != nil {
		t.Fatalf("acquire replacement run: %v", err)
	}
	defer func() { _ = second.Finish(t.Context(), agentrun.StatusCompleted, "") }()

	store := New(db)
	scope := pkgplugins.StateScope{Kind: pkgplugins.StateScopeSession, ID: sessionID}
	if err := store.Set(t.Context(), "tool/example", scope, "result", map[string]any{"owner": "current"}); err != nil {
		t.Fatalf("seed plugin state: %v", err)
	}
	stale := agentrun.WithGuard(t.Context(), first.Guard)
	if err := store.Set(stale, "tool/example", scope, "result", map[string]any{"owner": "stale"}); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale Set = %v, want ErrLeaseLost", err)
	}
	if err := store.Delete(stale, "tool/example", scope, "result"); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale Delete = %v, want ErrLeaseLost", err)
	}
	value, ok, err := store.Get(t.Context(), "tool/example", scope, "result")
	if err != nil || !ok || value["owner"] != "current" {
		t.Fatalf("plugin state after stale mutations = %#v/%v err=%v", value, ok, err)
	}
}
