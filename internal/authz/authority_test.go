package authz_test

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// must unwraps a grant constructor, panicking on error. It takes the two-value
// constructor result directly so it composes as must(authz.XGrant(...)).
func must(g authz.Grant, err error) authz.Grant {
	if err != nil {
		panic(err)
	}
	return g
}

func mustGrantSet(t *testing.T, grants ...authz.Grant) authz.GrantSet {
	t.Helper()
	s, err := authz.NewGrantSet(grants...)
	if err != nil {
		t.Fatalf("NewGrantSet: %v", err)
	}
	return s
}

func mustRoleSet(t *testing.T, roles ...authz.Role) authz.RoleSet {
	t.Helper()
	s, err := authz.NewRoleSet(roles...)
	if err != nil {
		t.Fatalf("NewRoleSet: %v", err)
	}
	return s
}

// TestZeroValuesFailClosed proves the zero Actor/Authority are invalid.
func TestZeroValuesFailClosed(t *testing.T) {
	if (authz.Actor{}).Valid() {
		t.Error("zero Actor must be invalid")
	}
	if (authz.Actor{}).Kind() != authz.ActorInvalid {
		t.Error("zero Actor kind must be ActorInvalid")
	}
	if (authz.Authority{}).Valid() {
		t.Error("zero Authority must be invalid")
	}
	if (authz.Authority{}).Kind() != authz.ActorInvalid {
		t.Error("zero Authority kind must be ActorInvalid")
	}
	// A zero Decision denies.
	if (authz.Decision{}).Allowed() {
		t.Error("zero Decision must deny")
	}
}

// TestEveryActorKindConstructs exercises each variant's happy path and the
// identity fields it must and must not carry.
func TestEveryActorKindConstructs(t *testing.T) {
	t.Run("user", func(t *testing.T) {
		a, err := authz.NewUserAuthority("u1", mustRoleSet(t, authz.RoleUser), authz.GrantSet{})
		if err != nil {
			t.Fatalf("NewUserAuthority: %v", err)
		}
		if a.Kind() != authz.ActorUser || a.Actor().UserID() != "u1" {
			t.Fatalf("bad user actor: %+v", a.Actor())
		}
		if a.Actor().AgentID() != "" || a.Actor().GroupID() != "" || a.Actor().Component() != "" {
			t.Fatal("user actor leaked a foreign id")
		}
	})

	t.Run("agent durable reconstruction", func(t *testing.T) {
		// Owner + executor is exactly the shape a durable worker reconstructs
		// from a persisted job/goal row.
		a, err := authz.NewAgentAuthority("owner", "executor", authz.RoleSet{}, authz.GrantSet{})
		if err != nil {
			t.Fatalf("NewAgentAuthority: %v", err)
		}
		if a.Kind() != authz.ActorAgent {
			t.Fatalf("kind = %v", a.Kind())
		}
		if a.Actor().UserID() != "owner" || a.Actor().AgentID() != "executor" {
			t.Fatalf("owner/executor not reconstructed: %+v", a.Actor())
		}
		if a.Actor().GroupID() != "" {
			t.Fatal("agent actor leaked a group id")
		}
	})

	t.Run("group", func(t *testing.T) {
		a, err := authz.NewGroupAuthority("g1", authz.RoleSet{}, authz.GrantSet{})
		if err != nil {
			t.Fatalf("NewGroupAuthority: %v", err)
		}
		if a.Kind() != authz.ActorGroup || a.Actor().GroupID() != "g1" {
			t.Fatalf("bad group actor: %+v", a.Actor())
		}
		if a.Actor().UserID() != "" {
			t.Fatal("group actor must not carry a user id")
		}
	})

	t.Run("group agent", func(t *testing.T) {
		a, err := authz.NewGroupAgentAuthority("g1", "agent", authz.RoleSet{}, authz.GrantSet{})
		if err != nil {
			t.Fatalf("NewGroupAgentAuthority: %v", err)
		}
		if a.Kind() != authz.ActorGroupAgent || a.Actor().GroupID() != "g1" || a.Actor().AgentID() != "agent" {
			t.Fatalf("bad group-agent actor: %+v", a.Actor())
		}
		if a.Actor().UserID() != "" {
			t.Fatal("group-agent actor must not carry a user id (no private-user capability)")
		}
	})

	t.Run("system named", func(t *testing.T) {
		a, err := authz.NewSystemAuthority("embedding-backfill",
			mustGrantSet(t, must(authz.SystemGrant("embedding-backfill"))))
		if err != nil {
			t.Fatalf("NewSystemAuthority: %v", err)
		}
		if a.Kind() != authz.ActorSystem || a.Actor().Component() != "embedding-backfill" {
			t.Fatalf("bad system actor: %+v", a.Actor())
		}
	})
}

