package email_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func seedEmailConfig(t *testing.T, vaultSvc *vault.Service, userID string) {
	t.Helper()
	cfg := email.Config{Default: "work", Accounts: map[string]email.EmailAccount{"work": {
		IMAPHost: "8.8.8.8", SMTPHost: "1.1.1.1", Username: "user@example.com", Password: "secret", From: "user@example.com",
	}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := vaultSvc.SetScoped(context.Background(), vault.ScopeUser, userID, "", "EMAIL_CONFIG", string(b)); err != nil {
		t.Fatalf("set EMAIL_CONFIG: %v", err)
	}
}

// Access rejects an invalid Authority (403) and a valid Authority carrying no
// user (a system agent → 401) before any operation. Email is a user-owned
// capability; a request without a user has nothing to act on.
func TestEmailAccessRejectsInvalidAndSystemAuthority(t *testing.T) {
	db := dbtest.New(t)
	userID := seedEmailUser(t, db, "reject")
	vaultSvc := newEmailVaultService(t, db, userID)
	svc := email.NewService(vaultSvc, sqlc.New(db))

	if _, err := svc.Access(authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Access(zero) err=%v, want forbidden", err)
	}
	sysAuth, err := agentaccess.SystemAgentAuthority("test")
	if err != nil {
		t.Fatalf("SystemAgentAuthority: %v", err)
	}
	if _, err := svc.Access(sysAuth); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("Access(system) err=%v, want unauthenticated", err)
	}
}

// A delegated agent has the same email access as its delegating user (email is
// user-owned: the account config lives in that user's vault).
func TestEmailAgentActsAsUser(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID := seedEmailUser(t, db, "agent")
	vaultSvc := newEmailVaultService(t, db, userID)
	seedEmailConfig(t, vaultSvc, userID)

	svc := email.NewService(vaultSvc, sqlc.New(db))
	authority, err := agentaccess.WorkerAgentAuthority(userID, "agent-x")
	if err != nil {
		t.Fatalf("WorkerAgentAuthority: %v", err)
	}
	acc, err := svc.Access(authority)
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	accounts, err := acc.Accounts(ctx)
	if err != nil {
		t.Fatalf("agent Accounts: %v", err)
	}
	if len(accounts.Accounts) != 1 || accounts.Default != "work" {
		t.Fatalf("Accounts=%+v, want the user's single account", accounts)
	}
}

// A foreign user (with no config of their own) cannot read another user's email;
// the per-user vault namespace isolates them.
func TestEmailForeignUserIsolated(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	ownerID := seedEmailUser(t, db, "owner")
	vaultSvc := newEmailVaultService(t, db, ownerID)
	seedEmailConfig(t, vaultSvc, ownerID)
	foreignID := seedEmailUser(t, db, "foreign")

	svc := email.NewService(vaultSvc, sqlc.New(db))
	acc, err := svc.Access(userAuthority(t, foreignID))
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	if _, err := acc.Accounts(ctx); err == nil {
		t.Fatal("foreign Accounts succeeded, want isolation error")
	}
}
