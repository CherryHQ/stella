package credential

import (
	"strings"
	"testing"

	pkgauth "github.com/CherryHQ/stella/pkg/auth"
)

func TestEnforceScopedAgentBoundary(t *testing.T) {
	p := &Principal{Kind: KindScoped, AgentID: "agent-1", Scopes: pkgauth.DefaultSandboxScopes}

	if err := Enforce(p, "GET", "/api/agents/agent-1/skills"); err != nil {
		t.Fatalf("matching agent path should be allowed: %v", err)
	}
	if err := Enforce(p, "GET", "/api/agents/agent-2/skills"); err == nil {
		t.Fatal("different agent path must be denied")
	}
	if err := Enforce(p, "GET", "/api/status"); err != nil {
		t.Fatalf("status should be allowed: %v", err)
	}
	if err := Enforce(p, "GET", "/api/vault/EMAIL_CONFIG"); err != nil {
		t.Fatalf("scoped vault read should be allowed by default sandbox scopes: %v", err)
	}
	if err := Enforce(p, "POST", "/api/users/me/oauth/github/start"); err != nil {
		t.Fatalf("oauth connect should be allowed by default sandbox scopes: %v", err)
	}
}

func TestEnforceRequiresMappedScope(t *testing.T) {
	p := &Principal{Kind: KindScoped, AgentID: "agent-1", Scopes: []string{"tasks:read"}}

	if err := Enforce(p, "GET", "/api/tasks"); err != nil {
		t.Fatalf("tasks read should be allowed: %v", err)
	}
	if err := Enforce(p, "POST", "/api/tasks"); err == nil {
		t.Fatal("tasks write must be denied without tasks:write")
	}
	if err := Enforce(p, "GET", "/api/vault/EMAIL_CONFIG"); err == nil {
		t.Fatal("scoped vault read must be denied without vault:read")
	}
	if err := Enforce(p, "GET", "/api/users/me"); err == nil {
		t.Fatal("unmapped users/me profile must be denied")
	}
}

// A PAT with no matching scope must be DENIED, not silently full-access. This
// locks CRITICAL #1 (enforcement gated on kind/scopes, not info.Scoped != nil).
func TestEnforcePATWithoutScopeIsDenied(t *testing.T) {
	p := &Principal{Kind: KindPAT, UserID: "u1", Scopes: []string{"tasks:read"}}

	if err := Enforce(p, "GET", "/api/tasks"); err != nil {
		t.Fatalf("PAT with tasks:read should reach tasks read: %v", err)
	}
	if err := Enforce(p, "GET", "/api/goals"); err == nil {
		t.Fatal("PAT without goals scope must be denied on /api/goals")
	}
	// A PAT is not agent-bound, so it can read any agent it has the scope for,
	// but it must still be denied without the scope.
	empty := &Principal{Kind: KindPAT, UserID: "u1", Scopes: nil}
	if err := Enforce(empty, "GET", "/api/tasks"); err == nil {
		t.Fatal("PAT with no scopes must be denied, not granted full access")
	}
}

func TestEnforceRejectsRetiredLegacyKind(t *testing.T) {
	p := &Principal{Kind: Kind("legacy_stella_token"), UserID: "u1"}
	if err := Enforce(p, "GET", "/api/tasks"); err == nil {
		t.Fatal("retired legacy token kind must not bypass API-scope checks")
	}
}

// Unregistered routes fail closed for scoped bearers.
func TestEnforceUnregisteredRouteDenied(t *testing.T) {
	p := &Principal{Kind: KindPAT, Scopes: []string{"tasks:*", "channels:*"}}
	if err := Enforce(p, "GET", "/api/channels"); err == nil {
		t.Fatal("a resource not exposed to scoped bearers must be denied")
	}
	if err := Enforce(p, "GET", "/agents"); err == nil {
		t.Fatal("non-API page routes must be denied for scoped bearers")
	}
}

