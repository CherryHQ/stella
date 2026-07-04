package credential

import (
	"errors"
	"fmt"
	"strings"
)

// ErrForbidden is returned by Enforce when a principal may not perform a request.
var ErrForbidden = errors.New("permission denied")

// deniedResources are top-level /api resources that scoped bearers (PAT / OAuth /
// sandbox scoped tokens) may never reach. They are session- or admin-only. They
// are listed EXPLICITLY -- an unlisted, unmapped resource is treated as a
// registration gap (see RequiredScope's registered=false), which a test flags,
// so a new route can never silently default into being reachable or unreachable.
var deniedResources = map[string]bool{
	"admin":              true,
	"auth":               true,
	"builtin":            true,
	"channels":           true,
	"cli-tools":          true,
	"clawhub":            true,
	"embedding-settings": true,
	"groups":             true,
	"inbox":              true,
	"manifest-plugins":   true,
	"models":             true,
	"plugins":            true,
	"provider-types":     true,
	"providers":          true,
	"token-scopes":       true,
	"tools":              true,
}

// Enforce covers kind + scope + the scoped-token agent boundary. Object-level
// ownership (this user owns this task/agent/session) is NOT done here -- it
// remains a per-handler responsibility for PAT/OAuth, exactly as for cookie
// sessions. It runs four explicit layers:
//
//  1. kind policy   -- pat/oauth/scoped are the only accepted bearer kinds.
//  2. method+path -> required scope -- RequiredScope classifies every /api route
//     to a scope; an unregistered route is deny (fail-closed).
//  3. reachability by kind -- sandbox scoped tokens keep the pre-unification
//     agent-bound surface (scopedSandboxReachable); PAT/OAuth use the catalog's
//     PAT-exposable surface (patReachable). The two kinds share ONE scope map but
//     have DIFFERENT reachable surfaces: a sandbox token must not reach top-level
//     external APIs (email/skills/scheduler); a PAT must not reach vault/oauth.
//  4. subject/object boundary -- scoped tokens stay locked to their agentID.
//
// Cookie/OIDC sessions have no Principal and are never passed here.
func Enforce(p *Principal, method, path string) error {
	if p == nil {
		return fmt.Errorf("%w: no principal", ErrForbidden)
	}

	// Scoped bearers are API-only; they may not fetch page routes.
	if !strings.HasPrefix(path, "/api/") {
		return fmt.Errorf("%w: scoped credential may only call /api routes", ErrForbidden)
	}

	// Layer 2: route -> required scope.
	scope, registered := RequiredScope(method, path)
	if !registered {
		return fmt.Errorf("%w: route %s %s has no registered scope", ErrForbidden, method, path)
	}
	if scope == "" {
		return fmt.Errorf("%w: route %s %s is not available to bearer credentials", ErrForbidden, method, path)
	}

	// Layer 3: reachability by kind. Scoped and PAT/OAuth share the scope map but
	// not the surface -- this is what keeps a sandbox token off the top-level
	// user-facing APIs its broad DefaultSandboxScopes would otherwise unlock.
	switch p.Kind {
	case KindScoped:
		if !scopedSandboxReachable(apiSegments(path)) {
			return fmt.Errorf("%w: route %s %s is not available to sandbox scoped tokens", ErrForbidden, method, path)
		}
	case KindPAT, KindOAuth:
		if !patReachable(scope) {
			return fmt.Errorf("%w: route %s %s is not available to this token", ErrForbidden, method, path)
		}
	default:
		return fmt.Errorf("%w: unknown credential kind %q", ErrForbidden, p.Kind)
	}

	if !MatchScope(p.Scopes, scope) {
		return fmt.Errorf("%w: missing scope %q", ErrForbidden, scope)
	}

	// Layer 4: subject/object boundary by kind.
	if p.Kind == KindScoped {
		if err := enforceAgentBoundary(p, path); err != nil {
			return err
		}
	}
	return nil
}

// apiSegments returns the path segments after the leading /api (e.g.
// "/api/agents/x/sessions" -> ["agents","x","sessions"]). Returns nil for
// non-/api paths.
func apiSegments(path string) []string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "api" {
		return nil
	}
	return parts[1:]
}

