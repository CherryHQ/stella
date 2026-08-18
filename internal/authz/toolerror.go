package authz

import (
	"context"
	"errors"
	"fmt"
)

func ToolIdentity(ctx context.Context, tool string) (Identity, error) {
	ident, err := FromContext(ctx)
	if err != nil {
		return Identity{}, fmt.Errorf("this session has no user identity — %s tools are unavailable here", tool)
	}
	if ident.AgentID == "" {
		return Identity{}, fmt.Errorf("this session has no agent identity — %s tools are unavailable here", tool)
	}
	return ident, nil
}

// ToolAuthority reconstructs the trusted runtime authority for tools that have
// an explicitly authorized group-scoped mode. It rejects mixed user/group
// contexts rather than silently choosing the more privileged identity.
func ToolAuthority(ctx context.Context, tool string) (Authority, error) {
	agentID := AgentIDFromContext(ctx)
	if agentID == "" {
		return Authority{}, fmt.Errorf("this session has no agent identity — %s tools are unavailable here", tool)
	}
	if groupID := GroupIDFromContext(ctx); groupID != "" {
		if UserIDFromContext(ctx) != "" || GuestIDFromContext(ctx) != "" {
			return Authority{}, fmt.Errorf("this session has conflicting identities — %s tools are unavailable here", tool)
		}
		authority, err := NewGroupAgentAuthority(GroupID(groupID), AgentID(agentID))
		if err != nil {
			return Authority{}, fmt.Errorf("this session has invalid group identity — %s tools are unavailable here", tool)
		}
		return authority, nil
	}
	ident, err := ToolIdentity(ctx, tool)
	if err != nil {
		return Authority{}, err
	}
	authority, err := ident.ToAuthority()
	if err != nil {
		return Authority{}, MapError(tool, err)
	}
	return authority, nil
}

func MapError(tool string, err error) error {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return fmt.Errorf("this session has no user identity — %s tools are unavailable here", tool)
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%s not found — check the id with action=list", tool)
	case errors.Is(err, ErrForbidden):
		return fmt.Errorf("%s access denied — use action=list to see resources available to this agent", tool)
	default:
		return err
	}
}