// A sandbox scoped token carries broad DefaultSandboxScopes (email:*, skills:*,
// scheduler:*), but must NOT reach the top-level user-facing APIs -- only the
// legacy sandbox surface and the agent-bound subtree. This locks the CRITICAL
// privilege-widening regression (CR-011) where the unified registry exposed
// top-level email/skills/scheduler to every injected sandbox token.
func TestEnforceScopedTokenCannotReachTopLevelExternalAPIs(t *testing.T) {
	p := &Principal{Kind: KindScoped, AgentID: "agent-1", Scopes: pkgauth.DefaultSandboxScopes}

	denied := [][2]string{
		{"GET", "/api/email/messages"},
		{"POST", "/api/email/send"},
		{"GET", "/api/skills"},
		{"POST", "/api/skills/upload"},
		{"GET", "/api/scheduler"},
	}
	for _, d := range denied {
		if err := Enforce(p, d[0], d[1]); err == nil {
			t.Errorf("sandbox scoped token must be denied top-level %s %s", d[0], d[1])
		}
	}
	// The agent-bound equivalents remain reachable (that is why the sandbox scope
	// set keeps skills:*/scheduler:*).
	if err := Enforce(p, "POST", "/api/agents/agent-1/skills/install"); err != nil {
		t.Fatalf("agent-bound skills must stay reachable by a sandbox token: %v", err)
	}
}

// A PAT reaches the top-level external APIs (its whole purpose) but must never
// reach the sandbox-internal vault/oauth surface, even if it somehow presented
// those scopes. Counterpart to the scoped-token restriction above (CR-011).
func TestEnforcePATReachesExternalButNotSandboxInternal(t *testing.T) {
	p := &Principal{Kind: KindPAT, UserID: "u1", Scopes: []string{"email:*", "skills:*", "vault:*", "oauth:*"}}

	if err := Enforce(p, "POST", "/api/email/send"); err != nil {
		t.Fatalf("PAT with email:* must reach /api/email/send: %v", err)
	}
	if err := Enforce(p, "GET", "/api/skills"); err != nil {
		t.Fatalf("PAT with skills:* must reach /api/skills: %v", err)
	}
	if err := Enforce(p, "GET", "/api/vault/EMAIL_CONFIG"); err == nil {
		t.Fatal("PAT must never reach vault even with vault:* present")
	}
	if err := Enforce(p, "POST", "/api/users/me/oauth/github/start"); err == nil {
		t.Fatal("PAT must never reach oauth even with oauth:* present")
	}
}

// An unknown agent sub-resource must fail closed (registered=false) rather than
// inherit the broad agent scope -- this is what makes the coverage test catch a
// newly added /api/agents/{id}/<new> at build time (CR-002).
func TestRequiredScopeUnknownAgentSubResourceFailsClosed(t *testing.T) {
	if _, registered := RequiredScope("POST", "/api/agents/a1/secrets"); registered {
		t.Fatal("unknown agent sub-resource must be registered=false (fail-closed)")
	}
	// Known sub-resources stay registered.
	for _, path := range []string{
		"/api/agents/a1/sessions",
		"/api/agents/a1/skills",
		"/api/agents/a1/scheduler/jobs",
		"/api/agents/a1/projects",
		"/api/agents/a1/users",
		"/api/agents/a1",
	} {
		if _, registered := RequiredScope("GET", path); !registered {
			t.Errorf("known route %s must be registered", path)
		}
	}
}

func TestValidatePATScopes(t *testing.T) {
	if _, ok := ValidatePATScopes(nil); ok {
		t.Fatal("empty scope set must be rejected")
	}
	if _, ok := ValidatePATScopes([]string{"tasks:read", "goals:*"}); !ok {
		t.Fatal("valid exposable scopes should pass")
	}
	if bad, ok := ValidatePATScopes([]string{"vault:read"}); ok || bad != "vault:read" {
		t.Fatalf("vault must not be grantable to a PAT; got bad=%q ok=%v", bad, ok)
	}
	if bad, ok := ValidatePATScopes([]string{"oauth:*"}); ok || bad != "oauth:*" {
		t.Fatalf("oauth must not be grantable to a PAT; got bad=%q ok=%v", bad, ok)
	}
	if _, ok := ValidatePATScopes([]string{"tasks"}); ok {
		t.Fatal("malformed scope (no action) must be rejected")
	}
	if _, ok := ValidatePATScopes([]string{"nope:read"}); ok {
		t.Fatal("unknown resource must be rejected")
	}
}

func TestExposableScopesExcludeSandboxInternal(t *testing.T) {
	for _, s := range ExposableScopes() {
		if strings.HasPrefix(s, "vault:") || strings.HasPrefix(s, "oauth:") {
			t.Fatalf("sandbox-internal scope %q must not be exposable to PATs", s)
		}
	}
}
