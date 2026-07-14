package access

import "github.com/CherryHQ/stella/internal/authz"

// WorkerAgentAuthority reconstructs the sole authority shape permitted for a
// persisted user-owned worker: its durable owner and exact executor agent.
func WorkerAgentAuthority(ownerID, agentID string) (authz.Authority, error) {
	return authz.NewAgentAuthority(authz.UserID(ownerID), authz.AgentID(agentID), authz.RoleSet{}, authz.GrantSet{})
}

// SystemAgentAuthority mints the named maintenance authority used for an agent
// invocation. System execution is grant-based; it never inherits an admin role.
func SystemAgentAuthority(component string) (authz.Authority, error) {
	grant, err := authz.SystemGrant("agent.use")
	if err != nil {
		return authz.Authority{}, err
	}
	grants, err := authz.NewGrantSet(grant)
	if err != nil {
		return authz.Authority{}, err
	}
	return authz.NewSystemAuthority(authz.Component(component), grants)
}

// GroupAgentAuthority mints a roleless, exact group/member authority for one
// group turn. A triggering user is intentionally absent from this capability.
func GroupAgentAuthority(groupID, agentID string) (authz.Authority, error) {
	grant, err := authz.GroupToolGrant("agent.use")
	if err != nil {
		return authz.Authority{}, err
	}
	grants, err := authz.NewGrantSet(grant)
	if err != nil {
		return authz.Authority{}, err
	}
	return authz.NewGroupAgentAuthority(authz.GroupID(groupID), authz.AgentID(agentID), authz.RoleSet{}, grants)
}
