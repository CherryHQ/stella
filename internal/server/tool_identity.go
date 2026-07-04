package server

import "github.com/CherryHQ/stella/internal/toolctx"

func toolIdentity(info *AuthInfo) toolctx.Identity {
	if info == nil {
		return toolctx.Identity{}
	}
	agentID, _, scoped := info.scopedBoundary()
	return toolctx.Identity{UserID: info.UserID, AgentID: agentID, AgentScoped: scoped}
}
