package auth

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestSubjectAuthority proves a session subject maps to a UserActor with the
// admin flag resolved from its roles, and that assigned AgentIDs are
// intentionally not part of the identity.
func TestSubjectAuthority(t *testing.T) {
	s := Subject{UserID: "u1", Roles: []string{RoleAdmin}, AgentIDs: []string{"a1", "a2"}}
	a, err := s.Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if a.Kind() != authz.ActorUser || a.UserID() != "u1" {
		t.Fatalf("bad actor: %+v", a)
	}
	if !a.IsAdmin() {
		t.Error("admin subject must be admin")
	}
	// AgentIDs are policy attributes, not identity; a plain user subject carries no
	// channel binding.
	if a.AgentID() != "" || a.ChannelBindingID() != "" {
		t.Errorf("subject adapter must not carry agent id or channel binding: %+v", a)
	}
}

// TestSubjectAuthorityDefaultsToUser proves a subject with no roles is an
// ordinary (non-admin) user rather than a failure.
func TestSubjectAuthorityDefaultsToUser(t *testing.T) {
	a, err := Subject{UserID: "u1"}.Authority()
	if err != nil {
		t.Fatalf("Authority: %v", err)
	}
	if a.Kind() != authz.ActorUser || a.IsAdmin() {
		t.Errorf("role-less subject must be an ordinary user: %+v", a)
	}
}

// TestSubjectAuthorityRejectsUnknownRole proves an unrecognised role string
// fails closed rather than being dropped.
func TestSubjectAuthorityRejectsUnknownRole(t *testing.T) {
	if _, err := (Subject{UserID: "u1", Roles: []string{"superuser"}}).Authority(); err == nil {
		t.Fatal("unknown role must fail closed")
	}
}

// TestChannelAuthorityCarriesExactBinding proves the dedicated-channel adapter
// mints a UserActor bound to the exact channel id.
func TestChannelAuthorityCarriesExactBinding(t *testing.T) {
	a, err := Subject{UserID: "u1"}.ChannelAuthority("chan-1")
	if err != nil {
		t.Fatalf("ChannelAuthority: %v", err)
	}
	if a.Kind() != authz.ActorUser || a.UserID() != "u1" || a.ChannelBindingID() != "chan-1" {
		t.Fatalf("bad channel authority: %+v", a)
	}
}
