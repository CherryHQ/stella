package mcp

import "testing"

func TestDeleteForeignOwnerDoesNotPurgeCredential(t *testing.T) {
	db := newFakeDB()
	vault := newFakeVault()
	svc := NewService(db, vault)
	reg, err := svc.Create(t.Context(), CreateInput{
		Scope: ScopeUser, UserID: "owner", Name: "remote", URL: "https://example.test/mcp",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeBearer, Token: "secret-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(t.Context(), reg.ID, ScopeUser, "attacker", ""); err != nil {
		t.Fatal(err)
	}
	if got, ok := vault.stored[vaultKey(ScopeUser, "owner", "", reg.CredentialRef)]; !ok || got != "secret-token" {
		t.Fatalf("foreign delete purged owner credential: present=%t value=%q", ok, got)
	}
}

type recordingInvalidator struct{ users, userAgents, agents, all int }

func (r *recordingInvalidator) InvalidateUser(string) error              { r.users++; return nil }
func (r *recordingInvalidator) InvalidateUserAgent(string, string) error { r.userAgents++; return nil }
func (r *recordingInvalidator) InvalidateAgent(string) error             { r.agents++; return nil }
func (r *recordingInvalidator) InvalidateAll() error                     { r.all++; return nil }

func TestRegistrationMutationsInvalidateThroughService(t *testing.T) {
	db := newFakeDB()
	inv := &recordingInvalidator{}
	svc := NewService(db, nil)
	svc.SetInvalidator(inv)
	reg, err := svc.Create(t.Context(), CreateInput{Scope: ScopeUser, UserID: "u1", Name: "remote", URL: "https://example.test/mcp", Transport: TransportStreamableHTTP, AuthType: AuthTypeNone})
	if err != nil {
		t.Fatal(err)
	}
	if inv.users != 1 {
		t.Fatalf("create invalidations = %d, want 1", inv.users)
	}
	name := "renamed"
	if _, err := svc.Update(t.Context(), UpdateInput{ID: reg.ID, Scope: ScopeUser, UserID: "u1", Name: &name}); err != nil {
		t.Fatal(err)
	}
	if inv.users != 2 {
		t.Fatalf("update invalidations = %d, want 2", inv.users)
	}
	if err := svc.Delete(t.Context(), reg.ID, ScopeUser, "u1", ""); err != nil {
		t.Fatal(err)
	}
	if inv.users != 3 {
		t.Fatalf("delete invalidations = %d, want 3", inv.users)
	}
}
