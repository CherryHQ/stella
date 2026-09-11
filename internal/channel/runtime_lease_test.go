package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// The channel runtime lease is the single-owner fence for pollers and senders:
// one live token per channel, token-fenced writes, expiry opens takeover.
func TestChannelRuntimeLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	q := sqlc.New(db)
	if _, err := q.CreateChannel(ctx, sqlc.CreateChannelParams{ID: "ch-1", Name: "ch-1", Type: "telegram"}); err != nil {
		t.Fatal(err)
	}

	// Replica A claims.
	a, err := q.ClaimChannelRuntime(ctx, sqlc.ClaimChannelRuntimeParams{
		ChannelID: "ch-1", OwnerID: pgtype.Text{String: "replica-a", Valid: true}, Token: pgtype.Text{String: "11111111-1111-4111-8111-111111111111", Valid: true},
	})
	if err != nil {
		t.Fatalf("claim a: %v", err)
	}
	if a.RuntimeOwnerID.String != "replica-a" {
		t.Fatalf("owner %q", a.RuntimeOwnerID.String)
	}

	// Replica B cannot take a live lease.
	if _, err := q.ClaimChannelRuntime(ctx, sqlc.ClaimChannelRuntimeParams{
		ChannelID: "ch-1", OwnerID: pgtype.Text{String: "replica-b", Valid: true}, Token: pgtype.Text{String: "22222222-2222-4222-8222-222222222222", Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim b on live lease: %v", err)
	}

	// A's writes are fenced by its token; B's token writes nothing.
	n, err := q.UpdateChannelCheckpoint(ctx, sqlc.UpdateChannelCheckpointParams{
		ChannelID: "ch-1", Token: pgtype.Text{String: "22222222-2222-4222-8222-222222222222", Valid: true},
		Checkpoint: []byte(`{"cursor":9}`),
	})
	if err != nil || n != 0 {
		t.Fatalf("foreign checkpoint write: n=%d err=%v", n, err)
	}
	n, err = q.UpdateChannelCheckpoint(ctx, sqlc.UpdateChannelCheckpointParams{
		ChannelID: "ch-1", Token: pgtype.Text{String: "11111111-1111-4111-8111-111111111111", Valid: true},
		Checkpoint: []byte(`{"cursor":9}`),
	})
	if err != nil || n != 1 {
		t.Fatalf("owner checkpoint write: n=%d err=%v", n, err)
	}
	if n, _ := q.RenewChannelRuntime(ctx, sqlc.RenewChannelRuntimeParams{
		ChannelID: "ch-1", Token: pgtype.Text{String: "11111111-1111-4111-8111-111111111111", Valid: true},
	}); n != 1 {
		t.Fatalf("renew: n=%d", n)
	}

	// Claimable scan skips the live lease; backdating expiry opens takeover.
	if rows, err := q.ListClaimableChannels(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("claimable with live lease: %d", len(rows))
	}
	if _, err := db.Exec(ctx,
		`UPDATE channel SET runtime_lease_until = clock_timestamp() - interval '1 second' WHERE id = 'ch-1'`); err != nil {
		t.Fatal(err)
	}
	b, err := q.ClaimChannelRuntime(ctx, sqlc.ClaimChannelRuntimeParams{
		ChannelID: "ch-1", OwnerID: pgtype.Text{String: "replica-b", Valid: true}, Token: pgtype.Text{String: "22222222-2222-4222-8222-222222222222", Valid: true},
	})
	if err != nil {
		t.Fatalf("takeover claim: %v", err)
	}
	if b.RuntimeToken.String != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("token %q", b.RuntimeToken.String)
	}
	// The fenced-out owner's writes now fail.
	if n, _ := q.UpdateChannelCheckpoint(ctx, sqlc.UpdateChannelCheckpointParams{
		ChannelID: "ch-1", Token: pgtype.Text{String: "11111111-1111-4111-8111-111111111111", Valid: true},
		Checkpoint: []byte(`{"cursor":10}`),
	}); n != 0 {
		t.Fatalf("fenced-out owner wrote checkpoint")
	}

	// Release only by the holder.
	if n, _ := q.ReleaseChannelRuntime(ctx, sqlc.ReleaseChannelRuntimeParams{
		ChannelID: "ch-1", Token: pgtype.Text{String: "11111111-1111-4111-8111-111111111111", Valid: true},
	}); n != 0 {
		t.Fatalf("foreign release succeeded")
	}
	if n, _ := q.ReleaseChannelRuntime(ctx, sqlc.ReleaseChannelRuntimeParams{
		ChannelID: "ch-1", Token: pgtype.Text{String: "22222222-2222-4222-8222-222222222222", Valid: true},
	}); n != 1 {
		t.Fatalf("owner release: n=%d", n)
	}
	row, _ := q.GetChannel(ctx, "ch-1")
	if row.RuntimeToken.Valid || row.RuntimeLeaseUntil.Valid {
		t.Fatal("release left lease fields set")
	}
}