// TestConstructorRejectsMissingIDs proves each variant fails closed without its
// required identity.
func TestConstructorRejectsMissingIDs(t *testing.T) {
	if _, err := authz.NewUserAuthority("", authz.RoleSet{}, authz.GrantSet{}); err == nil {
		t.Error("user without id must fail")
	}
	if _, err := authz.NewAgentAuthority("", "executor", authz.RoleSet{}, authz.GrantSet{}); err == nil {
		t.Error("agent without owner must fail")
	}
	if _, err := authz.NewAgentAuthority("owner", "", authz.RoleSet{}, authz.GrantSet{}); err == nil {
		t.Error("agent without executor must fail")
	}
	if _, err := authz.NewGroupAuthority("", authz.RoleSet{}, authz.GrantSet{}); err == nil {
		t.Error("group without id must fail")
	}
	if _, err := authz.NewGroupAgentAuthority("g1", "", authz.RoleSet{}, authz.GrantSet{}); err == nil {
		t.Error("group-agent without agent must fail")
	}
	if _, err := authz.NewGroupAgentAuthority("", "a", authz.RoleSet{}, authz.GrantSet{}); err == nil {
		t.Error("group-agent without group must fail")
	}
	if _, err := authz.NewSystemAuthority("", mustGrantSet(t, must(authz.SystemGrant("x")))); err == nil {
		t.Error("unnamed system actor must fail")
	}
}

// TestSystemActorNeedsNamedGrant proves there is no omnipotent implicit system
// actor: a name alone is not enough, and its grants must be system grants.
func TestSystemActorNeedsNamedGrant(t *testing.T) {
	if _, err := authz.NewSystemAuthority("maint", authz.GrantSet{}); err == nil {
		t.Error("system actor with no grant must fail")
	}
	// A public tool grant is not a system grant.
	if _, err := authz.NewSystemAuthority("maint",
		mustGrantSet(t, must(authz.PublicToolGrant("clock")))); err == nil {
		t.Error("system actor holding a non-system grant must fail")
	}
}

// TestGroupActorsRejectRoles proves group ingress and group turns carry no
// user/admin role: any non-empty RoleSet is rejected, and the zero role set
// works.
func TestGroupActorsRejectRoles(t *testing.T) {
	for _, role := range []authz.Role{authz.RoleUser, authz.RoleAdmin} {
		rs := mustRoleSet(t, role)
		t.Run("group rejects "+role.String(), func(t *testing.T) {
			if _, err := authz.NewGroupAuthority("g", rs, authz.GrantSet{}); !errors.Is(err, authz.ErrRolesNotAllowed) {
				t.Fatalf("group with role %s err = %v, want ErrRolesNotAllowed", role, err)
			}
		})
		t.Run("group-agent rejects "+role.String(), func(t *testing.T) {
			if _, err := authz.NewGroupAgentAuthority("g", "a", rs, authz.GrantSet{}); !errors.Is(err, authz.ErrRolesNotAllowed) {
				t.Fatalf("group-agent with role %s err = %v, want ErrRolesNotAllowed", role, err)
			}
		})
	}

	// Zero roles (both the empty RoleSet{} and an explicitly-empty NewRoleSet)
	// construct successfully.
	if _, err := authz.NewGroupAuthority("g", authz.RoleSet{}, authz.GrantSet{}); err != nil {
		t.Fatalf("group with zero roles must succeed: %v", err)
	}
	if _, err := authz.NewGroupAgentAuthority("g", "a", mustRoleSet(t), authz.GrantSet{}); err != nil {
		t.Fatalf("group-agent with empty NewRoleSet must succeed: %v", err)
	}
}

// TestGroupCapabilityMatrix is the core security property: a group / group-agent
// actor can hold only public or group-scoped grants and rejects every
// user-private grant.
func TestGroupCapabilityMatrix(t *testing.T) {
	userPrivate := []struct {
		name  string
		grant authz.Grant
	}{
		{"agent tool", must(authz.AgentToolGrant("memory"))},
		{"entry scope", must(authz.EntryScopeGrant("goals:read"))},
		{"channel binding", must(authz.ChannelBindingGrant("chan-1"))},
	}
	for _, tc := range userPrivate {
		t.Run("group-agent rejects "+tc.name, func(t *testing.T) {
			_, err := authz.NewGroupAgentAuthority("g", "a", authz.RoleSet{}, mustGrantSet(t, tc.grant))
			if err == nil {
				t.Fatalf("group-agent must reject %s", tc.name)
			}
		})
		t.Run("group rejects "+tc.name, func(t *testing.T) {
			_, err := authz.NewGroupAuthority("g", authz.RoleSet{}, mustGrantSet(t, tc.grant))
			if err == nil {
				t.Fatalf("group must reject %s", tc.name)
			}
		})
	}

	allowed := []struct {
		name  string
		grant authz.Grant
	}{
		{"public tool", must(authz.PublicToolGrant("clock"))},
		{"group tool", must(authz.GroupToolGrant("group-search"))},
	}
	for _, tc := range allowed {
		t.Run("group-agent allows "+tc.name, func(t *testing.T) {
			if _, err := authz.NewGroupAgentAuthority("g", "a", authz.RoleSet{}, mustGrantSet(t, tc.grant)); err != nil {
				t.Fatalf("group-agent must allow %s: %v", tc.name, err)
			}
		})
	}
}

