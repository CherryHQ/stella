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

// MapError maps an authorization failure for a union tool, whose single name
// covers several actions and whose recovery advice is therefore an action on
// itself.
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

// MapToolError maps an authorization failure for a split tool. tool is the name
// the model called; discover is the sibling that lists what this agent can
// reach, so the recovery advice names a tool that exists rather than the
// union-era "action=list". Pass "" when the family has no list action.
func MapToolError(tool, discover string, err error) error {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return fmt.Errorf("this session has no user identity — %s is unavailable here", tool)
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%s: not found — check the id%s", tool, withDiscover(" with %s", discover))
	case errors.Is(err, ErrForbidden):
		return fmt.Errorf("%s: access denied%s", tool, withDiscover(" — use %s to see what this agent can reach", discover))
	default:
		return err
	}
}

func withDiscover(format, discover string) string {
	if discover == "" {
		return ""
	}
	return fmt.Sprintf(format, discover)
}
