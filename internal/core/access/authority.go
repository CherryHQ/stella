package access

import "github.com/CherryHQ/stella/internal/authz"

// WorkerAgentAuthority reconstructs the sole authority shape permitted for a
// persisted user-owned worker: its durable owner and exact executor agent.
func WorkerAgentAuthority(ownerID, agentID string) (authz.Authority, error) {
	return authz.NewAgentAuthority(authz.UserID(ownerID), authz.AgentID(agentID))
}

// SystemAgentAuthority mints the named maintenance authority used for an agent
// invocation. System execution is a named component; it never inherits an admin
// or user identity.
func SystemAgentAuthority(component string) (authz.Authority, error) {
	return authz.NewSystemAuthority(authz.Component(component))
}

// GroupAgentAuthority mints an exact group/agent authority for one group turn. A
// triggering user is intentionally absent from this capability, so a group turn
// can never reach user-private access.
func GroupAgentAuthority(groupID, agentID string) (authz.Authority, error) {
	return authz.NewGroupAgentAuthority(authz.GroupID(groupID), authz.AgentID(agentID))
}
