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
