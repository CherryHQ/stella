package vault_test

import (
	"context"
	"path/filepath"
	"testing"

	"filippo.io/age"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestBackfillUserKeys(t *testing.T) {
	t.Parallel()

	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "backfill_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	// Create two users without age keys.
	for _, name := range []string{"alice", "bob"} {
		if _, err := q.CreateAuthUser(ctx, sqlc.CreateAuthUserParams{
			Username:     name,
			PasswordHash: "hash",
		}); err != nil {
			t.Fatalf("CreateAuthUser(%s): %v", name, err)
		}
	}

	// Backfill should update both.
	n, err := vault.BackfillUserKeys(ctx, q, masterID.Recipient())
	if err != nil {
		t.Fatalf("BackfillUserKeys: %v", err)
	}
	if n != 2 {
		t.Fatalf("BackfillUserKeys = %d, want 2", n)
	}

	// Running again should update none.
	n, err = vault.BackfillUserKeys(ctx, q, masterID.Recipient())
	if err != nil {
		t.Fatalf("BackfillUserKeys (second): %v", err)
	}
	if n != 0 {
		t.Fatalf("BackfillUserKeys (second) = %d, want 0", n)
	}

	// Verify keys are set.
	users, err := q.ListAuthUsers(ctx)
	if err != nil {
		t.Fatalf("ListAuthUsers: %v", err)
	}
	for _, u := range users {
		if u.AgePublicKey == "" {
			t.Errorf("user %s: AgePublicKey is empty after backfill", u.Username)
		}
		if u.AgePrivateKey == "" {
			t.Errorf("user %s: AgePrivateKey is empty after backfill", u.Username)
		}
	}
}
