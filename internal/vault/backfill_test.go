package vault_test

import (
	"context"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
)

func TestBackfillUserKeys(t *testing.T) {
	t.Parallel()

	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "backfill_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	oidc := appdb.NewOIDCStore(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	for _, name := range []string{"alice", "bob"} {
		if _, err := oidc.CreateUser(ctx, auth.User{
			ID:    uuid.NewString(),
			Email: name + "@backfill.test",
			Name:  name,
		}); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
	}

	n, err := vault.BackfillUserKeys(ctx, oidc, masterID.Recipient())
	if err != nil {
		t.Fatalf("BackfillUserKeys: %v", err)
	}
	if n != 2 {
		t.Fatalf("BackfillUserKeys = %d, want 2", n)
	}

	n, err = vault.BackfillUserKeys(ctx, oidc, masterID.Recipient())
	if err != nil {
		t.Fatalf("BackfillUserKeys (second): %v", err)
	}
	if n != 0 {
		t.Fatalf("BackfillUserKeys (second) = %d, want 0", n)
	}

	users, err := oidc.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range users {
		if u.AgePublicKey == "" {
			t.Errorf("user %s: AgePublicKey is empty after backfill", u.Email)
		}
		if u.AgePrivateKey == "" {
			t.Errorf("user %s: AgePrivateKey is empty after backfill", u.Email)
		}
	}
}
