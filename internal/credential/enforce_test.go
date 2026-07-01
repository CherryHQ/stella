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

// legacy_stella_token bypasses API-scope checks (but not handler ownership/admin,
// which live outside Enforce).
func TestEnforceLegacyBypassesScopes(t *testing.T) {
	p := &Principal{Kind: KindLegacyStellaToken, UserID: "u1"}
	for _, path := range []string{"/api/tasks", "/api/goals", "/api/anything-unmapped"} {
		if err := Enforce(p, "POST", path); err != nil {
			t.Fatalf("legacy token must bypass API-scope checks for %s: %v", path, err)
		}
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
