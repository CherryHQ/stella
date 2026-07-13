package auth

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestSubjectAuthority proves a session subject maps to a UserActor with mapped
// roles, and that assigned AgentIDs are intentionally not part of the identity.
func TestSubjectAuthority(t *testing.T) {
	s := Subject{UserID: "u1", Roles: []string{RoleAdmin}, AgentIDs: []string{"a1", "a2"}}
	a, err := s.Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if a.Kind() != authz.ActorUser || a.Actor().UserID() != "u1" {
		t.Fatalf("bad actor: %+v", a.Actor())
	}
	if !a.IsAdmin() {
		t.Error("admin subject must hold admin role")
	}
	// AgentIDs are policy attributes, not identity: no grants are minted.
	if len(a.Grants()) != 0 {
		t.Errorf("subject adapter must not mint grants, got %d", len(a.Grants()))
	}
}

// TestSubjectAuthorityDefaultsToUser proves a subject with no roles still gets
// the user role (never an empty role set that silently denies everything).
func TestSubjectAuthorityDefaultsToUser(t *testing.T) {
	a, err := Subject{UserID: "u1"}.Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if !a.HasRole(authz.RoleUser) {
		t.Error("role-less subject must default to the user role")
	}
}

// TestSubjectAuthorityRejectsUnknownRole proves an unrecognised role string
// fails closed rather than being dropped.
func TestSubjectAuthorityRejectsUnknownRole(t *testing.T) {
	if _, err := (Subject{UserID: "u1", Roles: []string{"superuser"}}).Authority(); err == nil {
		t.Fatal("unknown role must fail closed")
	}
}
