package credential

import (
	"errors"
	"fmt"
	"strings"
)

// ErrForbidden is returned by Enforce when a principal may not perform a request.
var ErrForbidden = errors.New("permission denied")

// deniedResources are top-level /api resources that bearer credentials (PAT /
// OAuth) may never reach. They are session- or admin-only. They
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
	"knowledge-files":    true,
	"manifest-plugins":   true,
	"models":             true,
	"plugins":            true,
	"provider-types":     true,
	"providers":          true,
	"token-scopes":       true,
	"tools":              true,
}

// Enforce covers bearer kind + scope. Object-level ownership (this user owns
// this task/agent/session) is NOT done here -- it remains a per-handler
// responsibility for PAT/OAuth, exactly as for cookie sessions. It runs three
// explicit layers:
//
//  1. kind policy   -- pat/oauth are the only accepted bearer kinds.
//  2. method+path -> required scope -- RequiredScope classifies every /api route
//     to a scope; an unregistered route is deny (fail-closed).
//  3. reachability by kind -- PAT/OAuth use the catalog's PAT-exposable surface
//     (patReachable).
//
// Cookie/OIDC sessions have no Principal and are never passed here.
func Enforce(p *Principal, method, path string) error {
	if p == nil {
		return fmt.Errorf("%w: no principal", ErrForbidden)
	}

	// Bearer credentials are API-only; they may not fetch page routes.
	// One deliberate exception lives outside this gate: POST /webhooks/{id} is
	// auth-exempt in the server middleware and re-checks kind + ScopeAgentWrite
	// itself (internal/server/webhook_ingress.go). Account for it when changing
	// PAT policy here.
	if !strings.HasPrefix(path, "/api/") {
		return fmt.Errorf("%w: bearer credential may only call /api routes", ErrForbidden)
	}

	// Layer 2: route -> required scope.
	scope, registered := RequiredScope(method, path)
	if !registered {
		return fmt.Errorf("%w: route %s %s has no registered scope", ErrForbidden, method, path)
	}
	if scope == "" {
		return fmt.Errorf("%w: route %s %s is not available to bearer credentials", ErrForbidden, method, path)
	}

	// Layer 3: reachability by kind.
	switch p.Kind {
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

	if resource == "goals" && method == "POST" && len(rest) == 3 && rest[2] == "save-as-workflow" {
		return scopeForMethod("workflows", method), true
	}

	switch resource {
	case "status":
		return "agent:" + ActionRead, true
	case "agents":
		return agentRouteScope(method, rest[1:])
	case "users":
		return usersRouteScope(method, rest[1:])
	case "goals", "workflows", "shares", "recally", "email", "mcp", "vault", "scheduler", "skills":
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
// added sub-route cannot silently become reachable. Object-level agent ownership
// for PAT/OAuth requests is enforced by each handler that loads the object.
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
	case "tools":
		return "", true
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
