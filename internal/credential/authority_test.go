package credential

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestPrincipalAuthority proves a resolved bearer principal maps to a UserActor
// whose scopes become entry-scope grants and whose admin flag maps to the role
// catalog.
func TestPrincipalAuthority(t *testing.T) {
	p := Principal{
		Kind:    KindPAT,
		UserID:  "u1",
		Scopes:  []string{"goals:read", "agent:write"},
		IsAdmin: false,
	}
	a, err := p.Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if a.Kind() != authz.ActorUser || a.Actor().UserID() != "u1" {
		t.Fatalf("bad actor: %+v", a.Actor())
	}
	if a.IsAdmin() {
		t.Error("non-admin principal must not be admin")
	}
	if !a.HasRole(authz.RoleUser) {
		t.Error("principal must hold the user role")
	}
	scope, _ := authz.EntryScopeGrant("goals:read")
	if !a.HasGrant(scope) {
		t.Error("scope goals:read did not become an entry-scope grant")
	}
	if len(a.Grants()) != 2 {
		t.Fatalf("grants = %d, want 2", len(a.Grants()))
	}
}

// TestPrincipalAuthorityAdmin proves the admin flag adds the admin role.
func TestPrincipalAuthorityAdmin(t *testing.T) {
	a, err := Principal{UserID: "root", IsAdmin: true}.Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if !a.IsAdmin() {
		t.Error("admin principal must hold admin role")
	}
}

// TestPrincipalAuthorityRejectsEmptyScope proves a malformed scope fails closed
// rather than minting an Authority with an invalid grant.
func TestPrincipalAuthorityRejectsEmptyScope(t *testing.T) {
	if _, err := (Principal{UserID: "u1", Scopes: []string{""}}).Authority(); err == nil {
		t.Fatal("empty scope must fail closed")
	}
}
