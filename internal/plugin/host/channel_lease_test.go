package host

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
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
