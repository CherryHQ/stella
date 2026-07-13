package email_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// countingAuthorizer proves the PEP opens exactly one Begin per use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

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

func TestEmailBeginRejectsInvalidAuthority(t *testing.T) {
	db := dbtest.New(t)
	userID := seedEmailUser(t, db, "invalid")
	vaultSvc := newEmailVaultService(t, db, userID)
	svc := email.NewService(vaultSvc, sqlc.New(db), policy.New(db))
	if _, err := svc.Begin(context.Background(), authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Begin(zero) err=%v, want forbidden", err)
	}
}

// A delegated agent has the same email access as its delegating user (email is
// user-owned, not agent-confined); exactly one Begin is opened per use case.
func TestEmailAgentActsAsUser(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID := seedEmailUser(t, db, "agent")
	vaultSvc := newEmailVaultService(t, db, userID)
	seedEmailConfig(t, vaultSvc, userID)

	az := &countingAuthorizer{Authorizer: policy.New(db)}
	svc := email.NewService(vaultSvc, sqlc.New(db), az)

	authority, err := agentaccess.WorkerAgentAuthority(userID, "agent-x")
	if err != nil {
		t.Fatalf("WorkerAgentAuthority: %v", err)
	}
	acc, err := svc.Begin(ctx, authority)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	before := az.begins
	accounts, err := acc.Accounts(ctx)
	if err != nil {
		t.Fatalf("agent Accounts: %v", err)
	}
	if len(accounts.Accounts) != 1 || accounts.Default != "work" {
		t.Fatalf("Accounts=%+v, want the user's single account", accounts)
	}
	if az.begins != before {
		t.Fatalf("Accounts opened %d extra Begins, want 0 within one Access", az.begins-before)
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

	svc := email.NewService(vaultSvc, sqlc.New(db), policy.New(db))
	acc, err := svc.Begin(ctx, userAuthority(t, foreignID))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Accounts(ctx); err == nil {
		t.Fatal("foreign Accounts succeeded, want isolation error")
	}
}

// A custom deny on is_owner read overrides the owner built-in.
func TestEmailCustomDenyHidesOwnAccounts(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	userID := seedEmailUser(t, db, "deny")
	vaultSvc := newEmailVaultService(t, db, userID)
	seedEmailConfig(t, vaultSvc, userID)

	ps := policy.NewService(policy.New(db))
	if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
		Name: "deny own email read", Resource: authz.ResourceEmail, Action: authz.ActionRead,
		Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
		Predicates: []policy.Predicate{policy.Eq("is_owner", "true")},
	}); err != nil {
		t.Fatal(err)
	}
	svc := email.NewService(vaultSvc, sqlc.New(db), policy.New(db))
	acc, err := svc.Begin(ctx, userAuthority(t, userID))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Accounts(ctx); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("custom deny Accounts err=%v, want forbidden", err)
	}
}
