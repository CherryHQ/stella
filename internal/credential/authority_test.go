package credential

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestPrincipalAuthority proves a resolved bearer principal maps to an ordinary
// UserActor and that its API scopes are deliberately NOT copied into the
// Authority — scope enforcement stays at credential.Enforce.
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
	if a.Kind() != authz.ActorUser || a.UserID() != "u1" {
		t.Fatalf("bad actor: %+v", a)
	}
	if a.IsAdmin() {
		t.Error("non-admin principal must not be admin")
	}
}

// TestPrincipalAuthorityAdmin proves the admin flag maps straight through.
func TestPrincipalAuthorityAdmin(t *testing.T) {
	a, err := Principal{UserID: "root", IsAdmin: true}.Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if !a.IsAdmin() {
		t.Error("admin principal must be admin")
	}
}

// TestScopeEnforcementStaysAtEnforce proves OAuth route scopes are not
// represented in Authority: a principal scoped only to goals:read still mints a
// valid Authority, while Enforce permits the mapped route and denies an
// unscoped one.
func TestScopeEnforcementStaysAtEnforce(t *testing.T) {
	p := &Principal{Kind: KindOAuth, UserID: "u1", Scopes: []string{"goals:read"}}
	if _, err := p.Authority(); err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if err := Enforce(p, "GET", "/api/goals"); err != nil {
		t.Fatalf("Enforce mapped scope = %v, want nil", err)
	}
	if err := Enforce(p, "POST", "/api/goals"); err == nil {
		t.Fatal("Enforce must deny a route the principal is not scoped for")
	}
}
