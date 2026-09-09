package store_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestDBStoreRejectsStaleAgentCreateAndUpdate(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	dbStore := store.NewDBStore(db)
	existing := config.Agent{ID: "fenced-existing", Name: "before", Model: "test/model", Enabled: true}
	if err := dbStore.CreateAgent(ctx, existing); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	oldStore := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	newStore := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(oldStore.Close)
	t.Cleanup(newStore.Close)
	for _, runs := range []*agentrun.Store{oldStore, newStore} {
		if err := runs.RegisterBoot(ctx); err != nil {
			t.Fatalf("register boot: %v", err)
		}
	}
	old, err := oldStore.Acquire(ctx, sessionID, "settings")
	if err != nil {
		t.Fatalf("acquire old run: %v", err)
	}
	oldStore.Close()
	if _, err := db.Exec(ctx, `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, old.Guard.RunID); err != nil {
		t.Fatalf("expire old run: %v", err)
	}
	if _, err := newStore.Acquire(ctx, sessionID, "settings"); err != nil {
		t.Fatalf("acquire successor: %v", err)
	}
	stale := old.ContextWith(ctx)

	if err := dbStore.CreateAgent(stale, config.Agent{ID: "fenced-created", Name: "must not persist", Model: "test/model", Enabled: true}); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale create error = %v, want ErrLeaseLost", err)
	}
	if _, err := sqlc.New(db).GetAgent(ctx, "fenced-created"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale create row lookup = %v, want no row", err)
	}

	if err := dbStore.UpdateAgent(stale, config.Agent{ID: existing.ID, Name: "must not update", Model: existing.Model, Enabled: true}); !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale update error = %v, want ErrLeaseLost", err)
	}
	got, err := dbStore.GetAgent(ctx, existing.ID)
	if err != nil {
		t.Fatalf("read existing agent: %v", err)
	}
	if got.Name != existing.Name {
		t.Fatalf("stale update changed name to %q", got.Name)
	}
}

func TestControlPlaneProviderWriteRejectsStaleRun(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	sessionID := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	dbStore := store.NewDBStore(db)
	provider := config.Provider{ID: "fenced-provider", Type: "openai", Name: "before", Enabled: true}
	if err := dbStore.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	oldStore := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	newStore := agentrun.NewStoreWithContext(ctx, db, uuid.NewString())
	t.Cleanup(oldStore.Close)
	t.Cleanup(newStore.Close)
	for _, runs := range []*agentrun.Store{oldStore, newStore} {
		if err := runs.RegisterBoot(ctx); err != nil {
			t.Fatalf("register boot: %v", err)
		}
	}
	old, err := oldStore.Acquire(ctx, sessionID, "settings")
	if err != nil {
		t.Fatalf("acquire old run: %v", err)
	}
	oldStore.Close()
	if _, err := db.Exec(ctx, `UPDATE agent_run SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, old.Guard.RunID); err != nil {
		t.Fatalf("expire old run: %v", err)
	}
	if _, err := newStore.Acquire(ctx, sessionID, "settings"); err != nil {
		t.Fatalf("acquire successor: %v", err)
	}

	authority, err := authz.NewUserAuthority("admin-fenced", true)
	if err != nil {
		t.Fatalf("admin authority: %v", err)
	}
	access, err := controlplane.NewService(dbStore, nil, nil, nil, nil, nil).Begin(ctx, authority)
	if err != nil {
		t.Fatalf("begin control plane: %v", err)
	}
	_, err = access.UpdateProvider(old.ContextWith(ctx), provider.ID, config.Provider{Name: "must not update", Enabled: true})
	if !errors.Is(err, agentrun.ErrLeaseLost) {
		t.Fatalf("stale control-plane provider write = %v, want ErrLeaseLost", err)
	}
	got, err := dbStore.GetProvider(ctx, provider.ID)
	if err != nil {
		t.Fatalf("read provider: %v", err)
	}
	if got.Name != provider.Name {
		t.Fatalf("stale control-plane write changed name to %q", got.Name)
	}
}
