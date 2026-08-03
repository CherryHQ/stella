package credential

import "strings"

// Scope actions.
const (
	ActionRead  = "read"
	ActionWrite = "write"
)

// ScopeAgentWrite guards posting messages / triggering an agent run. The
// webhook ingress (POST /webhooks/{id}) is auth-exempt and bypasses Enforce,
// so it checks this constant directly; defining it here ties that surface to
// the same catalog mapping scopeForMethod applies to the equivalent /api
// routes (see TestScopeAgentWriteMatchesRouteMapping).
const ScopeAgentWrite = "agent:" + ActionWrite

// Scope is one entry in the authoritative API-permission catalog. These are the
// resource:action permissions checked at the HTTP boundary. They are a DIFFERENT
// axis from skill/vault storage scopes (system / user / agent ownership) -- do
// not conflate scopes.Resource with a skill's storage scope.
type Scope struct {
	// Resource is the left side of resource:action (e.g. "goals").
	Resource string
	// Description is human-facing copy for the PAT creation UI / OAuth consent.
	Description string
	// ExposableToPAT controls whether a PAT may be granted this resource.
	// vault/oauth are sandbox-internal and default-denied to external tokens.
	ExposableToPAT bool
	// ExposableToOAuth controls whether a third-party OAuth2 client may be
	// granted this resource via consent. Same default-deny policy as PATs:
	// vault:* / oauth:* are never grantable to an OAuth client.
	ExposableToOAuth bool
}

// catalog is the single authoritative scope registry. Wildcard resource:* is
// derived from these entries (a granted "goals:*" covers goals:read/goals:write).
var catalog = []Scope{
	// NOTE: the "agent" scope is coarse. Read grants the full agent surface --
	// config, sessions, conversation messages, and workspace file contents;
	// write additionally allows deleting the agent, managing its members, posting
	// messages, and writing workspace files. The Description states this so PAT
	// consent is not misleading. Splitting sessions/workspace into finer scopes is
	// a deferred product decision -- keep the copy honest until then.
	{Resource: "agent", Description: "Full agent access: read config, sessions, messages, and workspace files; write can delete agents, manage members, post messages, and modify files", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "goals", Description: "Manage goals", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "workflows", Description: "Manage reusable workflows", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "webhooks", Description: "Manage personal webhook invocation capabilities", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "scheduler", Description: "Manage scheduled jobs", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "skills", Description: "Manage skills, including installing and uploading skills that run as code in your sandbox", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "shares", Description: "Manage public shares", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "recally", Description: "Manage Recally articles, feeds, and digests", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "email", Description: "Read and send email", ExposableToPAT: true, ExposableToOAuth: true},
	{Resource: "mcp", Description: "Manage MCP server registrations", ExposableToPAT: true, ExposableToOAuth: true},
	// Sandbox-internal capabilities. Dangerous to hand a third party, so they are
	// NOT exposable to PATs or OAuth clients by default (default-deny policy).
	{Resource: "vault", Description: "Read and write encrypted secrets", ExposableToPAT: false, ExposableToOAuth: false},
	{Resource: "oauth", Description: "Manage linked OAuth accounts", ExposableToPAT: false, ExposableToOAuth: false},
}

var catalogByResource = func() map[string]Scope {
	m := make(map[string]Scope, len(catalog))
	for _, s := range catalog {
		m[s.Resource] = s
	}
	return m
}()

// Catalog returns the scope catalog (copy) for UI/consent surfaces.
func Catalog() []Scope {
	out := make([]Scope, len(catalog))
	copy(out, catalog)
	return out
}

// ExposableScopes returns the concrete scope strings a PAT may be granted
// (resource:read and resource:write for every exposable resource, plus the
// resource:* wildcard).
func ExposableScopes() []string {
	var out []string
	for _, s := range catalog {
		if !s.ExposableToPAT {
			continue
		}
		out = append(out, s.Resource+":"+ActionRead, s.Resource+":"+ActionWrite, s.Resource+":*")
	}
	return out
}

// ValidatePATScopes rejects unknown, malformed, or non-exposable scopes. It
// returns the first offending scope. An empty scope set is rejected: a PAT with
// no scope can do nothing and is almost always a mistake.
func ValidatePATScopes(scopes []string) (string, bool) {
	if len(scopes) == 0 {
		return "", false
	}
	for _, sc := range scopes {
		resource, action, ok := strings.Cut(sc, ":")
		if !ok || resource == "" || action == "" {
			return sc, false
		}
		entry, known := catalogByResource[resource]
		if !known || !entry.ExposableToPAT {
			return sc, false
		}
		if action != ActionRead && action != ActionWrite && action != "*" {
			return sc, false
		}
	}
	return "", true
}

// OAuthGrantableScopes returns the concrete scope strings an OAuth2 client may
// be granted (resource:read, resource:write, and resource:* for every
// OAuth-exposable resource).
func OAuthGrantableScopes() []string {
	var out []string
	for _, s := range catalog {
		if !s.ExposableToOAuth {
			continue
		}
		out = append(out, s.Resource+":"+ActionRead, s.Resource+":"+ActionWrite, s.Resource+":*")
	}
	return out
}

// ValidateOAuthScopes rejects unknown, malformed, or non-OAuth-exposable scopes,
// returning the first offending scope. It is the client-allowed-scope gate: a
// client may only be registered with scopes that pass here. An empty set is
// allowed (a client with no default scopes requests them per-authorization).
func ValidateOAuthScopes(scopes []string) (string, bool) {
	for _, sc := range scopes {
		resource, action, ok := strings.Cut(sc, ":")
		if !ok || resource == "" || action == "" {
			return sc, false
		}
		entry, known := catalogByResource[resource]
		if !known || !entry.ExposableToOAuth {
			return sc, false
		}
		if action != ActionRead && action != ActionWrite && action != "*" {
			return sc, false
		}
	}
	return "", true
}

// ScopesSubset reports whether every scope in sub is satisfied by super, where a
// resource:* in super covers resource:read/resource:write in sub. This is the
// subset-chain primitive: issued scopes <= code scopes <= client scopes <= user
// permissions. It returns the first scope in sub not covered by super.
func ScopesSubset(sub, super []string) (string, bool) {
	for _, s := range sub {
		if !MatchScope(super, s) {
			return s, false
		}
	}
	return "", true
}

// MatchScope reports whether a granted scope set satisfies a required
// resource:action. A granted resource:* covers any action on that resource.
func MatchScope(granted []string, required string) bool {
	if required == "" {
		return false
	}
	prefix, _, ok := strings.Cut(required, ":")
	wildcard := ""
	if ok {
		wildcard = prefix + ":*"
	}
	for _, g := range granted {
		if g == required || (wildcard != "" && g == wildcard) {
			return true
		}
	}
	return false
}

// scopeForMethod maps an HTTP method to the read/write action scope for a
// resource.
func scopeForMethod(resource, method string) string {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return resource + ":" + ActionRead
	default:
		return resource + ":" + ActionWrite
	}
}
