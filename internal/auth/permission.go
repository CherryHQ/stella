package auth

import "context"

// PermissionChecker decides whether a principal may perform an action on a resource.
// Implementations should be stateless and fast — they are called on every request.
type PermissionChecker interface {
	Can(ctx context.Context, principal Principal, action Action, resource Resource) bool
}

// RoleBasedChecker is the single-tenant implementation: admin can do anything,
// regular users can only act on their own resources.
type RoleBasedChecker struct{}

// Can returns true when the principal is allowed to perform action on resource.
func (c *RoleBasedChecker) Can(ctx context.Context, p Principal, action Action, resource Resource) bool {
	// Owner always has full access to their own resources.
	if resource.OwnerID == p.UserID {
		return true
	}

	// Admin has full access in single-tenant mode.
	if p.IsAdmin() {
		return true
	}

	return false
}
