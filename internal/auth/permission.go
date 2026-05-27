package auth

import "context"

// PermissionChecker decides whether a principal may perform an action on a resource.
// Implementations should be stateless and fast — they are called on every request.
type PermissionChecker interface {
	Can(ctx context.Context, principal Principal, action Action, resource Resource) bool
}

// RoleBasedChecker is the first-version implementation: admin can perform any
// action on resources within their own org; members can only act on their own
// resources.
//
// Admin scope is intentionally limited to org-level operations (managing members,
// viewing the member list, etc.). For business resources (agents, sessions, etc.)
// both admin and member are treated identically — access is filtered by user_id.
// Full resource-level org isolation is deferred to a later PR.
type RoleBasedChecker struct{}

// Can returns true when the principal is allowed to perform action on resource.
// Admin permission requires principal.OrgID == resource.OwnerOrgID; without that
// match, admin has no extra rights over a member.
func (c *RoleBasedChecker) Can(ctx context.Context, p Principal, action Action, resource Resource) bool {
	// Owner always has full access to their own resources.
	if resource.OwnerID == p.UserID {
		return true
	}

	// Org-scoped admin: extra privileges only within the same org.
	if p.IsAdmin() && p.OrgID != "" && p.OrgID == resource.OwnerOrgID {
		// Admin can manage org-level operations (user management, member list).
		// Business resources are still restricted to owner access only.
		switch resource.Type {
		case ResourceUser, ResourceUserData:
			return true
		}
	}

	return false
}
