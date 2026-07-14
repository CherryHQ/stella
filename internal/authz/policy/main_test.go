package policy

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// userAuthority builds a user Authority with the user or admin role.
func userAuthority(t *testing.T, userID string, admin bool) authz.Authority {
	t.Helper()
	role := authz.RoleUser
	if admin {
		role = authz.RoleAdmin
	}
	roles, err := authz.NewRoleSet(role)
	if err != nil {
		t.Fatalf("new role set: %v", err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(userID), roles, authz.GrantSet{})
	if err != nil {
		t.Fatalf("new user authority: %v", err)
	}
	return authority
}

func mustAgentRead(t *testing.T, agentID, ownerID, scope string, assigned bool) authz.Request {
	t.Helper()
	request, err := AgentReadRequest(agentID, ownerID, AgentFacts{Scope: scope, Assigned: assigned})
	if err != nil {
		t.Fatalf("agent read request: %v", err)
	}
	return request
}
