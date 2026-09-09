package vault_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingVaultRevoker struct {
	calls [][2]string
}

func (r *recordingVaultRevoker) ApplyUserRevocation(_ context.Context, userID, agentID string, mutate func() error) error {
	if err := mutate(); err != nil {
		return err
	}
	r.calls = append(r.calls, [2]string{userID, agentID})
	return nil
}

func TestVaultAccessDeleteUsesTerminalRevocationForEveryScope(t *testing.T) {
	svc, db, ownerID := vaultPEP(t)
	ctx := t.Context()
	agentID := "vault-revocation-agent"
	q := sqlc.New(db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: agentID, Name: "Vault Revocation Agent", Model: "test/model", Workspace: "workspace",
		Sandbox: json.RawMessage(`{}`), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := appdb.NewAuthStore(db).AssignAgent(ctx, ownerID, agentID); err != nil {
		t.Fatalf("AssignAgent: %v", err)
	}
	if err := svc.Set(ctx, ownerID, "VAULT_USER_TOKEN", "user"); err != nil {
		t.Fatalf("Set user secret: %v", err)
	}
	if err := svc.SetScoped(ctx, vault.ScopeUserAgent, ownerID, agentID, "VAULT_AGENT_TOKEN", "user-agent"); err != nil {
		t.Fatalf("Set user-agent secret: %v", err)
	}
	if err := svc.SetSystemScoped(ctx, vault.ScopeSystem, "", "SYSTEM_SECRET", "system"); err != nil {
		t.Fatalf("Set system secret: %v", err)
	}
	if err := svc.SetSystemScoped(ctx, vault.ScopeSystemAgent, agentID, "SYSTEM_AGENT_SECRET", "system-agent"); err != nil {
		t.Fatalf("Set system-agent secret: %v", err)
	}

	revoker := &recordingVaultRevoker{}
	svc.SetRevocationCoordinator(revoker)
	userAuth := userAuthority(t, ownerID)
	adminAuth := adminAuthority(t, "vault-admin")
	userAccess, err := svc.Begin(ctx, userAuth)
	if err != nil {
		t.Fatalf("Begin user: %v", err)
	}
	adminAccess, err := svc.Begin(ctx, adminAuth)
	if err != nil {
		t.Fatalf("Begin admin: %v", err)
	}
	if err := userAccess.DeleteScoped(ctx, vault.ScopeUser, "", "VAULT_USER_TOKEN"); err != nil {
		t.Fatalf("Delete user: %v", err)
	}
	if err := userAccess.DeleteScoped(ctx, vault.ScopeUserAgent, agentID, "VAULT_AGENT_TOKEN"); err != nil {
		t.Fatalf("Delete user-agent: %v", err)
	}
	if err := adminAccess.DeleteScoped(ctx, vault.ScopeSystemAgent, agentID, "SYSTEM_AGENT_SECRET"); err != nil {
		t.Fatalf("Delete system-agent: %v", err)
	}
	if err := adminAccess.DeleteScoped(ctx, vault.ScopeSystem, "", "SYSTEM_SECRET"); err != nil {
		t.Fatalf("Delete system: %v", err)
	}

	want := [][2]string{{ownerID, ""}, {ownerID, agentID}, {"", agentID}, {"", ""}}
	if !reflect.DeepEqual(revoker.calls, want) {
		t.Fatalf("revocation calls = %v, want %v", revoker.calls, want)
	}
}

func TestVaultAccessDeleteChecksAuthorizationBeforeRevocation(t *testing.T) {
	svc, _, ownerID := vaultPEP(t)
	ctx := t.Context()
	if err := svc.Set(ctx, ownerID, "VAULT_AUTH_TOKEN", "value"); err != nil {
		t.Fatalf("Set secret: %v", err)
	}
	revoker := &recordingVaultRevoker{}
	svc.SetRevocationCoordinator(revoker)
	foreign, err := svc.Begin(ctx, userAuthority(t, "foreign-user"))
	if err != nil {
		t.Fatalf("Begin foreign: %v", err)
	}
	if err := foreign.DeleteScoped(ctx, vault.ScopeSystem, "", "VAULT_AUTH_TOKEN"); err == nil || !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign Delete = %v, want authorization denial", err)
	}
	if len(revoker.calls) != 0 {
		t.Fatalf("revocation ran before authorization: %v", revoker.calls)
	}
}
