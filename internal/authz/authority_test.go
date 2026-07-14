package authz_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestZeroValueFailsClosed proves the zero Authority is invalid and fails closed.
func TestZeroValueFailsClosed(t *testing.T) {
	if (authz.Authority{}).Valid() {
		t.Error("zero Authority must be invalid")
	}
	if (authz.Authority{}).Kind() != authz.ActorInvalid {
		t.Error("zero Authority kind must be ActorInvalid")
	}
}

// TestConstructorsRejectMissingIDs proves each variant fails closed without its
// required identity, so no constructor can mint an invalid Authority.
func TestConstructorsRejectMissingIDs(t *testing.T) {
	if _, err := authz.NewUserAuthority("", false); err == nil {
		t.Error("user without id must fail")
	}
	if _, err := authz.NewChannelAuthority("", false, "chan"); err == nil {
		t.Error("channel authority without user must fail")
	}
	if _, err := authz.NewChannelAuthority("u", false, ""); err == nil {
		t.Error("channel authority without binding must fail")
	}
	if _, err := authz.NewAgentAuthority("", "exec"); err == nil {
		t.Error("agent without owner must fail")
	}
	if _, err := authz.NewAgentAuthority("owner", ""); err == nil {
		t.Error("agent without executor must fail")
	}
	if _, err := authz.NewGroupAgentAuthority("", "a"); err == nil {
		t.Error("group-agent without group must fail")
	}
	if _, err := authz.NewGroupAgentAuthority("g", ""); err == nil {
		t.Error("group-agent without agent must fail")
	}
	if _, err := authz.NewSystemAuthority(""); err == nil {
		t.Error("unnamed system actor must fail")
	}
}

// TestUserAuthorityShape proves an ordinary user carries only its user id, an
// admin flag when requested, and no foreign identity.
func TestUserAuthorityShape(t *testing.T) {
	a, err := authz.NewUserAuthority("u1", false)
	if err != nil {
		t.Fatalf("NewUserAuthority: %v", err)
	}
	if !a.Valid() || a.Kind() != authz.ActorUser || a.UserID() != "u1" {
		t.Fatalf("bad user actor: %+v", a)
	}
	if a.IsAdmin() {
		t.Error("ordinary user must not be admin")
	}
	if a.AgentID() != "" || a.GroupID() != "" || a.Component() != "" || a.ChannelBindingID() != "" {
		t.Fatalf("user actor leaked a foreign field: %+v", a)
	}

	admin, err := authz.NewUserAuthority("root", true)
	if err != nil {
		t.Fatalf("NewUserAuthority admin: %v", err)
	}
	if !admin.IsAdmin() {
		t.Error("admin user must report IsAdmin")
	}
}

// TestChannelAuthorityHoldsExactBinding proves a dedicated-channel authority is a
// user carrying exactly one channel binding.
func TestChannelAuthorityHoldsExactBinding(t *testing.T) {
	a, err := authz.NewChannelAuthority("u1", false, "chan-1")
	if err != nil {
		t.Fatalf("NewChannelAuthority: %v", err)
	}
	if !a.Valid() || a.Kind() != authz.ActorUser || a.UserID() != "u1" || a.ChannelBindingID() != "chan-1" {
		t.Fatalf("bad channel actor: %+v", a)
	}
	if a.AgentID() != "" || a.GroupID() != "" || a.Component() != "" {
		t.Fatalf("channel actor leaked a foreign field: %+v", a)
	}
}

// TestAgentAuthorityDurableReconstruction proves the owner+executor shape a
// durable worker rebuilds is a valid AgentActor with no admin, group, or binding.
func TestAgentAuthorityDurableReconstruction(t *testing.T) {
	a, err := authz.NewAgentAuthority("owner", "executor")
	if err != nil {
		t.Fatalf("NewAgentAuthority: %v", err)
	}
	if !a.Valid() || a.Kind() != authz.ActorAgent || a.UserID() != "owner" || a.AgentID() != "executor" {
		t.Fatalf("owner/executor not reconstructed: %+v", a)
	}
	if a.IsAdmin() || a.GroupID() != "" || a.Component() != "" || a.ChannelBindingID() != "" {
		t.Fatalf("agent actor leaked a foreign field: %+v", a)
	}
}

// TestGroupAgentHasNoUserIdentity is the core group isolation property: a group
// turn carries exactly one group and one agent and never a user, admin, or
// binding, so user-private access is structurally impossible.
func TestGroupAgentHasNoUserIdentity(t *testing.T) {
	a, err := authz.NewGroupAgentAuthority("g1", "agent")
	if err != nil {
		t.Fatalf("NewGroupAgentAuthority: %v", err)
	}
	if !a.Valid() || a.Kind() != authz.ActorGroupAgent || a.GroupID() != "g1" || a.AgentID() != "agent" {
		t.Fatalf("bad group-agent actor: %+v", a)
	}
	if a.UserID() != "" || a.IsAdmin() || a.Component() != "" || a.ChannelBindingID() != "" {
		t.Fatalf("group-agent actor must carry no user/admin/binding: %+v", a)
	}
}

// TestSystemAuthorityIsNamedWithoutUser proves a system actor is named
// maintenance work with no user or admin identity.
func TestSystemAuthorityIsNamedWithoutUser(t *testing.T) {
	a, err := authz.NewSystemAuthority("embedding-backfill")
	if err != nil {
		t.Fatalf("NewSystemAuthority: %v", err)
	}
	if !a.Valid() || a.Kind() != authz.ActorSystem || a.Component() != "embedding-backfill" {
		t.Fatalf("bad system actor: %+v", a)
	}
	if a.UserID() != "" || a.AgentID() != "" || a.GroupID() != "" || a.IsAdmin() {
		t.Fatalf("system actor must carry no user/agent/group/admin identity: %+v", a)
	}
}
