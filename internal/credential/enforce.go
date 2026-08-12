package credential

import (
	"errors"
	"fmt"
	"strings"
)

// ErrForbidden is returned by Enforce when a principal may not perform a request.
var ErrForbidden = errors.New("permission denied")

// deniedResources are top-level /api resources OAuth access tokens may never
// reach. They are session- or admin-only. They are listed EXPLICITLY -- an
// unlisted, unmapped resource is treated as a
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
	"provisioned-users":  true,
	"tools":              true,
	"vision-settings":    true,
}

// Enforce covers bearer kind + route scope. Object-level ownership (this user
// owns this task/agent/session) is NOT done here -- it remains a per-handler
// responsibility for PAT/OAuth, exactly as for cookie sessions. PATs are
// API-only credentials for their owner's current authority and pass this gate;
// OAuth access tokens additionally run through route-to-scope enforcement.
//
// OAuth enforcement runs three explicit layers:
//
//  1. method+path -> required scope -- RequiredScope classifies every /api route
//     to a scope; an unregistered route is deny (fail-closed).
//  2. OAuth reachability -- the catalog's OAuth-exposable surface.
//  3. granted scope match.
//
// Cookie/OIDC sessions have no Principal and are never passed here.
func Enforce(p *Principal, method, path string) error {
	if p == nil {
		return fmt.Errorf("%w: no principal", ErrForbidden)
	}

	// Bearer credentials are API-only; they may not fetch page routes. Public
	// capability routes such as POST /webhooks/{id} use their own authentication
	// boundary and never pass through this resolver.
	if !strings.HasPrefix(path, "/api/") {
		return fmt.Errorf("%w: bearer credential may only call /api routes", ErrForbidden)
	}

	switch p.Kind {
	case KindPAT:
		if patCredentialRouteDenied(path) {
			return fmt.Errorf("%w: PAT may not manage authentication credentials", ErrForbidden)
		}
		return nil
	case KindProvisioning:
		if !provisionedUserRouteAllowed(path) {
			return fmt.Errorf("%w: provisioning credentials may only access provisioned users", ErrForbidden)
		}
		return nil
	case KindOAuth:
		// OAuth access tokens are delegated capabilities, not account credentials.
		// Preserve the existing route-to-scope and OAuth-exposability policy.
		scope, registered := RequiredScope(method, path)
		if !registered {
			return fmt.Errorf("%w: route %s %s has no registered scope", ErrForbidden, method, path)
		}
		if scope == "" {
			return fmt.Errorf("%w: route %s %s is not available to bearer credentials", ErrForbidden, method, path)
		}
		if !oauthReachable(scope) {
			return fmt.Errorf("%w: route %s %s is not available to this token", ErrForbidden, method, path)
		}
		if !MatchScope(p.Scopes, scope) {
			return fmt.Errorf("%w: missing scope %q", ErrForbidden, scope)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown credential kind %q", ErrForbidden, p.Kind)
	}
}

// patCredentialRouteDenied is the parent-layer fence for PATs. A PAT inherits
// its owner's business and admin authority, but it must not create or control
// authentication credentials, browser sessions, or identities: a stolen bearer
// must neither reproduce itself nor become an account-takeover primitive.
//
// Keep this policy here, before handlers, rather than duplicating it across
// self-service and admin endpoints. The patterns deliberately compare complete
// path segments: family rules include their root and descendants, while exact
// rules do not grow to future sub-routes by accident.
func patCredentialRouteDenied(path string) bool {
	rest := apiSegments(path)
	switch {
	case hasSegmentPrefix(rest, "users", "me", "tokens"),
		hasSegmentPrefix(rest, "admin", "provisioning-tokens"),
		hasSegmentPrefix(rest, "provisioned-users"),
		hasSegmentPrefix(rest, "users", "me", "oauth-clients"),
		hasSegmentPrefix(rest, "users", "me", "authorized-apps"),
		hasSegmentPrefix(rest, "auth", "sessions"),
		hasSegmentPrefix(rest, "users", "me", "identities"):
		return true
	case len(rest) >= 3 && rest[0] == "users" && rest[2] == "identities":
		return true
	case hasExactSegments(rest, "users", "me", "password"),
		hasExactSegments(rest, "users", "me", "link-code"),
		len(rest) == 3 && rest[0] == "users" && (rest[2] == "role" || rest[2] == "active"):
		return true
	default:
		return false
	}
}

// provisionedUserRouteAllowed is deliberately an allowlist: a provisioning
// credential has no implicit owner authority, and future routes stay denied
// until they are placed under this resource family on purpose.
func provisionedUserRouteAllowed(path string) bool {
	return hasSegmentPrefix(apiSegments(path), "provisioned-users")
}

func hasSegmentPrefix(got []string, want ...string) bool {
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func hasExactSegments(got []string, want ...string) bool {
	return len(got) == len(want) && hasSegmentPrefix(got, want...)
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

// oauthReachable reports whether an OAuth token may reach a route requiring the
// given scope. Vault/oauth are sandbox-internal and never delegated to an OAuth
// client. Object ownership is still checked by the handler.
func oauthReachable(scope string) bool {
	resource, _, _ := strings.Cut(scope, ":")
	entry, ok := catalogByResource[resource]
	return ok && entry.ExposableToOAuth
}

// RequiredScope maps an HTTP method + /api path to the scope needed to reach it.
// It returns:
//
//   - scope:      the required resource:action; "" for a known OAuth-token-denied
//     route (admin/auth/self-management).
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
	case "library-files":
		// Keep the HTTP noun explicit while presenting the shorter
		// library:read/write capability in PAT and OAuth consent surfaces.
		return scopeForMethod("library", method), true
	case "agents":
		return agentRouteScope(method, rest[1:])
	case "users":
		return usersRouteScope(method, rest[1:])
	case "goals", "workflows", "webhooks", "shares", "recally", "email", "mcp", "vault", "scheduler", "skills":
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
	case "sessions", "projects", "users", "provider-credentials":
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
