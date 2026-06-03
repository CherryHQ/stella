package agentvault

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// vaultTestDB combines OIDCStore (auth_user) with sqlc.Queries (vault_entry) to
// satisfy vault.DB, mirroring the wrapper in internal/vault's own tests.
type vaultTestDB struct {
	oidc *appdb.OIDCStore
	q    *sqlc.Queries
}

func (d *vaultTestDB) GetVaultUser(ctx context.Context, id string) (sqlc.VaultUser, error) {
	u, err := d.oidc.GetUser(ctx, id)
	if err != nil {
		return sqlc.VaultUser{}, err
	}
	return sqlc.VaultUser{AgePublicKey: u.AgePublicKey, AgePrivateKey: u.AgePrivateKey}, nil
}

func (d *vaultTestDB) GetVaultEntry(ctx context.Context, arg sqlc.GetVaultEntryParams) (sqlc.VaultEntry, error) {
	return d.q.GetVaultEntry(ctx, arg)
}

func (d *vaultTestDB) ListVaultEntriesByUser(ctx context.Context, userID string) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesByUser(ctx, userID)
}

func (d *vaultTestDB) UpsertVaultEntry(ctx context.Context, arg sqlc.UpsertVaultEntryParams) error {
	return d.q.UpsertVaultEntry(ctx, arg)
}

func (d *vaultTestDB) DeleteVaultEntry(ctx context.Context, arg sqlc.DeleteVaultEntryParams) error {
	return d.q.DeleteVaultEntry(ctx, arg)
}

func newTestImpl(t *testing.T) (*impl, string) {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "vault_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	oidc := appdb.NewOIDCStore(db)
	q := sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	svc, err := vault.NewService(&vaultTestDB{oidc: oidc, q: q}, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "u@vault.test", Name: "U"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := oidc.UpdateUserAgeKeys(ctx, user.ID, pubKey, encPrivKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}
	return &impl{svc: svc}, user.ID
}

func ctxWithUser(userID string) context.Context {
	return memory.WithUserID(context.Background(), userID)
}

func TestSchemasHaveNoIdentityProps(t *testing.T) {
	for _, def := range []map[string]any{
		listDef().InputSchema, setDef().InputSchema, deleteDef().InputSchema,
	} {
		props, _ := def["properties"].(map[string]any)
		for _, k := range []string{"user", "user_id", "agent", "agent_id"} {
			if _, ok := props[k]; ok {
				t.Errorf("schema exposes identity prop %q", k)
			}
		}
	}
}

func TestMissingIdentityErrors(t *testing.T) {
	t.Parallel()
	tl, _ := newTestImpl(t)
	if _, err := tl.list(context.Background(), nil); err == nil {
		t.Error("list without identity: want error, got nil")
	}
	if _, err := tl.set(context.Background(), map[string]any{"name": "X", "value": "y"}); err == nil {
		t.Error("set without identity: want error, got nil")
	}
}

func TestSetValidatesArgs(t *testing.T) {
	t.Parallel()
	tl, userID := newTestImpl(t)
	ctx := ctxWithUser(userID)
	if _, err := tl.set(ctx, map[string]any{"value": "y"}); err == nil {
		t.Error("set without name: want error")
	}
	if _, err := tl.set(ctx, map[string]any{"name": "X"}); err == nil {
		t.Error("set without value: want error")
	}
}

func TestSetListDeleteHappyPathNoValueLeak(t *testing.T) {
	t.Parallel()
	tl, userID := newTestImpl(t)
	ctx := ctxWithUser(userID)

	const secret = "ghp_super_secret_value"
	if _, err := tl.set(ctx, map[string]any{"name": "GITHUB_TOKEN", "value": secret}); err != nil {
		t.Fatalf("set: %v", err)
	}

	out, err := tl.list(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "GITHUB_TOKEN") {
		t.Errorf("list output missing entry name: %s", out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("list output leaked secret value: %s", out)
	}

	if _, err := tl.del(ctx, map[string]any{"name": "GITHUB_TOKEN"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	out, err = tl.list(ctx, nil)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if strings.Contains(out, "GITHUB_TOKEN") {
		t.Errorf("entry still present after delete: %s", out)
	}
}