// TestUserAndAgentCapabilityMatrix proves user/agent actors hold public and
// user-private grants but reject group and system grants.
func TestUserAndAgentCapabilityMatrix(t *testing.T) {
	ok := []authz.Grant{
		must(authz.PublicToolGrant("clock")),
		must(authz.AgentToolGrant("memory")),
		must(authz.EntryScopeGrant("goals:write")),
	}
	if _, err := authz.NewUserAuthority("u", authz.RoleSet{}, mustGrantSet(t, ok...)); err != nil {
		t.Fatalf("user must allow public/user-private grants: %v", err)
	}
	if _, err := authz.NewAgentAuthority("u", "a", authz.RoleSet{}, mustGrantSet(t, ok...)); err != nil {
		t.Fatalf("agent must allow public/user-private grants: %v", err)
	}

	bad := []authz.Grant{
		must(authz.GroupToolGrant("group-search")),
		must(authz.SystemGrant("maint")),
	}
	for _, g := range bad {
		if _, err := authz.NewUserAuthority("u", authz.RoleSet{}, mustGrantSet(t, g)); err == nil {
			t.Errorf("user must reject %s grant", g.Kind())
		}
		if _, err := authz.NewAgentAuthority("u", "a", authz.RoleSet{}, mustGrantSet(t, g)); err == nil {
			t.Errorf("agent must reject %s grant", g.Kind())
		}
	}
}

// TestDefensiveCopyOnInput proves mutating the caller's slice after construction
// does not change the published Authority.
func TestDefensiveCopyOnInput(t *testing.T) {
	grants := []authz.Grant{must(authz.AgentToolGrant("memory"))}
	roles := []authz.Role{authz.RoleUser}

	gs, err := authz.NewGrantSet(grants...)
	if err != nil {
		t.Fatalf("NewGrantSet: %v", err)
	}
	rs, err := authz.NewRoleSet(roles...)
	if err != nil {
		t.Fatalf("NewRoleSet: %v", err)
	}
	a, err := authz.NewUserAuthority("u", rs, gs)
	if err != nil {
		t.Fatalf("NewUserAuthority: %v", err)
	}

	// Mutate the caller's backing arrays.
	grants[0] = must(authz.AgentToolGrant("EVIL"))
	roles[0] = authz.RoleAdmin

	if got := a.Grants(); len(got) != 1 || got[0].Key() != "memory" {
		t.Fatalf("grant input mutation leaked into Authority: %+v", got)
	}
	if a.IsAdmin() {
		t.Fatal("role input mutation leaked into Authority")
	}
}

// TestDefensiveCopyOnAccessor proves mutating an accessor's return value does
// not change the Authority's internal state.
func TestDefensiveCopyOnAccessor(t *testing.T) {
	a, err := authz.NewUserAuthority("u",
		mustRoleSet(t, authz.RoleUser),
		mustGrantSet(t, must(authz.AgentToolGrant("memory"))))
	if err != nil {
		t.Fatalf("NewUserAuthority: %v", err)
	}

	got := a.Grants()
	got[0] = must(authz.AgentToolGrant("EVIL"))
	gotRoles := a.Roles()
	if len(gotRoles) > 0 {
		gotRoles[0] = authz.RoleAdmin
	}

	if again := a.Grants(); again[0].Key() != "memory" {
		t.Fatalf("accessor mutation leaked back into Authority: %+v", again)
	}
	if a.IsAdmin() {
		t.Fatal("role accessor mutation leaked back into Authority")
	}
}

// TestGrantSetDedupAndValidation proves duplicate grants collapse and an invalid
// grant is rejected.
func TestGrantSetDedupAndValidation(t *testing.T) {
	g := must(authz.AgentToolGrant("memory"))
	s, err := authz.NewGrantSet(g, g, g)
	if err != nil {
		t.Fatalf("NewGrantSet: %v", err)
	}
	if s.Len() != 1 {
		t.Fatalf("duplicates not collapsed: len=%d", s.Len())
	}
	if _, err := authz.NewGrantSet(authz.Grant{}); err == nil {
		t.Fatal("invalid (zero) grant must be rejected")
	}
	if _, err := authz.EntryScopeGrant(""); err == nil {
		t.Fatal("empty grant key must be rejected")
	}
}

// TestRoleSetValidation proves unknown roles fail closed.
func TestRoleSetValidation(t *testing.T) {
	if _, err := authz.NewRoleSet(authz.RoleInvalid); err == nil {
		t.Fatal("invalid role must be rejected")
	}
	if _, err := authz.NewRoleSet(authz.Role(200)); err == nil {
		t.Fatal("out-of-range role must be rejected")
	}
	s := mustRoleSet(t, authz.RoleAdmin, authz.RoleAdmin, authz.RoleUser)
	if s.Len() != 2 {
		t.Fatalf("role duplicates not collapsed: %d", s.Len())
	}
}
