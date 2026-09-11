package host

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestChannelLeaseSingleOwner(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	if _, err := sqlc.New(db).CreateChannel(ctx, sqlc.CreateChannelParams{ID: "ch-1", Name: "ch-1", Type: "telegram", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a := NewChannelLeases(db, "replica-a")
	b := NewChannelLeases(db, "replica-b")

	row, ok := a.Ensure(ctx, "ch-1")
	if !ok || !row.Enabled {
		t.Fatal("replica A should own the lease")
	}
	if _, ok := b.Ensure(ctx, "ch-1"); ok {
		t.Fatal("replica B must not own a live lease")
	}
	// A's token fences B's state writes: B holds nothing.
	if b.MarkApplied(ctx, "ch-1", "running", "", 1) {
		t.Fatal("non-owner stamped runtime state")
	}
	if !a.MarkApplied(ctx, "ch-1", "running", "", row.ConfigRevision) {
		t.Fatal("owner failed to stamp state")
	}
	var applied int64
	if err := db.QueryRow(ctx, "SELECT applied_revision FROM channel WHERE id='ch-1'").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != row.ConfigRevision {
		t.Fatalf("applied_revision = %d", applied)
	}
}

func TestChannelLeaseTakeoverAfterExpiry(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	if _, err := sqlc.New(db).CreateChannel(ctx, sqlc.CreateChannelParams{ID: "ch-1", Name: "ch-1", Type: "telegram", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a := NewChannelLeases(db, "replica-a")
	b := NewChannelLeases(db, "replica-b")

	if _, ok := a.Ensure(ctx, "ch-1"); !ok {
		t.Fatal("A claim")
	}
	// A dies: lease expires in place.
	if _, err := db.Exec(ctx, "UPDATE channel SET runtime_lease_until = clock_timestamp() - interval '1s' WHERE id='ch-1'"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Ensure(ctx, "ch-1"); !ok {
		t.Fatal("B should take over the expired lease")
	}
	// A's stale token can no longer renew or write.
	if _, err := sqlc.New(db).RenewChannelRuntime(ctx, sqlc.RenewChannelRuntimeParams{ChannelID: "ch-1", Token: pgtype.Text{String: a.Token("ch-1"), Valid: true}}); err == nil {
		// A still thinks it holds the lease — its token must not match.
		t.Fatal("stale token renewed")
	}
}

func TestChannelLeaseReleaseFreesOwnership(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	if _, err := sqlc.New(db).CreateChannel(ctx, sqlc.CreateChannelParams{ID: "ch-1", Name: "ch-1", Type: "telegram", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a := NewChannelLeases(db, "replica-a")
	b := NewChannelLeases(db, "replica-b")
	if _, ok := a.Ensure(ctx, "ch-1"); !ok {
		t.Fatal("A claim")
	}
	a.Release(ctx, "ch-1")
	if _, ok := b.Ensure(ctx, "ch-1"); !ok {
		t.Fatal("B should claim after release")
	}
}

// A renew failure must stop the local ingress even when the follow-up
// reconcile also cannot reach the DB — otherwise a stale poller outlives
// a takeover.
func TestRenewFailureStopsLocalIngress(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	if _, err := sqlc.New(db).CreateChannel(ctx, sqlc.CreateChannelParams{ID: "self-ch", Name: "self-ch", Type: "feishu", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	store := &failOnceChannelStore{stubStore: &stubStore{channels: map[string][]config.Channel{
		"feishu": {{ID: "self-ch", Type: "feishu", Enabled: true}},
	}}}
	h := New(store, WithListenerCap(allowAllListenerCap), WithChannelLeases(db, "a"))
	h.RegisterPluginID("channel/feishu")
	runtime := &channelInstanceRuntime{id: "self-ch"}
	h.AddRuntime(pkgplugins.RuntimeSpec{
		PluginID: "channel/feishu", Name: "bot",
		Build: func(pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) { return runtime, nil },
	})
	if err := h.ReconcileChannel(ctx, "self-ch"); err != nil {
		t.Fatal(err)
	}
	if !runtime.state.Enabled {
		t.Fatal("setup: runtime not enabled")
	}

	// Simultaneous outage: renew fails AND the reconcile's GetChannel fails.
	store.fail = true
	h.channelLeases.q = sqlc.New(failingRowDB{db})
	h.channelLeases.renewHeld(ctx)
	if h.channelLeases.Token("self-ch") != "" {
		t.Fatal("setup: token not dropped")
	}
	store.fail = false
	h.channelLeases.q = sqlc.New(db)

	// Lease expires; replica B takes over.
	if _, err := db.Exec(ctx, "UPDATE channel SET runtime_lease_until=clock_timestamp()-interval '1 second' WHERE id='self-ch'"); err != nil {
		t.Fatal(err)
	}
	b := NewChannelLeases(db, "b")
	if _, ok := b.Ensure(ctx, "self-ch"); !ok {
		t.Fatal("B takeover failed")
	}
	h.channelLeases.renewHeld(ctx)
	h.channelLeases.sweep(ctx)

	// The renew failure must have evicted the runtime locally — an unadopted
	// entry or a re-applied enabled runtime would mean a zombie ingress.
	h.runtimes.mu.RLock()
	entry := h.runtimes.rt[runtimeKey{RuntimeID: "self-ch", RuntimeName: "bot"}]
	h.runtimes.mu.RUnlock()
	if entry != nil && entry.managed != nil && runtime.state.Enabled {
		t.Fatal("old ingress survived the DB outage and B's takeover")
	}
}

type failOnceChannelStore struct {
	*stubStore
	fail bool
}

func (s *failOnceChannelStore) GetChannel(ctx context.Context, id string) (config.Channel, error) {
	if s.fail {
		return config.Channel{}, errors.New("database unavailable")
	}
	return s.stubStore.GetChannel(ctx, id)
}

type (
	failingRowDB struct{ *pgxpool.Pool }
	failingRow   struct{}
)

func (failingRow) Scan(...any) error { return errors.New("database unavailable") }
func (d failingRowDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return failingRow{}
}
