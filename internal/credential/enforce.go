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
	"tools":              true,
}

// Enforce is the single authorization path. It runs in three explicit layers,
// all inside this file (this is where the former agent-binding coupling in
// scopedTokenAllowsRequest is fixed, not eliminated):
//
//  1. kind policy   -- legacy_stella_token bypasses API-scope checks (handler
//     ownership/admin checks still apply); pat/oauth/scoped must pass scope checks.
//  2. method+path -> required scope -- a shared route map applied to every scoped
//     bearer; an unregistered route is deny (fail-closed).
//  3. subject/object boundary by kind -- scoped tokens stay locked to their
//     agentID; pat/oauth use ordinary user ownership/admin checks at the handler.
//
// Cookie/OIDC sessions have no Principal and are never passed here.
func Enforce(p *Principal, method, path string) error {
	if p == nil {
		return fmt.Errorf("%w: no principal", ErrForbidden)
	}

	// Layer 1: kind policy.
	if p.Kind == KindLegacyStellaToken {
		return nil
	}

	// Scoped bearers are API-only; they may not fetch page routes.
	if !strings.HasPrefix(path, "/api/") {
		return fmt.Errorf("%w: scoped credential may only call /api routes", ErrForbidden)
	}

	// Layer 2: route -> required scope.
	scope, exposed, registered := RequiredScope(method, path)
	if !registered {
		return fmt.Errorf("%w: route %s %s has no registered scope", ErrForbidden, method, path)
	}
	if !exposed {
		return fmt.Errorf("%w: route %s %s is not available to scoped credentials", ErrForbidden, method, path)
	}
	if !MatchScope(p.Scopes, scope) {
		return fmt.Errorf("%w: missing scope %q", ErrForbidden, scope)
	}

	// Layer 3: subject/object boundary by kind.
	if p.Kind == KindScoped {
		if err := enforceAgentBoundary(p, path); err != nil {
			return err
		}
	}
	return nil
}

// RequiredScope maps an HTTP method + /api path to the scope a scoped bearer
// needs. It returns:
//
//   - scope:      the required resource:action (empty when not exposed)
//   - exposed:    whether scoped bearers may reach this route at all
//   - registered: whether the route's resource is known to the registry
//
// registered=false means a route exists that nobody classified. Enforce treats
// it as deny (fail-closed), and a test asserts every generated /api route is
// registered so the gap surfaces at build time, not in production.
func RequiredScope(method, path string) (scope string, exposed, registered bool) {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 || parts[0] != "api" {
		return "", false, false
	}
	rest := parts[1:]
	resource := rest[0]

	switch resource {
	case "status":
		return "agent:" + ActionRead, true, true
	case "agents":
		return agentRouteScope(method, rest[1:]), true, true
	case "users":
		return usersRouteScope(method, rest[1:])
	case "tasks", "goals", "shares", "recally", "email", "vault", "scheduler", "skills":
		return scopeForMethod(resource, method), true, true
	}

	if deniedResources[resource] {
		return "", false, true
	}
	return "", false, false
}

// agentRouteScope maps /api/agents/{id}/... sub-paths to their scope. The agent
// ownership boundary ({id} == principal.AgentID for scoped tokens) is a separate
// concern handled in enforceAgentBoundary (layer 3), not here.
func agentRouteScope(method string, sub []string) string {
	// sub is the path after /api/agents. sub[0] is the agent id.
	if len(sub) < 2 {
		return scopeForMethod("agent", method)
	}
	switch sub[1] {
	case "scheduler":
		return scopeForMethod("scheduler", method)
	case "skills":
		return scopeForMethod("skills", method)
	case "tasks":
		return scopeForMethod("tasks", method)
	default:
		return scopeForMethod("agent", method)
	}
}

// usersRouteScope exposes only the /api/users/me/oauth subtree to scoped
// bearers; all other /api/users routes are admin/self-management and denied.
func usersRouteScope(method string, sub []string) (string, bool, bool) {
	if len(sub) >= 2 && sub[0] == "me" && sub[1] == "oauth" {
		return scopeForMethod("oauth", method), true, true
	}
	return "", false, true
}

// enforceAgentBoundary locks a scoped token to its own agent: any
// /api/agents/{id}/... request must target the token's agentID.
func enforceAgentBoundary(p *Principal, path string) error {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 3 && parts[1] == "agents" {
		if p.AgentID == "" || parts[2] != p.AgentID {
			return fmt.Errorf("%w: scoped token is bound to a different agent", ErrForbidden)
		}
	}
	return nil
}
