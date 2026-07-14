package vault_test

import (
	"context"
	"errors"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	return roledAuthority(t, id, authz.RoleUser)
}

func adminAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	return roledAuthority(t, id, authz.RoleAdmin)
}

func roledAuthority(t *testing.T, id string, role authz.Role) authz.Authority {
	t.Helper()
	rs, err := authz.NewRoleSet(role)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority(authz.UserID(id), rs, authz.GrantSet{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// vaultPEP builds a PEP-enabled vault Service over a fresh pool, returning the
// pool so the service and its agent gate share the same durable test state,
// and a seeded owner user with age keys provisioned.
func vaultPEP(t *testing.T) (*vault.Service, *pgxpool.Pool, string) {
	t.Helper()
	db := dbtest.New(t)
	ctx := context.Background()
	oidc := appdb.NewOIDCStore(db)
	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	testDB := &vaultTestDB{oidc: oidc, q: sqlc.New(db)}
	authorizer := policy.New()
	agents := agentaccess.NewService(storepkg.NewDBStore(db), appdb.NewAuthStore(db), authorizer)
	svc, err := vault.NewService(testDB, masterID.String(), authorizer, agents)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	owner, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "owner@vault.test", Role: auth.RoleUser})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	pub, encPriv, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := oidc.UpdateUserAgeKeys(ctx, owner.ID, pub, encPriv); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}
	return svc, db, owner.ID
}

// The owner can round-trip a user secret; a foreign user is denied; admin holds
// the admin-managed system scope; a non-admin cannot.
func TestVaultAccessOwnerAdminAndForeign(t *testing.T) {
	svc, _, ownerID := vaultPEP(t)
	ctx := context.Background()
	begin := func(authority authz.Authority) *vault.Access {
		acc, err := svc.Begin(ctx, authority)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		return acc
	}

	if err := begin(userAuthority(t, ownerID)).SetScoped(ctx, vault.ScopeUser, "", "MY_SECRET", "v", vault.SetOptions{}); err != nil {
		t.Fatalf("owner Set: %v", err)
	}
	if got, err := begin(userAuthority(t, ownerID)).GetScoped(ctx, vault.ScopeUser, "", "MY_SECRET"); err != nil || got != "v" {
		t.Fatalf("owner Get = %q, %v; want v", got, err)
	}

	// A foreign user cannot read the owner's user scope (different owner column).
	if _, err := begin(userAuthority(t, "foreign")).GetScoped(ctx, vault.ScopeUser, "", "MY_SECRET"); err == nil {
		t.Fatal("foreign Get succeeded, want denial/not-found")
	}

	// A non-admin user cannot write a system scope; an admin can.
	if err := begin(userAuthority(t, ownerID)).SetScoped(ctx, vault.ScopeSystem, "", "SYS", "s", vault.SetOptions{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("non-admin system Set err=%v, want forbidden", err)
	}
	if err := begin(adminAuthority(t, "admin-1")).SetScoped(ctx, vault.ScopeSystem, "", "SYS", "s", vault.SetOptions{}); err != nil {
		t.Fatalf("admin system Set: %v", err)
	}
	if got, err := begin(adminAuthority(t, "admin-1")).GetScoped(ctx, vault.ScopeSystem, "", "SYS"); err != nil || got != "s" {
		t.Fatalf("admin system Get = %q, %v; want s", got, err)
	}
}
