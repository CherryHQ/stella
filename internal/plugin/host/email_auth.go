package host

import (
	"context"

	"github.com/CherryHQ/stella/internal/authz"
)

// ResolveEmailUser resolves the user-owned email capability from the trusted
// Authority installed by an entry adapter. It never derives a user from
// request or model input.
func ResolveEmailUser(ctx context.Context) (string, error) {
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok || !authority.Valid() {
		return "", authz.ErrForbidden
	}
	if authority.UserID() == "" {
		return "", authz.ErrUnauthenticated
	}
	return string(authority.UserID()), nil
}

// AuthorizeEmailTool admits an email tool call through the existing tool
// identity and Authority boundary, preserving ToolIdentity's raw errors.
func AuthorizeEmailTool(ctx context.Context, tool, discover string) (context.Context, error) {
	identity, err := authz.ToolIdentity(ctx, tool)
	if err != nil {
		return nil, err
	}
	authority, err := identity.ToAuthority()
	if err != nil {
		return nil, authz.MapToolError(tool, discover, err)
	}
	return authz.WithAuthority(ctx, authority), nil
}
