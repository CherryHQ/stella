package authz_test

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// TestIdentityToAuthority proves the legacy runtime Identity maps to the correct
// Authority variant and that a user-less identity fails closed.
func TestIdentityToAuthority(t *testing.T) {
	t.Run("agent-scoped becomes delegated AgentActor", func(t *testing.T) {
		a, err := authz.Identity{UserID: "u1", AgentID: "a1", AgentScoped: true}.ToAuthority()
		if err != nil {
			t.Fatalf("ToAuthority: %v", err)
		}
		if a.Kind() != authz.ActorAgent || a.UserID() != "u1" || a.AgentID() != "a1" {
			t.Fatalf("bad agent authority: %+v", a)
		}
	})

	t.Run("bare user becomes UserActor", func(t *testing.T) {
		a, err := authz.Identity{UserID: "u1"}.ToAuthority()
		if err != nil {
			t.Fatalf("ToAuthority: %v", err)
		}
		if a.Kind() != authz.ActorUser || a.UserID() != "u1" {
			t.Fatalf("bad user authority: %+v", a)
		}
	})

	t.Run("no user id fails closed", func(t *testing.T) {
		if _, err := (authz.Identity{}).ToAuthority(); !errors.Is(err, authz.ErrUnauthenticated) {
			t.Fatalf("empty identity err = %v, want ErrUnauthenticated", err)
		}
		// A group/unauthenticated identity that only has an agent id still has
		// no user owner and must fail.
		if _, err := (authz.Identity{AgentID: "a1", AgentScoped: true}).ToAuthority(); err == nil {
			t.Fatal("identity without a user must not produce an Authority")
		}
	})

	t.Run("scoped but agentless fails closed, never widens to UserActor", func(t *testing.T) {
		// AgentScoped asserts confinement to an agent; a missing agent id makes
		// the confinement unsatisfiable. It must fail, not fall back to an
		// unconfined UserActor that escapes agent scope.
		got, err := (authz.Identity{UserID: "u1", AgentScoped: true, AgentID: ""}).ToAuthority()
		if !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("scoped agentless err = %v, want ErrForbidden", err)
		}
		if got.Valid() || got.Kind() == authz.ActorUser {
			t.Fatalf("scoped agentless must not widen to a UserActor: %+v", got)
		}
	})
}
