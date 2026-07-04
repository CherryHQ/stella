package server

import "github.com/CherryHQ/stella/internal/authz"

func toolIdentity(info *AuthInfo) authz.Identity {
	if info == nil {
		return authz.Identity{}
	}
	agentID, _, scoped := info.scopedBoundary()
	return authz.Identity{UserID: info.UserID, AgentID: agentID, AgentScoped: scoped}
}
