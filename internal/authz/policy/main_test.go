package policy

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// userAuthority builds a user Authority with the user or admin role.
func userAuthority(t *testing.T, userID string, admin bool) authz.Authority {
	t.Helper()
	role := authz.RoleUser
	if admin {
		role = authz.RoleAdmin
	}
	rs, err := authz.NewRoleSet(role)
	if err != nil {
		t.Fatalf("new role set: %v", err)
	}
	a, err := authz.NewUserAuthority(authz.UserID(userID), rs, authz.GrantSet{})
	if err != nil {
		t.Fatalf("new user authority: %v", err)
	}
	return a
}

// mustAgentRead builds an agent read request or fails the test.
func mustAgentRead(t *testing.T, agentID, ownerID, scope string, assigned bool) authz.Request {
	t.Helper()
	req, err := AgentReadRequest(agentID, ownerID, AgentFacts{Scope: scope, Assigned: assigned, Creator: ownerID, Status: "enabled"})
	if err != nil {
		t.Fatalf("agent read request: %v", err)
	}
	return req
}