// scopedSandboxReachable reports whether a sandbox scoped token (stella_scoped_)
// may reach the given /api path (rest is the path after /api). It preserves the
// pre-unification surface of the old scopedTokenAllowsRequest: status, tasks,
// goals, shares, recally, vault, the /api/users/me/oauth subtree, and the
// agent-bound /api/agents/{id}/... subtree.
//
// Top-level external APIs (email, skills, scheduler) are deliberately NOT
// reachable here, even though DefaultSandboxScopes nominally contains
// email:*/skills:*/scheduler:*. Those scopes exist so agent-bound skills and
// scheduler under /api/agents/{id}/... keep working; the top-level user-facing
// mailbox and global-skill management stay off-limits to a sandbox token. PAT/
// OAuth reach those top-level APIs instead (see patReachable).
func scopedSandboxReachable(rest []string) bool {
	if len(rest) == 0 {
		return false
	}
	switch rest[0] {
	case "status", "tasks", "goals", "shares", "recally", "vault", "agents":
		return true
	case "users":
		return len(rest) >= 3 && rest[1] == "me" && rest[2] == "oauth"
	}
	return false
}

// patReachable reports whether a PAT/OAuth token may reach a route requiring the
// given scope. Reachability is the catalog's PAT-exposable policy: vault/oauth
// are sandbox-internal and never reachable by an external token; everything else
// in the catalog is. Object ownership is still checked by the handler.
func patReachable(scope string) bool {
	resource, _, _ := strings.Cut(scope, ":")
	entry, ok := catalogByResource[resource]
	return ok && entry.ExposableToPAT
}

// RequiredScope maps an HTTP method + /api path to the scope needed to reach it.
// It returns:
//
//   - scope:      the required resource:action; "" for a known-but-token-denied
//     route (admin/auth/self-management), which Enforce rejects for every kind.
//   - registered: whether the route was classified at all.
//
// registered=false means a route exists that nobody classified -- Enforce treats
// it as deny (fail-closed), and TestEveryAPIRouteHasRegisteredScope asserts every
// generated /api route (down to its sub-resource) is registered, so the gap
// surfaces at build time, not in production. Classification is at sub-resource
// granularity: an unrecognized /api/agents/{id}/<new> returns registered=false
// rather than silently inheriting the broad agent scope.
func RequiredScope(method, path string) (scope string, registered bool) {
	rest := apiSegments(path)
	if len(rest) == 0 {
		return "", false
	}
	resource := rest[0]

	switch resource {
	case "status":
		return "agent:" + ActionRead, true
	case "agents":
		return agentRouteScope(method, rest[1:])
	case "users":
		return usersRouteScope(method, rest[1:])
	case "tasks", "goals", "shares", "recally", "email", "mcp", "vault", "scheduler", "skills":
		return scopeForMethod(resource, method), true
	}

	if deniedResources[resource] {
		return "", true
	}
	return "", false
}

// agentRouteScope maps /api/agents/{id}/... sub-paths to their scope. Every
// sub-resource is enumerated explicitly: an unknown one returns registered=false
// (fail-closed) instead of defaulting into the broad agent scope, so a newly
// added sub-route cannot silently become reachable. The agent ownership boundary
// ({id} == principal.AgentID for scoped tokens) is handled in enforceAgentBoundary.
func agentRouteScope(method string, sub []string) (scope string, registered bool) {
	// sub is the path after /api/agents. sub[0] is the agent id; the collection
	// (/api/agents) and single agent (/api/agents/{id}) both use the agent scope.
	if len(sub) < 2 {
		return scopeForMethod("agent", method), true
	}
	switch sub[1] {
	case "scheduler":
		return scopeForMethod("scheduler", method), true
	case "skills":
		return scopeForMethod("skills", method), true
	case "sessions", "projects", "users":
		// Conversation history, workspace files, projects, and membership are all
		// gated by the agent scope. NOTE: agent:read here grants read of full
		// session messages and workspace file contents, not just status -- see
		// the agent scope Description in scopes.go.
		return scopeForMethod("agent", method), true
	default:
		return "", false
	}
}

// usersRouteScope exposes only the /api/users/me/oauth subtree to bearer
// credentials; all other /api/users routes (admin, self-management, token
// management) are known but token-denied -- registered with an empty scope.
func usersRouteScope(method string, sub []string) (scope string, registered bool) {
	if len(sub) >= 2 && sub[0] == "me" && sub[1] == "oauth" {
		return scopeForMethod("oauth", method), true
	}
	return "", true
}

// enforceAgentBoundary locks a scoped token to its own agent: any
// /api/agents/{id}/... request must target the token's agentID.
func enforceAgentBoundary(p *Principal, path string) error {
	seg := apiSegments(path)
	if len(seg) >= 2 && seg[0] == "agents" {
		if p.AgentID == "" || seg[1] != p.AgentID {
			return fmt.Errorf("%w: scoped token is bound to a different agent", ErrForbidden)
		}
	}
	return nil
}
